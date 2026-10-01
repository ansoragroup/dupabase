package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/api/auth"
	"github.com/ansoraGROUP/dupabase/internal/config"
	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/ansoraGROUP/dupabase/internal/platform"
	"github.com/ansoraGROUP/dupabase/internal/server"
)

func main() {
	if os.Getenv("LOG_FORMAT") == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx := context.Background()

	// Connect to platform database
	slog.Info("Connecting to platform database")
	platformPool, err := database.NewPlatformPool(ctx, cfg.DatabaseURL, int32(cfg.PlatformMaxConns), int32(cfg.PlatformMinConns))
	if err != nil {
		log.Fatalf("Failed to connect to platform database: %v", err)
	}
	slog.Info("Connected to platform database")

	// Run platform migrations
	slog.Info("Running platform migrations")
	migCtx, migCancel := context.WithTimeout(ctx, 30*time.Second)
	defer migCancel()
	err = database.RunMigrations(migCtx, platformPool, platformMigrations())
	if err != nil {
		log.Fatalf("Failed to run platform migrations: %v", err)
	}
	slog.Info("Platform migrations complete")

	// Initialize pool manager
	poolManager, err := database.NewPoolManager(cfg, platformPool)
	if err != nil {
		log.Fatalf("Failed to create pool manager: %v", err)
	}

	// Initialize services
	authService := platform.NewAuthService(platformPool, cfg.PlatformJWTSecret, cfg.PlatformJWTExpiry)
	authService.StartCleanup()
	projectService := platform.NewProjectService(platformPool, poolManager, cfg.SiteURL, cfg.APIKeyExpiryDays)
	credService := platform.NewCredentialService(platformPool, cfg.DatabaseURL)
	auditService := platform.NewAuditService(platformPool, cfg.TrustProxy)
	adminService := platform.NewAdminService(platformPool, poolManager)

	// Backup encryption key: prefer dedicated env var, fall back to JWT secret
	backupKey := cfg.BackupEncryptionKey
	if backupKey == "" {
		backupKey = cfg.PlatformJWTSecret
		slog.Warn("BACKUP_ENCRYPTION_KEY not set, falling back to PLATFORM_JWT_SECRET — set a dedicated key for production")
	}
	backupService := platform.NewBackupService(platformPool, cfg.DatabaseURL, backupKey)
	importService := platform.NewImportService(platformPool, cfg.DatabaseURL)
	importService.SetPoolManager(poolManager)
	backupService.SetProjectServices(poolManager, importService)
	orgService := platform.NewOrgService(platformPool)
	analyticsService := platform.NewAnalyticsService(platformPool, poolManager)
	tableService := platform.NewTableService(platformPool, poolManager)
	sqlService := platform.NewSQLService(platformPool, poolManager)
	authUserService := platform.NewAuthUserService(platformPool, poolManager)

	// Ensure admin user exists if ADMIN_EMAIL is set
	if cfg.AdminEmail != "" {
		slog.Info("Ensuring admin user exists", "email", cfg.AdminEmail)
		if err := authService.EnsureAdmin(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
			log.Fatalf("Failed to create admin user: %v", err)
		}
		slog.Info("Admin user ready", "email", cfg.AdminEmail)
	}

	// Start backup scheduler
	backupScheduler := platform.NewBackupScheduler(backupService)
	backupScheduler.Start()

	// Create server
	srv := server.New(authService, projectService, credService, auditService, backupService, importService, adminService, orgService, analyticsService, tableService, sqlService, authUserService, poolManager, platformPool, cfg.ImportMaxSizeMB, cfg.ImportTempDir, cfg.TrustProxy)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:      srv.Handler(),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		slog.Info("Shutting down")
		backupScheduler.Stop()
		auth.StopGoTrueCleanup()
		authService.StopCleanup()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutCtx); err != nil {
			slog.Error("HTTP server shutdown error", "error", err)
		}
		srv.Stop()
		poolManager.Shutdown()
		platformPool.Close()
	}()

	slog.Info("Server started", "host", cfg.Host, "port", cfg.Port)

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func platformMigrations() []database.Migration {
	return []database.Migration{
		{
			Name: "001_initial.sql",
			SQL: `
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  display_name TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_users_email ON platform.users(email);

CREATE TABLE IF NOT EXISTS platform.pg_users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL UNIQUE REFERENCES platform.users(id) ON DELETE CASCADE,
  pg_username TEXT NOT NULL UNIQUE,
  pg_password_hash TEXT NOT NULL,
  pg_password_encrypted TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS platform.projects (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
  pg_user_id UUID NOT NULL REFERENCES platform.pg_users(id),
  name TEXT NOT NULL,
  db_name TEXT NOT NULL UNIQUE,
  region TEXT NOT NULL DEFAULT 'local',
  jwt_secret TEXT NOT NULL,
  anon_key TEXT NOT NULL,
  service_role_key TEXT NOT NULL,
  enable_signup BOOLEAN NOT NULL DEFAULT TRUE,
  autoconfirm BOOLEAN NOT NULL DEFAULT TRUE,
  password_min_length INTEGER NOT NULL DEFAULT 6,
  site_url TEXT NOT NULL DEFAULT 'http://localhost:3000',
  status TEXT NOT NULL DEFAULT 'creating'
    CHECK (status IN ('creating', 'active', 'paused', 'deleting', 'deleted')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_platform_projects_user ON platform.projects(user_id);
CREATE INDEX IF NOT EXISTS idx_platform_projects_db ON platform.projects(db_name);
CREATE INDEX IF NOT EXISTS idx_platform_projects_status ON platform.projects(status);

CREATE TABLE IF NOT EXISTS platform.audit_log (
  id BIGSERIAL PRIMARY KEY,
  user_id UUID REFERENCES platform.users(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  resource_type TEXT,
  resource_id TEXT,
  ip_address TEXT,
  user_agent TEXT,
  metadata JSONB DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_audit_log_user ON platform.audit_log(user_id);
CREATE INDEX IF NOT EXISTS idx_platform_audit_log_action ON platform.audit_log(action);
CREATE INDEX IF NOT EXISTS idx_platform_audit_log_created ON platform.audit_log(created_at);

CREATE TABLE IF NOT EXISTS platform.backup_settings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL UNIQUE REFERENCES platform.users(id) ON DELETE CASCADE,
  s3_endpoint TEXT NOT NULL,
  s3_region TEXT NOT NULL DEFAULT 'us-east-1',
  s3_bucket TEXT NOT NULL,
  s3_access_key_encrypted TEXT NOT NULL,
  s3_secret_key_encrypted TEXT NOT NULL,
  s3_path_prefix TEXT NOT NULL DEFAULT '',
  schedule TEXT NOT NULL DEFAULT 'daily',
  retention_days INTEGER NOT NULL DEFAULT 30,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS platform.backup_history (
  id BIGSERIAL PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
  project_id UUID NOT NULL REFERENCES platform.projects(id) ON DELETE CASCADE,
  db_name TEXT NOT NULL,
  s3_key TEXT NOT NULL,
  size_bytes BIGINT,
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
  error_message TEXT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_backup_history_user ON platform.backup_history(user_id);
CREATE INDEX IF NOT EXISTS idx_backup_history_status ON platform.backup_history(status);
`,
		},
		{Name: "002_backup_tables.sql", SQL: "-- no-op: tables already created in 001_initial.sql"},
		{
			Name: "003_import_tasks.sql",
			SQL: `
CREATE TABLE IF NOT EXISTS platform.import_tasks (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES platform.projects(id) ON DELETE CASCADE,
    db_name TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_size BIGINT NOT NULL DEFAULT 0,
    format TEXT NOT NULL DEFAULT 'auto',
    options JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'uploading'
        CHECK (status IN ('uploading','running','completed','failed','cancelled')),
    error_message TEXT,
    tables_imported INT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_import_tasks_project ON platform.import_tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_import_tasks_user ON platform.import_tasks(user_id);
`,
		},
		{
			Name: "004_backup_project_ids.sql",
			SQL: `
ALTER TABLE platform.backup_settings ADD COLUMN IF NOT EXISTS project_ids TEXT[] NOT NULL DEFAULT '{}';
`,
		},
		{
			Name: "005_admin_and_invites.sql",
			SQL: `
ALTER TABLE platform.users ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS platform.settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO platform.settings (key, value) VALUES ('registration_mode', 'open') ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS platform.invites (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  code TEXT NOT NULL UNIQUE,
  email TEXT,
  created_by UUID NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
  used_by UUID REFERENCES platform.users(id) ON DELETE SET NULL,
  used_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_invites_code ON platform.invites(code);
CREATE INDEX IF NOT EXISTS idx_invites_created_by ON platform.invites(created_by);
`,
		},
		{Name: "006_token_version.sql", SQL: `ALTER TABLE platform.users ADD COLUMN IF NOT EXISTS token_version INT NOT NULL DEFAULT 0;`},
		{
			Name: "007_organizations.sql",
			SQL: `
CREATE TABLE IF NOT EXISTS platform.organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    created_by UUID NOT NULL REFERENCES platform.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS platform.org_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES platform.organizations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, user_id)
);

CREATE TABLE IF NOT EXISTS platform.org_invites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES platform.organizations(id) ON DELETE CASCADE,
    email TEXT,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    invited_by UUID NOT NULL REFERENCES platform.users(id),
    token TEXT NOT NULL UNIQUE,
    accepted_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE platform.projects ADD COLUMN IF NOT EXISTS org_id UUID REFERENCES platform.organizations(id);

CREATE INDEX IF NOT EXISTS idx_org_members_user_id ON platform.org_members(user_id);
CREATE INDEX IF NOT EXISTS idx_org_members_org_id ON platform.org_members(org_id);
CREATE INDEX IF NOT EXISTS idx_org_invites_token ON platform.org_invites(token);
CREATE INDEX IF NOT EXISTS idx_org_invites_org_id ON platform.org_invites(org_id);
CREATE INDEX IF NOT EXISTS idx_projects_org_id ON platform.projects(org_id);
`,
		},
		{
			Name: "008_org_backfill.sql",
			SQL: `
-- Create personal org for every user that doesn't have one
INSERT INTO platform.organizations (name, slug, created_by)
SELECT 'Personal', 'personal-' || u.id::text, u.id
FROM platform.users u
WHERE NOT EXISTS (
    SELECT 1 FROM platform.org_members om WHERE om.user_id = u.id
);

-- Add each user as owner of their personal org
INSERT INTO platform.org_members (org_id, user_id, role)
SELECT o.id, o.created_by, 'owner'
FROM platform.organizations o
WHERE NOT EXISTS (
    SELECT 1 FROM platform.org_members m WHERE m.org_id = o.id AND m.user_id = o.created_by
);

-- Migrate orphan projects to their creator's personal org
UPDATE platform.projects p
SET org_id = (
    SELECT o.id FROM platform.organizations o WHERE o.created_by = p.user_id LIMIT 1
)
WHERE p.org_id IS NULL;

-- Make org_id NOT NULL (only after backfilling)
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM platform.projects WHERE org_id IS NULL
  ) THEN
    ALTER TABLE platform.projects ALTER COLUMN org_id SET NOT NULL;
  END IF;
END $$;
`,
		},
		{
			Name: "009_backup_org_scoping.sql",
			SQL: `
-- Add org_id to backup_settings for org-scoped backups
ALTER TABLE platform.backup_settings ADD COLUMN IF NOT EXISTS org_id UUID REFERENCES platform.organizations(id);

-- Backfill org_id from user's personal org
UPDATE platform.backup_settings bs SET org_id = (
    SELECT o.id FROM platform.organizations o WHERE o.slug = 'personal-' || bs.user_id::text LIMIT 1
) WHERE bs.org_id IS NULL;

-- Fix backup_history status CHECK to include 'cancelled'
ALTER TABLE platform.backup_history DROP CONSTRAINT IF EXISTS backup_history_status_check;
ALTER TABLE platform.backup_history ADD CONSTRAINT backup_history_status_check
    CHECK (status IN ('running', 'completed', 'failed', 'cancelled'));

-- Add indexes for org-scoped queries
CREATE INDEX IF NOT EXISTS idx_backup_settings_org_id ON platform.backup_settings(org_id);
CREATE INDEX IF NOT EXISTS idx_backup_history_project ON platform.backup_history(project_id);
`,
		},
		platform.BackupMaintenanceMigration,
	}
}
