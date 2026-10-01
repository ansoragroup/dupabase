package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProjectService struct {
	platformDB       *pgxpool.Pool
	poolManager      *database.PoolManager
	siteURL          string
	apiKeyExpiryDays int
}

func NewProjectService(platformDB *pgxpool.Pool, pm *database.PoolManager, siteURL string, apiKeyExpiryDays int) *ProjectService {
	if apiKeyExpiryDays <= 0 {
		apiKeyExpiryDays = 365
	}
	return &ProjectService{
		platformDB:       platformDB,
		poolManager:      pm,
		siteURL:          siteURL,
		apiKeyExpiryDays: apiKeyExpiryDays,
	}
}

type CreateProjectRequest struct {
	Name              string `json:"name"`
	OrgID             string `json:"org_id"`
	EnableSignup      *bool  `json:"enable_signup,omitempty"`
	Autoconfirm       *bool  `json:"autoconfirm,omitempty"`
	PasswordMinLength *int   `json:"password_min_length,omitempty"`
}

type ProjectSettings struct {
	EnableSignup      bool `json:"enable_signup"`
	Autoconfirm       bool `json:"autoconfirm"`
	PasswordMinLength int  `json:"password_min_length"`
}

type ProjectResponse struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	DBName         string          `json:"db_name"`
	Region         string          `json:"region"`
	AnonKey        string          `json:"anon_key"`
	ServiceRoleKey string          `json:"service_role_key"`
	JWTSecret      string          `json:"jwt_secret"`
	Status         string          `json:"status"`
	APIURL         string          `json:"api_url"`
	SiteURL        string          `json:"site_url"`
	Settings       ProjectSettings `json:"settings"`
	CreatedAt      time.Time       `json:"created_at"`
}

