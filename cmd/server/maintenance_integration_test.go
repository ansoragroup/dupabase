package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/config"
	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/ansoraGROUP/dupabase/internal/platform"
	"github.com/jackc/pgx/v5"
)

func TestMaintenanceDatabaseCompatibility(t *testing.T) {
	dsn := os.Getenv("DUPABASE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DUPABASE_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || !strings.HasPrefix(u.Path, "/dupabase_maintenance_") {
		t.Fatal("requires an explicitly disposable loopback database named dupabase_maintenance_*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.NewPlatformPool(ctx, dsn, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.RunMigrations(ctx, db, platformMigrations()); err != nil {
		t.Fatal(err)
	}
	// Repeated startup must preserve applied migrations and settings.
	if err = database.RunMigrations(ctx, db, platformMigrations()); err != nil {
		t.Fatal(err)
	}
	const secret = "isolated-maintenance-integration-jwt-secret-20261001"
	const password = "IsolatedRegressionPassword2026!"
	cfg := &config.Config{DatabaseURL: dsn, PlatformJWTSecret: secret, MaxConnectionsPerDB: 2, GlobalMaxConnections: 10, PoolIdleTimeout: 300, DefaultEnableSignup: false, DefaultAutoconfirm: false, DefaultPasswordMinLength: 10}
	pm, err := database.NewPoolManager(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	defer pm.Shutdown()
	auth := platform.NewAuthService(db, secret, 3600)
	fixtureEmail := fmt.Sprintf("maintenance-%d@example.test", time.Now().UnixNano())
	registered, status, err := auth.Register(ctx, platform.RegisterRequest{Email: fixtureEmail, Password: password})
	if err != nil || status != 201 {
		t.Fatalf("register fixture: status=%d error=%v", status, err)
	}
	userID, account := registered.User.ID, registered.User.PgUsername
	orgs := platform.NewOrgService(db)
	orgID, err := orgs.GetPersonalOrgID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	projects := platform.NewProjectService(db, pm, "http://127.0.0.1:3333", 365)
	var projectID, projectLogin string
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if projectID != "" {
			if _, err := projects.DeleteProject(cleanup, orgID, projectID); err != nil {
				t.Error(err)
			}
		}
		_, _ = db.Exec(cleanup, `DELETE FROM platform.users WHERE id=$1`, userID)
		for _, role := range []string{projectLogin, account} {
			if role != "" {
				if _, err := db.Exec(cleanup, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); err != nil {
					t.Error(err)
				}
			}
		}
	}()

	t.Run("existing administrator account requires password proof", func(t *testing.T) {
		if err := auth.EnsureAdmin(ctx, fixtureEmail, "DifferentFixturePassword2026!"); err == nil {
			t.Fatal("mismatched bootstrap password promoted a registered account")
		}
		if auth.IsAdmin(ctx, userID) {
			t.Fatal("failed bootstrap changed account authority")
		}
		if err := auth.EnsureAdmin(ctx, fixtureEmail, password); err != nil || !auth.IsAdmin(ctx, userID) {
			t.Fatalf("matching account bootstrap: %v", err)
		}
	})
	project, status, err := projects.CreateProject(ctx, userID, platform.CreateProjectRequest{Name: "default-security", OrgID: orgID})
	if err != nil || status != 201 {
		t.Fatalf("provision fixture: status=%d error=%v", status, err)
	}
	projectID = project.ID
	t.Run("new projects retain configured defaults", func(t *testing.T) {
		if project.Settings.EnableSignup || project.Settings.Autoconfirm || project.Settings.PasswordMinLength != 10 {
			t.Fatalf("configured defaults ignored: %+v", project.Settings)
		}
	})
	projectPool, err := pm.GetPool(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectPool.QueryRow(ctx, `SELECT session_user`).Scan(&projectLogin); err != nil {
		t.Fatal(err)
	}
	pm.ClosePool(projectID)
	operator, err := pm.ProvisioningPool(ctx, project.DBName)
	if err != nil {
		t.Fatal(err)
	}
	_, err = operator.Exec(ctx, `
CREATE TYPE public.legacy_state AS ENUM ('saved');
CREATE DOMAIN public.legacy_label AS text CHECK (length(VALUE)>0);
CREATE TYPE public.legacy_pair AS (key text,value text);
CREATE TABLE public.legacy_records (id bigserial PRIMARY KEY, state public.legacy_state, label public.legacy_label);
INSERT INTO public.legacy_records(state,label) VALUES ('saved','preserved');
CREATE FUNCTION public.legacy_value() RETURNS text LANGUAGE sql AS $$ SELECT 'original'::text $$;
`)
	operator.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("legacy ownership upgrades preserve data and direct credentials", func(t *testing.T) {
		pool, err := pm.GetPool(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		var current, session, label string
		var isSuper bool
		if err := pool.QueryRow(ctx, `SELECT current_user,session_user,rolsuper FROM pg_roles WHERE rolname=current_user`).Scan(&current, &session, &isSuper); err != nil || current != projectLogin || session != projectLogin || isSuper {
			t.Fatalf("restricted login: %s %s %v %v", current, session, isSuper, err)
		}
		if err := pool.QueryRow(ctx, `SELECT label FROM public.legacy_records`).Scan(&label); err != nil || label != "preserved" {
			t.Fatalf("legacy data: %v", err)
		}
		if _, err := pool.Exec(ctx, `ALTER TYPE public.legacy_state ADD VALUE 'new'; ALTER DOMAIN public.legacy_label DROP CONSTRAINT legacy_label_check; ALTER TYPE public.legacy_pair ADD ATTRIBUTE extra text; CREATE OR REPLACE FUNCTION public.legacy_value() RETURNS text LANGUAGE sql AS $$ SELECT 'updated'::text $$;`); err != nil {
			t.Fatalf("legacy object ownership: %v", err)
		}
		var encrypted string
		if err := db.QueryRow(ctx, `SELECT pg_password_encrypted FROM platform.pg_users WHERE user_id=$1`, userID).Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		pgPassword, err := platform.DecryptPgPassword(encrypted, password)
		if err != nil {
			t.Fatal(err)
		}
		directURL := *u
		directURL.Path = "/" + project.DBName
		directURL.User = url.UserPassword(account, pgPassword)
		direct, err := pgx.Connect(ctx, directURL.String())
		if err != nil {
			t.Fatal("existing direct PostgreSQL credential stopped connecting")
		}
		defer direct.Close(ctx)
		if _, err := direct.Exec(ctx, `INSERT INTO public.legacy_records(state,label) VALUES ('new','direct-client')`); err != nil {
			t.Fatalf("direct client CRUD: %v", err)
		}
	})
	t.Run("operator granted custom API roles survive pool recreation", func(t *testing.T) {
		customRole := fmt.Sprintf("maintenance_custom_%d", time.Now().UnixNano())
		quoted := pgx.Identifier{customRole}.Sanitize()
		if _, err := db.Exec(ctx, "CREATE ROLE "+quoted+" NOLOGIN"); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(context.Background(), "DROP ROLE "+quoted)
		if _, err := db.Exec(ctx, "GRANT "+quoted+" TO "+pgx.Identifier{projectLogin}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		pm.ClosePool(projectID)
		pool, err := pm.GetPool(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		who, err := database.ExecuteWithRLS(ctx, pool, customRole, database.JWTClaims{"role": customRole}, func(tx pgx.Tx) (string, error) {
			var who string
			err := tx.QueryRow(ctx, `SELECT current_user`).Scan(&who)
			return who, err
		})
		if err != nil || who != customRole {
			t.Fatalf("custom API role compatibility: who=%s error=%v", who, err)
		}
		_, err = database.ExecuteWithRLS(ctx, pool, "postgres", database.JWTClaims{"role": "postgres"}, func(tx pgx.Tx) (string, error) { return "unsafe", nil })
		if err == nil {
			t.Fatal("an ungranted operator role must remain inaccessible")
		}
	})
	t.Run("backup settings are independent across organizations", func(t *testing.T) {
		shared, status, err := orgs.CreateOrg(ctx, userID, platform.CreateOrgRequest{Name: "maintenance-shared", Slug: fmt.Sprintf("maintenance-%d", time.Now().UnixNano())})
		if err != nil || status != 201 {
			t.Fatalf("shared org: %v", err)
		}
		backups := platform.NewBackupService(db, dsn, secret)
		req := platform.SaveBackupSettingsRequest{S3Endpoint: "https://storage.example.test", S3Bucket: "personal", S3AccessKey: "fixture", S3SecretKey: "fixture", PlatformPassword: password}
		personal, status, err := backups.SaveSettings(ctx, userID, orgID, req)
		if err != nil || status != 200 {
			t.Fatalf("personal settings: %v status=%d", err, status)
		}
		req.S3Bucket = "shared"
		other, status, err := backups.SaveSettings(ctx, userID, shared.ID, req)
		if err != nil || status != 200 || other.ID == personal.ID {
			t.Fatalf("shared settings overwrote personal: %v status=%d", err, status)
		}
		unchanged, _, err := backups.GetSettings(ctx, orgID)
		if err != nil || unchanged.S3Bucket != "personal" {
			t.Fatalf("personal settings changed: %v", err)
		}
	})
}
