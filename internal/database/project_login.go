package database

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func projectLogin(projectID string) string {
	sum := sha256.Sum256([]byte(projectID))
	return "dp_" + hex.EncodeToString(sum[:16])
}

// DropProjectLogin is called only after its project database has been removed.
func (pm *PoolManager) DropProjectLogin(ctx context.Context, projectID string) error {
	_, err := pm.platformPool.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{projectLogin(projectID)}.Sanitize())
	return err
}

func (pm *PoolManager) projectURL(projectID, dbName string) string {
	mac := hmac.New(sha256.New, []byte(pm.cfg.PlatformJWTSecret))
	mac.Write([]byte("dupabase/project-login/v1/" + projectID))
	u := *pm.baseURL
	u.Path, u.RawPath = "/"+dbName, ""
	u.User = url.UserPassword(projectLogin(projectID), hex.EncodeToString(mac.Sum(nil)))
	q := u.Query()
	// libpq/pgx query parameters must not override the restricted login.
	for _, key := range []string{"user", "password", "dbname", "database"} {
		q.Del(key)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ProvisioningPool must never be used to execute tenant-supplied SQL.
func (pm *PoolManager) ProvisioningPool(ctx context.Context, dbName string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(pm.buildDBURL(dbName))
	if err != nil {
		return nil, err
	}
	cfg.MaxConns, cfg.MinConns = 1, 0
	return pgxpool.NewWithConfig(ctx, cfg)
}

// ProjectDatabaseURL returns the confined credential used by PostgreSQL tools.
// It first ensures existing projects have been upgraded to restricted logins.
func (pm *PoolManager) ProjectDatabaseURL(ctx context.Context, dbName string) (string, error) {
	var id string
	if err := pm.platformPool.QueryRow(ctx, `SELECT id FROM platform.projects WHERE db_name = $1 AND status = 'active'`, dbName).Scan(&id); err != nil {
		return "", fmt.Errorf("active project not found: %w", err)
	}
	if _, err := pm.GetPool(ctx, id); err != nil {
		return "", err
	}
	return pm.projectURL(id, dbName), nil
}

// ProvisionProjectLogin upgrades only this database's application-owned objects.
// No REASSIGN OWNED is used: that also changes shared database/tablespace ownership.
// The account's existing PostgreSQL login inherits the project owner so its
// connection details continue to work after the ownership change.
func (pm *PoolManager) ProvisionProjectLogin(ctx context.Context, projectID, dbName, accountLogin string) error {
	admin, err := pm.ProvisioningPool(ctx, dbName)
	if err != nil {
		return err
	}
	defer admin.Close()
	tx, err := admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('dupabase/project-login/' || current_database(), 0))`); err != nil {
		return err
	}
	login := projectLogin(projectID)
	quoted := pgx.Identifier{login}.Sanitize()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)`, login).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, "CREATE ROLE "+quoted+" LOGIN"); err != nil {
			return fmt.Errorf("create project login: %w", err)
		}
	}
	u, _ := url.Parse(pm.projectURL(projectID, dbName))
	password, _ := u.User.Password()
	// The derived password contains hexadecimal characters only.
	if _, err = tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD '%s'", quoted, password)); err != nil {
		return fmt.Errorf("restrict project login: %w", err)
	}
	// Tenant SQL cannot grant roles to this login. Preserve operator-provisioned
	// custom API-role memberships; PostgreSQL enforces SET ROLE authorization.
	if _, err = tx.Exec(ctx, "GRANT anon, authenticated, service_role TO "+quoted); err != nil {
		return err
	}
	if accountLogin != "" && accountLogin != login {
		if _, err = tx.Exec(ctx, "GRANT "+quoted+" TO "+pgx.Identifier{accountLogin}.Sanitize()); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{dbName}.Sanitize()+" OWNER TO "+quoted); err != nil {
		return err
	}
	// All names below come from catalogs and are quoted by PostgreSQL format().
	if _, err = tx.Exec(ctx, `SELECT set_config('dupabase.project_owner', $1, true)`, login); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, projectOwnershipSQL); err != nil {
		return fmt.Errorf("confine project ownership: %w", err)
	}
	if _, err = tx.Exec(ctx, "GRANT ALL ON SCHEMA public, auth TO "+quoted); err != nil {
		return err
	}
	for _, kind := range []string{"TABLES", "SEQUENCES", "ROUTINES"} {
		if _, err = tx.Exec(ctx, "ALTER DEFAULT PRIVILEGES FOR ROLE "+quoted+" IN SCHEMA public GRANT ALL ON "+kind+" TO anon, authenticated, service_role"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// set_config passes the login as data, so the ownership block stays constant.
var projectOwnershipSQL = strings.Join([]string{
	`DO $ownership$
DECLARE obj record; owner_name text := current_setting('dupabase.project_owner');
BEGIN
  FOR obj IN SELECT n.oid, n.nspname FROM pg_namespace n
    WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname NOT IN ('information_schema','platform','extensions')
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_namespace'::regclass AND d.objid=n.oid AND d.deptype='e')
      AND n.nspowner <> owner_name::regrole
  LOOP
    EXECUTE format('ALTER SCHEMA %I OWNER TO %I', obj.nspname, owner_name);
  END LOOP;
  FOR obj IN SELECT n.nspname, c.relname, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname NOT IN ('information_schema','platform','extensions')
      AND c.relkind IN ('r','p','v','m','S','f')
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')
      AND c.relowner <> owner_name::regrole
    ORDER BY CASE c.relkind WHEN 'S' THEN 1 ELSE 0 END
  LOOP
    EXECUTE format('ALTER %s %I.%I OWNER TO %I', CASE obj.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'S' THEN 'SEQUENCE' WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END, obj.nspname, obj.relname, owner_name);
  END LOOP;
  FOR obj IN SELECT n.nspname, p.proname, p.prokind, pg_get_function_identity_arguments(p.oid) args FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname NOT IN ('information_schema','platform','extensions')
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')
      AND p.proowner <> owner_name::regrole
  LOOP
    EXECUTE format('ALTER %s %I.%I(%s) OWNER TO %I', CASE obj.prokind WHEN 'p' THEN 'PROCEDURE' WHEN 'a' THEN 'AGGREGATE' ELSE 'FUNCTION' END, obj.nspname, obj.proname, obj.args, owner_name);
  END LOOP;
  FOR obj IN SELECT n.nspname, t.typname, t.typtype FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
    LEFT JOIN pg_class c ON c.oid=t.typrelid
    WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname NOT IN ('information_schema','platform','extensions')
      AND (t.typtype IN ('e','d','r') OR (t.typtype='c' AND c.relkind='c'))
      AND t.typowner <> owner_name::regrole
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype='e')
  LOOP
    EXECUTE format('ALTER %s %I.%I OWNER TO %I', CASE obj.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, obj.nspname, obj.typname, owner_name);
  END LOOP;
END $ownership$;`,
}, "\n")