// CreateProject creates a new database, runs migrations, and generates API keys.
func (s *ProjectService) CreateProject(ctx context.Context, userID string, req CreateProjectRequest) (*ProjectResponse, int, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("project name is required")
	}

	// Get user's PG user info
	var pgUserID, pgUsername string
	err := s.platformDB.QueryRow(ctx, `
		SELECT id, pg_username FROM platform.pg_users WHERE user_id = $1
	`, userID).Scan(&pgUserID, &pgUsername)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("get pg user: %w", err)
	}

	// Generate unique database name
	randBytes := make([]byte, 8)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate random: %w", err)
	}
	dbName := "proj_" + hex.EncodeToString(randBytes)

	// Generate project JWT secret
	secretBytes := make([]byte, 64)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate jwt secret: %w", err)
	}
	jwtSecret := hex.EncodeToString(secretBytes)

	// Insert project with status 'creating'
	var projectID string
	var createdAt time.Time

	defaults := s.poolManager.Config()
	enableSignup := defaults.DefaultEnableSignup
	if req.EnableSignup != nil {
		enableSignup = *req.EnableSignup
	}
	autoconfirm := defaults.DefaultAutoconfirm
	if req.Autoconfirm != nil {
		autoconfirm = *req.Autoconfirm
	}
	passwordMinLen := defaults.DefaultPasswordMinLength
	if req.PasswordMinLength != nil {
		passwordMinLen = *req.PasswordMinLength
	}
	if passwordMinLen < 6 || passwordMinLen > 72 {
		return nil, http.StatusBadRequest, fmt.Errorf("password_min_length must be between 6 and 72")
	}

	err = s.platformDB.QueryRow(ctx, `
		INSERT INTO platform.projects (user_id, pg_user_id, name, db_name, jwt_secret, anon_key, service_role_key,
			enable_signup, autoconfirm, password_min_length, site_url, status, org_id)
		VALUES ($1, $2, $3, $4, $5, '', '', $6, $7, $8, $9, 'creating', $10)
		RETURNING id, created_at
	`, userID, pgUserID, name, dbName, jwtSecret,
		enableSignup, autoconfirm, passwordMinLen, s.siteURL, req.OrgID,
	).Scan(&projectID, &createdAt)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			return nil, http.StatusConflict, fmt.Errorf("project name already exists")
		}
		return nil, http.StatusInternalServerError, fmt.Errorf("insert project: %w", err)
	}

	// Create database
	_, err = s.platformDB.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, dbName, pgUsername))
	if err != nil {
		// Cleanup
		s.platformDB.Exec(ctx, `DELETE FROM platform.projects WHERE id = $1`, projectID)
		return nil, http.StatusInternalServerError, fmt.Errorf("create database: %w", err)
	}

	// Lock down database access — only the owner and superuser can connect
	if _, err := s.platformDB.Exec(ctx, fmt.Sprintf(`REVOKE CONNECT ON DATABASE "%s" FROM PUBLIC`, dbName)); err != nil {
		slog.Error("failed to revoke public connect", "db", dbName, "error", err)
		s.platformDB.Exec(ctx, `DELETE FROM platform.projects WHERE id = $1`, projectID)
		s.platformDB.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbName))
		return nil, http.StatusInternalServerError, fmt.Errorf("secure database: %w", err)
	}

	// Cleanup function for failures after DB creation
	cleanupOnError := func() {
		s.platformDB.Exec(ctx, `DELETE FROM platform.projects WHERE id = $1`, projectID)
		s.platformDB.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbName))
		slog.Error("project creation failed, cleaned up", "project_id", projectID, "db", dbName)
	}

	// Connect to the new database and run migrations
	projectPool, err := s.poolManager.ProvisioningPool(ctx, dbName)
	if err != nil {
		cleanupOnError()
		return nil, http.StatusInternalServerError, fmt.Errorf("connect to new db: %w", err)
	}

	// Run project migrations
	err = database.RunMigrations(ctx, projectPool, projectMigrations())
	projectPool.Close()
	if err != nil {
		cleanupOnError()
		return nil, http.StatusInternalServerError, fmt.Errorf("run migrations: %w", err)
	}

	if err := s.poolManager.ProvisionProjectLogin(ctx, projectID, dbName, pgUsername); err != nil {
		cleanupOnError()
		return nil, http.StatusInternalServerError, fmt.Errorf("provision project login: %w", err)
	}

	// Generate anon_key and service_role_key
	anonKey, err := generateProjectAPIKey(jwtSecret, projectID, "anon", s.apiKeyExpiryDays)
	if err != nil {
		cleanupOnError()
		return nil, http.StatusInternalServerError, fmt.Errorf("generate anon key: %w", err)
	}
	serviceRoleKey, err := generateProjectAPIKey(jwtSecret, projectID, "service_role", s.apiKeyExpiryDays)
	if err != nil {
		cleanupOnError()
		return nil, http.StatusInternalServerError, fmt.Errorf("generate service role key: %w", err)
	}

	// Update project with keys and set to active
	_, err = s.platformDB.Exec(ctx, `
		UPDATE platform.projects
		SET anon_key = $1, service_role_key = $2, status = 'active', updated_at = NOW()
		WHERE id = $3
	`, anonKey, serviceRoleKey, projectID)
	if err != nil {
		cleanupOnError()
		return nil, http.StatusInternalServerError, fmt.Errorf("update project keys: %w", err)
	}

	// Invalidate cache so next request picks up the new keys
	s.poolManager.InvalidateProjectCache(projectID)

	return &ProjectResponse{
		ID:             projectID,
		Name:           name,
		DBName:         dbName,
		Region:         "local",
		AnonKey:        anonKey,
		ServiceRoleKey: serviceRoleKey,
		JWTSecret:      jwtSecret,
		Status:         "active",
		APIURL:         s.siteURL,
		SiteURL:        s.siteURL,
		Settings: ProjectSettings{
			EnableSignup:      enableSignup,
			Autoconfirm:       autoconfirm,
			PasswordMinLength: passwordMinLen,
		},
		CreatedAt: createdAt,
	}, http.StatusCreated, nil
}

// ListProjects returns all projects for an organization.
func (s *ProjectService) ListProjects(ctx context.Context, orgID string) ([]ProjectResponse, error) {
	rows, err := s.platformDB.Query(ctx, `
		SELECT id, name, db_name, region, anon_key, service_role_key, '' AS jwt_secret,
			status, site_url, enable_signup, autoconfirm, password_min_length, created_at
		FROM platform.projects
		WHERE org_id = $1 AND status != 'deleted'
		ORDER BY created_at DESC
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []ProjectResponse
	for rows.Next() {
		var p ProjectResponse
		if err := rows.Scan(&p.ID, &p.Name, &p.DBName, &p.Region,
			&p.AnonKey, &p.ServiceRoleKey, &p.JWTSecret,
			&p.Status, &p.SiteURL,
			&p.Settings.EnableSignup, &p.Settings.Autoconfirm, &p.Settings.PasswordMinLength,
			&p.CreatedAt); err != nil {
			return nil, err
		}
		p.APIURL = s.siteURL
		projects = append(projects, p)
	}

	if projects == nil {
		projects = []ProjectResponse{}
	}

	return projects, nil
}

// GetProject returns a single project by ID for an organization, including the real jwt_secret.
func (s *ProjectService) GetProject(ctx context.Context, orgID, projectID string) (*ProjectResponse, int, error) {
	var p ProjectResponse
	err := s.platformDB.QueryRow(ctx, `
		SELECT id, name, db_name, region, anon_key, service_role_key, jwt_secret,
			status, site_url, enable_signup, autoconfirm, password_min_length, created_at
		FROM platform.projects
		WHERE id = $1 AND org_id = $2 AND status = 'active'
	`, projectID, orgID).Scan(&p.ID, &p.Name, &p.DBName, &p.Region,
		&p.AnonKey, &p.ServiceRoleKey, &p.JWTSecret,
		&p.Status, &p.SiteURL,
		&p.Settings.EnableSignup, &p.Settings.Autoconfirm, &p.Settings.PasswordMinLength,
		&p.CreatedAt)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}
	p.APIURL = s.siteURL
	return &p, http.StatusOK, nil
}

// DeleteProject drops a database and marks the project as deleted.
// orgID is used to verify the project belongs to the given organization.
func (s *ProjectService) DeleteProject(ctx context.Context, orgID, projectID string) (int, error) {
	// Verify project belongs to org
	var dbName, status string
	err := s.platformDB.QueryRow(ctx, `
		SELECT db_name, status FROM platform.projects
		WHERE id = $1 AND org_id = $2
	`, projectID, orgID).Scan(&dbName, &status)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("project not found")
	}
	if status == "deleted" {
		return http.StatusGone, fmt.Errorf("project already deleted")
	}

	// Set to deleting
	if _, err := s.platformDB.Exec(ctx, `UPDATE platform.projects SET status = 'deleting' WHERE id = $1`, projectID); err != nil {
		slog.Error("failed to update project status to deleting", "error", err, "project_id", projectID)
	}

	// Close the pool for this project
	s.poolManager.ClosePool(projectID)

	// Terminate all connections to the database
	if _, err := s.platformDB.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = $1 AND pid != pg_backend_pid()
	`, dbName); err != nil {
		slog.Error("failed to terminate connections", "error", err, "project_id", projectID, "db", dbName)
	}

	// Drop the database
	_, err = s.platformDB.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbName))
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("drop database: %w", err)
	}
	if err := s.poolManager.DropProjectLogin(ctx, projectID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("drop project login: %w", err)
	}

	// Mark as deleted
	if _, err := s.platformDB.Exec(ctx, `UPDATE platform.projects SET status = 'deleted', updated_at = NOW() WHERE id = $1`, projectID); err != nil {
		slog.Error("failed to update project status to deleted", "error", err, "project_id", projectID)
	}

	return http.StatusOK, nil
}

type UpdateSettingsRequest struct {
	EnableSignup      *bool `json:"enable_signup,omitempty"`
	Autoconfirm       *bool `json:"autoconfirm,omitempty"`
	PasswordMinLength *int  `json:"password_min_length,omitempty"`
}

// UpdateProjectSettings updates settings for a project.
// orgID is used to verify the project belongs to the given organization.
func (s *ProjectService) UpdateProjectSettings(ctx context.Context, orgID, projectID string, req UpdateSettingsRequest) (*ProjectSettings, int, error) {
	// Verify project belongs to org
	var currentSignup, currentAutoconfirm bool
	var currentMinLen int
	err := s.platformDB.QueryRow(ctx, `
		SELECT enable_signup, autoconfirm, password_min_length
		FROM platform.projects
		WHERE id = $1 AND org_id = $2 AND status != 'deleted'
	`, projectID, orgID).Scan(&currentSignup, &currentAutoconfirm, &currentMinLen)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	if req.EnableSignup != nil {
		currentSignup = *req.EnableSignup
	}
	if req.Autoconfirm != nil {
		currentAutoconfirm = *req.Autoconfirm
	}
	if req.PasswordMinLength != nil {
		if *req.PasswordMinLength < 6 {
			return nil, http.StatusBadRequest, fmt.Errorf("password_min_length must be at least 6")
		}
		currentMinLen = *req.PasswordMinLength
	}

	_, err = s.platformDB.Exec(ctx, `
		UPDATE platform.projects
		SET enable_signup = $1, autoconfirm = $2, password_min_length = $3, updated_at = NOW()
		WHERE id = $4
	`, currentSignup, currentAutoconfirm, currentMinLen, projectID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("update settings: %w", err)
	}

	// Invalidate pool cache so settings are picked up
	s.poolManager.InvalidateProjectCache(projectID)

	return &ProjectSettings{
		EnableSignup:      currentSignup,
		Autoconfirm:       currentAutoconfirm,
		PasswordMinLength: currentMinLen,
	}, http.StatusOK, nil
}

// RotateAPIKeys generates new anon_key and service_role_key for a project.
// orgID is used to verify the project belongs to the given organization.
func (s *ProjectService) RotateAPIKeys(ctx context.Context, orgID, projectID string) (*ProjectResponse, int, error) {
	// Verify project belongs to org and get project data
	var dbName, jwtSecret, name, siteURL, status string
	var enableSignup, autoconfirm bool
	var passwordMinLen int
	var createdAt time.Time
	err := s.platformDB.QueryRow(ctx, `
		SELECT db_name, jwt_secret, name, site_url, status, enable_signup, autoconfirm, password_min_length, created_at
		FROM platform.projects
		WHERE id = $1 AND org_id = $2 AND status = 'active'
	`, projectID, orgID).Scan(&dbName, &jwtSecret, &name, &siteURL, &status, &enableSignup, &autoconfirm, &passwordMinLen, &createdAt)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	// Generate new JWT secret
	newSecretBytes := make([]byte, 64)
	if _, err := rand.Read(newSecretBytes); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate jwt secret: %w", err)
	}
	jwtSecret = hex.EncodeToString(newSecretBytes)

	// Generate new keys with the new secret
	anonKey, err := generateProjectAPIKey(jwtSecret, projectID, "anon", s.apiKeyExpiryDays)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate anon key: %w", err)
	}
	serviceRoleKey, err := generateProjectAPIKey(jwtSecret, projectID, "service_role", s.apiKeyExpiryDays)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate service role key: %w", err)
	}

	// Update project with new secret and keys
	_, err = s.platformDB.Exec(ctx, `
		UPDATE platform.projects
		SET jwt_secret = $1, anon_key = $2, service_role_key = $3, updated_at = NOW()
		WHERE id = $4
	`, jwtSecret, anonKey, serviceRoleKey, projectID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("update keys: %w", err)
	}

	// Invalidate cache
	s.poolManager.InvalidateProjectCache(projectID)

	return &ProjectResponse{
		ID:             projectID,
		Name:           name,
		DBName:         dbName,
		Region:         "local",
		AnonKey:        anonKey,
		ServiceRoleKey: serviceRoleKey,
		JWTSecret:      jwtSecret,
		Status:         status,
		APIURL:         s.siteURL,
		SiteURL:        siteURL,
		Settings: ProjectSettings{
			EnableSignup:      enableSignup,
			Autoconfirm:       autoconfirm,
			PasswordMinLength: passwordMinLen,
		},
		CreatedAt: createdAt,
	}, http.StatusOK, nil
}

// generateProjectAPIKey creates a long-lived JWT for a project (anon or service_role).
func generateProjectAPIKey(jwtSecret, projectID, role string, expiryDays int) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"role":       role,
		"iss":        "supabase",
		"project_id": projectID,
		"iat":        now.Unix(),
		"exp":        now.Add(time.Duration(expiryDays) * 24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(jwtSecret))
}

// projectMigrations returns the SQL migrations to run on each new project database.
func projectMigrations() []database.Migration {
	return []database.Migration{
		{
			Name: "001_auth_schema.sql",
			SQL:  projectAuthMigrationSQL,
		},
	}
}

// This is embedded directly so we don't need file I/O during project creation.
const projectAuthMigrationSQL = `
CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE IF NOT EXISTS auth.users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  instance_id UUID,
  aud VARCHAR(255) DEFAULT 'authenticated',
  role VARCHAR(255) DEFAULT 'authenticated',
  email VARCHAR(255) UNIQUE,
  encrypted_password VARCHAR(255),
  email_confirmed_at TIMESTAMPTZ,
  invited_at TIMESTAMPTZ,
  confirmation_token VARCHAR(255) DEFAULT '',
  confirmation_sent_at TIMESTAMPTZ,
  recovery_token VARCHAR(255) DEFAULT '',
  recovery_sent_at TIMESTAMPTZ,
  email_change_token_new VARCHAR(255) DEFAULT '',
  email_change VARCHAR(255) DEFAULT '',
  email_change_sent_at TIMESTAMPTZ,
  last_sign_in_at TIMESTAMPTZ,
  raw_app_meta_data JSONB DEFAULT '{"provider":"email","providers":["email"]}'::jsonb,
  raw_user_meta_data JSONB DEFAULT '{}'::jsonb,
  is_super_admin BOOLEAN,
  phone TEXT UNIQUE DEFAULT NULL,
  phone_confirmed_at TIMESTAMPTZ,
  phone_change TEXT DEFAULT '',
  phone_change_token VARCHAR(255) DEFAULT '',
  phone_change_sent_at TIMESTAMPTZ,
  confirmed_at TIMESTAMPTZ GENERATED ALWAYS AS (LEAST(email_confirmed_at, phone_confirmed_at)) STORED,
  email_change_token_current VARCHAR(255) DEFAULT '',
  email_change_confirm_status SMALLINT DEFAULT 0,
  banned_until TIMESTAMPTZ,
  reauthentication_token VARCHAR(255) DEFAULT '',
  reauthentication_sent_at TIMESTAMPTZ,
  is_sso_user BOOLEAN NOT NULL DEFAULT FALSE,
  deleted_at TIMESTAMPTZ,
  is_anonymous BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_auth_users_email ON auth.users(email);
CREATE INDEX IF NOT EXISTS idx_auth_users_instance_id ON auth.users(instance_id);

CREATE TABLE IF NOT EXISTS auth.sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),
  factor_id UUID,
  aal VARCHAR(255) DEFAULT 'aal1',
  not_after TIMESTAMPTZ,
  refreshed_at TIMESTAMPTZ,
  user_agent TEXT,
  ip TEXT,
  tag TEXT
);

CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth.sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_not_after ON auth.sessions(not_after);

CREATE TABLE IF NOT EXISTS auth.refresh_tokens (
  id BIGSERIAL PRIMARY KEY,
  token VARCHAR(255) UNIQUE NOT NULL,
  user_id UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  parent VARCHAR(255),
  session_id UUID REFERENCES auth.sessions(id) ON DELETE CASCADE,
  revoked BOOLEAN DEFAULT FALSE,
  expires_at TIMESTAMPTZ DEFAULT (NOW() + INTERVAL '7 days'),
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_auth_refresh_tokens_token ON auth.refresh_tokens(token);
CREATE INDEX IF NOT EXISTS idx_auth_refresh_tokens_session ON auth.refresh_tokens(session_id);
CREATE INDEX IF NOT EXISTS idx_auth_refresh_tokens_parent ON auth.refresh_tokens(parent);

CREATE TABLE IF NOT EXISTS auth.identities (
  provider_id TEXT NOT NULL,
  user_id UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  identity_data JSONB NOT NULL DEFAULT '{}'::jsonb,
  provider TEXT NOT NULL,
  last_sign_in_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW(),
  id UUID PRIMARY KEY DEFAULT gen_random_uuid()
);

CREATE INDEX IF NOT EXISTS idx_auth_identities_user ON auth.identities(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_identities_provider ON auth.identities(provider_id, provider);

CREATE TABLE IF NOT EXISTS auth.flow_state (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID REFERENCES auth.users(id) ON DELETE CASCADE,
  auth_code VARCHAR(255) NOT NULL,
  code_challenge VARCHAR(255) NOT NULL,
  code_challenge_method VARCHAR(255) NOT NULL DEFAULT 's256',
  provider_type VARCHAR(255) NOT NULL DEFAULT 'email',
  provider_access_token TEXT,
  provider_refresh_token TEXT,
  authentication_method VARCHAR(255) NOT NULL DEFAULT 'email/password',
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'anon') THEN
    CREATE ROLE anon NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'authenticated') THEN
    CREATE ROLE authenticated NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'service_role') THEN
    CREATE ROLE service_role NOLOGIN BYPASSRLS;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO anon, authenticated, service_role;
-- WARNING: This grants full CRUD on all public tables to the anon role.
-- Row Level Security (RLS) policies MUST be defined on user tables to
-- restrict access. Without RLS, anonymous requests can read/write all data.
-- Consider using: ALTER TABLE tablename ENABLE ROW LEVEL SECURITY;
GRANT ALL ON ALL TABLES IN SCHEMA public TO anon, authenticated, service_role;
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO anon, authenticated, service_role;
GRANT ALL ON ALL ROUTINES IN SCHEMA public TO anon, authenticated, service_role;

ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO anon, authenticated, service_role;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO anon, authenticated, service_role;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON ROUTINES TO anon, authenticated, service_role;

GRANT USAGE ON SCHEMA auth TO authenticated, service_role;
GRANT ALL ON ALL TABLES IN SCHEMA auth TO service_role;
GRANT ALL ON ALL SEQUENCES IN SCHEMA auth TO service_role;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA auth TO authenticated;

CREATE OR REPLACE FUNCTION auth.uid() RETURNS UUID AS $$
  SELECT NULLIF(current_setting('request.jwt.claim.sub', true), '')::UUID;
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION auth.role() RETURNS TEXT AS $$
  SELECT NULLIF(current_setting('request.jwt.claim.role', true), '');
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION auth.jwt() RETURNS JSONB AS $$
  SELECT NULLIF(current_setting('request.jwt.claims', true), '')::JSONB;
$$ LANGUAGE sql STABLE;

-- Enable RLS on auth tables to prevent cross-user data access
ALTER TABLE auth.users ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth.sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth.refresh_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth.identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth.flow_state ENABLE ROW LEVEL SECURITY;

-- auth.users: authenticated users can only read their own row
CREATE POLICY users_self_read ON auth.users FOR SELECT
  TO authenticated USING (id = auth.uid());
CREATE POLICY users_self_update ON auth.users FOR UPDATE
  TO authenticated USING (id = auth.uid());

-- auth.sessions: users can only see their own sessions
CREATE POLICY sessions_self_read ON auth.sessions FOR SELECT
  TO authenticated USING (user_id = auth.uid());
CREATE POLICY sessions_self_delete ON auth.sessions FOR DELETE
  TO authenticated USING (user_id = auth.uid());

-- auth.refresh_tokens: users can only see their own tokens
CREATE POLICY refresh_tokens_self_read ON auth.refresh_tokens FOR SELECT
  TO authenticated USING (user_id = auth.uid());

-- auth.identities: users can only see their own identities
CREATE POLICY identities_self_read ON auth.identities FOR SELECT
  TO authenticated USING (user_id = auth.uid());

-- auth.flow_state: users can only see their own flow states
CREATE POLICY flow_state_self_read ON auth.flow_state FOR SELECT
  TO authenticated USING (user_id = auth.uid());

-- service_role bypasses RLS (BYPASSRLS is set on the role)
-- anon has no access to auth tables (no GRANT, no policies)
`
