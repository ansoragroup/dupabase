package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/cryptocompat/bcrypt"
	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupService handles S3 backup operations for project databases.
type BackupService struct {
	db            *pgxpool.Pool
	databaseURL   string
	backupKey     string // server-level encryption key for S3 credentials
	poolManager   *database.PoolManager
	importService *ImportService
}

func (s *BackupService) SetProjectServices(pm *database.PoolManager, imports *ImportService) {
	s.poolManager, s.importService = pm, imports
}

func (s *BackupService) projectDBURL(ctx context.Context, dbName string) (string, error) {
	if s.poolManager == nil {
		return "", fmt.Errorf("project credential provider is not configured")
	}
	return s.poolManager.ProjectDatabaseURL(ctx, dbName)
}

// NewBackupService creates a new BackupService.
func NewBackupService(db *pgxpool.Pool, databaseURL string, backupKey string) *BackupService {
	return &BackupService{
		db:          db,
		databaseURL: databaseURL,
		backupKey:   backupKey,
	}
}

// --- Request/Response types ---

// SaveBackupSettingsRequest is the request body for saving S3 backup settings.
type SaveBackupSettingsRequest struct {
	S3Endpoint       string   `json:"s3_endpoint"`
	S3Region         string   `json:"s3_region"`
	S3Bucket         string   `json:"s3_bucket"`
	S3AccessKey      string   `json:"s3_access_key"`
	S3SecretKey      string   `json:"s3_secret_key"`
	S3PathPrefix     string   `json:"s3_path_prefix,omitempty"`
	Schedule         string   `json:"schedule,omitempty"` // "daily", "weekly", "hourly"
	RetentionDays    int      `json:"retention_days,omitempty"`
	ProjectIDs       []string `json:"project_ids,omitempty"` // empty = all projects
	PlatformPassword string   `json:"platform_password"`
	OrgID            string   `json:"org_id,omitempty"`
}

// BackupSettingsResponse is the public-facing backup settings (no decrypted keys).
type BackupSettingsResponse struct {
	ID            string   `json:"id"`
	S3Endpoint    string   `json:"s3_endpoint"`
	S3Region      string   `json:"s3_region"`
	S3Bucket      string   `json:"s3_bucket"`
	S3PathPrefix  string   `json:"s3_path_prefix"`
	Schedule      string   `json:"schedule"`
	RetentionDays int      `json:"retention_days"`
	ProjectIDs    []string `json:"project_ids"`
	Enabled       bool     `json:"enabled"`
}

// BackupHistoryResponse is a single backup history record.
type BackupHistoryResponse struct {
	ID          int64      `json:"id"`
	ProjectID   string     `json:"project_id"`
	DBName      string     `json:"db_name"`
	S3Key       string     `json:"s3_key"`
	SizeBytes   *int64     `json:"size_bytes"`
	Status      string     `json:"status"`
	Error       *string    `json:"error_message"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

// backupSettingsInternal holds the full row including encrypted credentials.
type backupSettingsInternal struct {
	ID                   string
	UserID               string
	OrgID                string
	S3Endpoint           string
	S3Region             string
	S3Bucket             string
	S3AccessKeyEncrypted string
	S3SecretKeyEncrypted string
	S3PathPrefix         string
	Schedule             string
	RetentionDays        int
	ProjectIDs           []string
	Enabled              bool
}

// --- Public methods ---

// SaveSettings validates the user's platform password, encrypts S3 keys with the
// server-level backup encryption key, and upserts backup settings.
// orgID determines which org the settings belong to.
func (s *BackupService) SaveSettings(ctx context.Context, userID, orgID string, req SaveBackupSettingsRequest) (*BackupSettingsResponse, int, error) {
	if req.PlatformPassword == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("platform_password is required")
	}
	if req.S3Endpoint == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("s3_endpoint is required")
	}
	if _, err := endpointOrigin(req.S3Endpoint); err != nil {
		return nil, http.StatusBadRequest, err
	}
	if req.S3Bucket == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("s3_bucket is required")
	}
	if req.S3AccessKey == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("s3_access_key is required")
	}
	if req.S3SecretKey == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("s3_secret_key is required")
	}

	// Verify the user's platform password via bcrypt
	var passwordHash string
	err := s.db.QueryRow(ctx, `SELECT password_hash FROM platform.users WHERE id = $1`, userID).Scan(&passwordHash)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.PlatformPassword)); err != nil {
		return nil, http.StatusUnauthorized, fmt.Errorf("invalid password")
	}

	// Defaults
	region := req.S3Region
	if region == "" {
		region = "us-east-1"
	}

	schedule := req.Schedule
	if schedule == "" {
		schedule = "daily"
	}
	if schedule != "hourly" && schedule != "daily" && schedule != "weekly" {
		return nil, http.StatusBadRequest, fmt.Errorf("schedule must be one of: hourly, daily, weekly")
	}
	retentionDays := req.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 30
	}

	// Encrypt S3 keys with the server-level backup encryption key
	accessKeyEnc, err := EncryptPgPassword(req.S3AccessKey, s.backupKey)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("encrypt access key: %w", err)
	}
	secretKeyEnc, err := EncryptPgPassword(req.S3SecretKey, s.backupKey)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("encrypt secret key: %w", err)
	}

	// Normalize project_ids (nil → empty)
	projectIDs := req.ProjectIDs
	if projectIDs == nil {
		projectIDs = []string{}
	}

	// Lock the organization, rather than a user's row: one administrator can
	// manage several organizations without overwriting their other settings.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("begin settings save: %w", err)
	}
	defer tx.Rollback(ctx)
	var lockedOrg string
	if err = tx.QueryRow(ctx, `SELECT id FROM platform.organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&lockedOrg); err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("organization not found")
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM platform.backup_settings WHERE org_id=$1 ORDER BY updated_at DESC, id DESC LIMIT 1`, orgID).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, http.StatusInternalServerError, fmt.Errorf("find settings: %w", err)
	}
	query := `
		INSERT INTO platform.backup_settings (
			user_id, org_id, s3_endpoint, s3_region, s3_bucket,
			s3_access_key_encrypted, s3_secret_key_encrypted,
			s3_path_prefix, schedule, retention_days, project_ids, enabled
		) VALUES ($1, $11, $2, $3, $4, $5, $6, $7, $8, $9, $10, TRUE)
		RETURNING id
	`
	args := []any{userID, req.S3Endpoint, region, req.S3Bucket,
		accessKeyEnc, secretKeyEnc,
		req.S3PathPrefix, schedule, retentionDays, projectIDs, orgID}
	if err == nil {
		query = `UPDATE platform.backup_settings SET user_id=$1, s3_endpoint=$2, s3_region=$3, s3_bucket=$4,
			s3_access_key_encrypted=$5, s3_secret_key_encrypted=$6, s3_path_prefix=$7, schedule=$8,
			retention_days=$9, project_ids=$10, org_id=$11, enabled=TRUE, updated_at=NOW() WHERE id=$12 RETURNING id`
		args = append(args, id)
	}
	err = tx.QueryRow(ctx, query, args...).Scan(&id)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("upsert backup settings: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("commit settings save: %w", err)
	}

	return &BackupSettingsResponse{
		ID:            id,
		S3Endpoint:    req.S3Endpoint,
		S3Region:      region,
		S3Bucket:      req.S3Bucket,
		S3PathPrefix:  req.S3PathPrefix,
		Schedule:      schedule,
		RetentionDays: retentionDays,
		ProjectIDs:    projectIDs,
		Enabled:       true,
	}, http.StatusOK, nil
}

// GetSettings returns backup settings for an org (without decrypted keys).
func (s *BackupService) GetSettings(ctx context.Context, orgID string) (*BackupSettingsResponse, int, error) {
	var resp BackupSettingsResponse
	err := s.db.QueryRow(ctx, `
		SELECT id, s3_endpoint, s3_region, s3_bucket, s3_path_prefix,
			schedule, retention_days, project_ids, enabled
		FROM platform.backup_settings
		WHERE org_id = $1
		ORDER BY updated_at DESC, id DESC LIMIT 1
	`, orgID).Scan(
		&resp.ID, &resp.S3Endpoint, &resp.S3Region, &resp.S3Bucket,
		&resp.S3PathPrefix, &resp.Schedule, &resp.RetentionDays, &resp.ProjectIDs, &resp.Enabled,
	)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("backup settings not found")
	}
	if resp.ProjectIDs == nil {
		resp.ProjectIDs = []string{}
	}
	return &resp, http.StatusOK, nil
}

// GetHistory returns the backup history for an org (via projects).
func (s *BackupService) GetHistory(ctx context.Context, orgID string) ([]BackupHistoryResponse, int, error) {
	rows, err := s.db.Query(ctx, `
		SELECT bh.id, bh.project_id, bh.db_name, bh.s3_key, bh.size_bytes,
			bh.status, bh.error_message, bh.started_at, bh.completed_at
		FROM platform.backup_history bh
		JOIN platform.projects p ON p.id = bh.project_id
		WHERE p.org_id = $1
		ORDER BY bh.started_at DESC
		LIMIT 100
	`, orgID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query backup history: %w", err)
	}
	defer rows.Close()

	var history []BackupHistoryResponse
	for rows.Next() {
		var h BackupHistoryResponse
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.DBName, &h.S3Key,
			&h.SizeBytes, &h.Status, &h.Error, &h.StartedAt, &h.CompletedAt); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan backup history: %w", err)
		}
		history = append(history, h)
	}
	if history == nil {
		history = []BackupHistoryResponse{}
	}
	return history, http.StatusOK, nil
}

// RunBackupForOrg runs backups for all active projects of a specific org.
// This is called from the manual "run now" endpoint.
func (s *BackupService) RunBackupForOrg(ctx context.Context, userID, orgID string) (int, error) {
	settings, err := s.getSettingsInternalByOrg(ctx, orgID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("backup settings not found")
	}
	if !settings.Enabled {
		return http.StatusBadRequest, fmt.Errorf("backups are disabled")
	}

	projects, err := s.getActiveProjectsByOrg(ctx, orgID, settings.ProjectIDs)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("get projects: %w", err)
	}
	if len(projects) == 0 {
		return http.StatusOK, nil
	}

	for _, proj := range projects {
		s.runSingleBackup(ctx, userID, proj.id, proj.dbName, settings)
	}
	return http.StatusOK, nil
}

// RunBackupsForAllUsers is the cron entry point that runs backups for all enabled orgs.
func (s *BackupService) RunBackupsForAllUsers(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_id, COALESCE(org_id::text, ''), s3_endpoint, s3_region, s3_bucket,
			s3_access_key_encrypted, s3_secret_key_encrypted,
			s3_path_prefix, schedule, retention_days, project_ids, enabled
		FROM platform.backup_settings
		WHERE enabled = TRUE
		AND id IN (SELECT DISTINCT ON (COALESCE(org_id::text, 'user:' || user_id::text)) id
		  FROM platform.backup_settings ORDER BY COALESCE(org_id::text, 'user:' || user_id::text), updated_at DESC, id DESC)
	`)
	if err != nil {
		return fmt.Errorf("query backup settings: %w", err)
	}
	defer rows.Close()

	var allSettings []backupSettingsInternal
	for rows.Next() {
		var bs backupSettingsInternal
		if err := rows.Scan(&bs.ID, &bs.UserID, &bs.OrgID, &bs.S3Endpoint, &bs.S3Region,
			&bs.S3Bucket, &bs.S3AccessKeyEncrypted, &bs.S3SecretKeyEncrypted,
			&bs.S3PathPrefix, &bs.Schedule, &bs.RetentionDays, &bs.ProjectIDs, &bs.Enabled); err != nil {
			return fmt.Errorf("scan backup settings: %w", err)
		}
		if bs.ProjectIDs == nil {
			bs.ProjectIDs = []string{}
		}
		allSettings = append(allSettings, bs)
	}

	now := time.Now().UTC()
	for _, settings := range allSettings {
		var projects []projectInfo
		var err error
		if settings.OrgID != "" {
			projects, err = s.getActiveProjectsByOrg(ctx, settings.OrgID, settings.ProjectIDs)
		} else {
			// Backward compat: fall back to user-scoped lookup
			projects, err = s.getActiveProjects(ctx, settings.UserID, settings.ProjectIDs)
		}
		if err != nil {
			slog.Error("Backup: failed to get projects", "user_id", settings.UserID, "org_id", settings.OrgID, "error", err)
			continue
		}

		for _, proj := range projects {
			if !s.isDue(ctx, settings.Schedule, proj.id, now) {
				continue
			}
			s.runSingleBackup(ctx, settings.UserID, proj.id, proj.dbName, &settings)
		}
	}
	return nil
}

// --- Request types for new features ---

// TestS3ConnectionRequest is the request body for testing S3 connectivity.
type TestS3ConnectionRequest struct {
	S3Endpoint  string `json:"s3_endpoint"`
	S3Region    string `json:"s3_region"`
	S3Bucket    string `json:"s3_bucket"`
	S3AccessKey string `json:"s3_access_key"`
	S3SecretKey string `json:"s3_secret_key"`
	OrgID       string `json:"org_id,omitempty"`
}

// ToggleBackupRequest is the request body for enabling/disabling backups.
type ToggleBackupRequest struct {
	Enabled          bool   `json:"enabled"`
	PlatformPassword string `json:"platform_password"`
	OrgID            string `json:"org_id,omitempty"`
}

// RestoreBackupRequest is the request body for restoring a backup.
type RestoreBackupRequest struct {
	PlatformPassword string `json:"platform_password"`
}

// TestS3Connection creates a temp S3 client with the provided (unencrypted) credentials
// and runs HeadBucket to verify connectivity.
func (s *BackupService) TestS3Connection(ctx context.Context, req TestS3ConnectionRequest) (int, error) {
	if req.S3Endpoint == "" {
		return http.StatusBadRequest, fmt.Errorf("s3_endpoint is required")
	}
	if req.S3Bucket == "" {
		return http.StatusBadRequest, fmt.Errorf("s3_bucket is required")
	}
	if req.S3AccessKey == "" {
		return http.StatusBadRequest, fmt.Errorf("s3_access_key is required")
	}
	if req.S3SecretKey == "" {
		return http.StatusBadRequest, fmt.Errorf("s3_secret_key is required")
	}

	region := req.S3Region
	if region == "" {
		region = "us-east-1"
	}

	httpClient, err := storageHTTPClient(req.S3Endpoint)
	if err != nil {
		return http.StatusBadRequest, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(httpClient),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(req.S3AccessKey, req.S3SecretKey, "")),
	)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if req.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(req.S3Endpoint)
			o.UsePathStyle = true
		}
	})

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(req.S3Bucket),
	})
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("S3 connection failed: %w", err)
	}

	return http.StatusOK, nil
}

// ToggleEnabled enables or disables backups after password verification.
// orgID determines which org's backup settings to toggle.
func (s *BackupService) ToggleEnabled(ctx context.Context, userID, orgID string, req ToggleBackupRequest) (*BackupSettingsResponse, int, error) {
	if req.PlatformPassword == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("platform_password is required")
	}

	// Verify password
	var passwordHash string
	err := s.db.QueryRow(ctx, `SELECT password_hash FROM platform.users WHERE id = $1`, userID).Scan(&passwordHash)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.PlatformPassword)); err != nil {
		return nil, http.StatusUnauthorized, fmt.Errorf("invalid password")
	}

	// Update enabled flag scoped to org
	var resp BackupSettingsResponse
	err = s.db.QueryRow(ctx, `
		UPDATE platform.backup_settings
		SET enabled = $1, updated_at = NOW()
		WHERE id = (SELECT id FROM platform.backup_settings WHERE org_id=$2 ORDER BY updated_at DESC, id DESC LIMIT 1)
		RETURNING id, s3_endpoint, s3_region, s3_bucket, s3_path_prefix,
			schedule, retention_days, project_ids, enabled
	`, req.Enabled, orgID).Scan(
		&resp.ID, &resp.S3Endpoint, &resp.S3Region, &resp.S3Bucket,
		&resp.S3PathPrefix, &resp.Schedule, &resp.RetentionDays, &resp.ProjectIDs, &resp.Enabled,
	)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("backup settings not found")
	}
	if resp.ProjectIDs == nil {
		resp.ProjectIDs = []string{}
	}
	return &resp, http.StatusOK, nil
}

// ExportOptions controls pg_dump behavior for database exports.
type ExportOptions struct {
	Format        string   // custom, plain/sql
	SchemaOnly    bool     // --schema-only
	DataOnly      bool     // --data-only
	Tables        []string // --table=X
	ExcludeTables []string // --exclude-table=X
	ExcludeAuth   bool     // --exclude-schema=auth
	Inserts       bool     // --inserts
	Compress      int      // -Z 0-9 (plain format only)
}

// safeExportIdentifierRe validates table/schema names to prevent injection.
var safeExportIdentifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]{0,126}$`)

// isSafeExportIdentifier checks if a string is a safe SQL identifier for pg_dump args.
func isSafeExportIdentifier(s string) bool {
	return safeExportIdentifierRe.MatchString(s)
}

// validateExportOptions checks for invalid option combinations and SQL injection.
func validateExportOptions(opts ExportOptions) error {
	if opts.SchemaOnly && opts.DataOnly {
		return fmt.Errorf("schema_only and data_only are mutually exclusive")
	}
	for _, t := range opts.Tables {
		if !isSafeExportIdentifier(t) {
			return fmt.Errorf("invalid table name: %q", t)
		}
	}
	for _, t := range opts.ExcludeTables {
		if !isSafeExportIdentifier(t) {
			return fmt.Errorf("invalid exclude table name: %q", t)
		}
	}
	if opts.Compress < 0 || opts.Compress > 9 {
		return fmt.Errorf("compress must be between 0 and 9")
	}
	return nil
}

// buildExportArgs constructs pg_dump arguments from connection info and export options.
func buildExportArgs(host, port, user, dbName string, opts ExportOptions) []string {
	pgFormat := "custom"
	if opts.Format == "plain" || opts.Format == "sql" {
		pgFormat = "plain"
	}

	args := []string{
		"--format=" + pgFormat,
		"--exclude-schema=platform",
		"--no-owner",
		"--no-acl",
		"--host=" + host, "--port=" + port, "--username=" + user, "--dbname=" + dbName,
	}

	if opts.SchemaOnly {
		args = append(args, "--schema-only")
	}
	if opts.DataOnly {
		args = append(args, "--data-only")
	}
	for _, t := range opts.Tables {
		args = append(args, "--table="+t)
	}
	for _, t := range opts.ExcludeTables {
		args = append(args, "--exclude-table="+t)
	}
	if opts.ExcludeAuth {
		args = append(args, "--exclude-schema=auth")
	}
	if opts.Inserts {
		args = append(args, "--inserts")
	}
	if opts.Compress > 0 && pgFormat == "plain" {
		args = append(args, "-Z", strconv.Itoa(opts.Compress))
	}

	return args
}

// ExportDatabase runs pg_dump with the given options and returns a reader + filename.
func (s *BackupService) ExportDatabase(ctx context.Context, dbName string, opts ExportOptions) (io.ReadCloser, string, error) {
	if err := validateExportOptions(opts); err != nil {
		return nil, "", err
	}

	reader, err := s.dumpDatabase(ctx, dbName, opts)
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	ext := ".dump"
	if opts.Format == "plain" || opts.Format == "sql" {
		ext = ".sql"
	}
	filename := fmt.Sprintf("%s_%s%s", dbName, now.Format("2006-01-02T15-04-05Z"), ext)

	return reader, filename, nil
}

// GetProjectDBName returns the db_name for an active project.
func (s *BackupService) GetProjectDBName(ctx context.Context, projectID string) (string, error) {
	var dbName string
	err := s.db.QueryRow(ctx, `
		SELECT db_name FROM platform.projects WHERE id = $1 AND status = 'active'
	`, projectID).Scan(&dbName)
	if err != nil {
		return "", fmt.Errorf("project not found or not active")
	}
	return dbName, nil
}

// RestoreBackup downloads a backup from S3 and restores it to the project database.
// It returns the import task ID, HTTP status, and any error.
func (s *BackupService) RestoreBackup(ctx context.Context, userID string, historyID int64, pw string) (int64, int, error) {
	if pw == "" {
		return 0, http.StatusBadRequest, fmt.Errorf("platform_password is required")
	}

	// Verify password
	var passwordHash string
	err := s.db.QueryRow(ctx, `SELECT password_hash FROM platform.users WHERE id = $1`, userID).Scan(&passwordHash)
	if err != nil {
		return 0, http.StatusNotFound, fmt.Errorf("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(pw)); err != nil {
		return 0, http.StatusUnauthorized, fmt.Errorf("invalid password")
	}

	// Look up backup history record
	var projectID, dbName, s3Key, status, expectedHash, sourceEndpoint, sourceRegion, sourceBucket string
	err = s.db.QueryRow(ctx, `
		SELECT project_id, db_name, s3_key, status, COALESCE(content_sha256,''),
			COALESCE(source_endpoint,''), COALESCE(source_region,''), COALESCE(source_bucket,'')
		FROM platform.backup_history
		WHERE id = $1
	`, historyID).Scan(&projectID, &dbName, &s3Key, &status, &expectedHash, &sourceEndpoint, &sourceRegion, &sourceBucket)
	if err != nil {
		return 0, http.StatusNotFound, fmt.Errorf("backup history record not found")
	}
	if status != "completed" {
		return 0, http.StatusBadRequest, fmt.Errorf("can only restore completed backups (status: %s)", status)
	}

	// The invoking user's mutable personal settings are not backup provenance.
	var orgID string
	err = s.db.QueryRow(ctx, `SELECT org_id FROM platform.projects WHERE id = $1 AND db_name = $2 AND status = 'active'`, projectID, dbName).Scan(&orgID)
	if err != nil {
		return 0, http.StatusNotFound, fmt.Errorf("backup project is not active")
	}
	settings, err := s.getSettingsInternalByOrg(ctx, orgID)
	if err != nil {
		return 0, http.StatusNotFound, fmt.Errorf("backup settings not found for restore")
	}
	// New backups bind their original destination and content. Credentials still
	// come from the current organization's administrator-controlled settings.
	if sourceEndpoint != "" {
		settings.S3Endpoint, settings.S3Region, settings.S3Bucket = sourceEndpoint, sourceRegion, sourceBucket
	}

	// Build target database URL
	dbURL, err := s.projectDBURL(ctx, dbName)
	if err != nil {
		return 0, http.StatusInternalServerError, fmt.Errorf("parse database URL: %w", err)
	}
	if s.importService == nil {
		return 0, http.StatusInternalServerError, fmt.Errorf("restore worker is not configured")
	}
	release, err := s.importService.reserveJob(projectID)
	if err != nil {
		return 0, http.StatusTooManyRequests, err
	}

	// Create import task record for tracking
	var taskID int64
	err = s.db.QueryRow(ctx, `
		INSERT INTO platform.import_tasks (user_id, project_id, db_name, file_name, file_size, format, status)
		VALUES ($1, $2, $3, $4, 0, 'custom', 'running')
		RETURNING id
	`, userID, projectID, dbName, "restore:"+s3Key).Scan(&taskID)
	if err != nil {
		release()
		return 0, http.StatusInternalServerError, fmt.Errorf("create import task: %w", err)
	}

	// Launch restore in background
	go func() { defer release(); s.executeRestore(taskID, dbURL, s3Key, settings, expectedHash) }()

	return taskID, http.StatusAccepted, nil
}

// executeRestore downloads a backup from S3 to a temp file and runs pg_restore.
func (s *BackupService) executeRestore(taskID int64, dbURL, s3Key string, settings *backupSettingsInternal, expectedHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	s.importService.mu.Lock()
	s.importService.cancels[taskID] = cancel
	s.importService.mu.Unlock()
	defer func() {
		s.importService.mu.Lock()
		delete(s.importService.cancels, taskID)
		s.importService.mu.Unlock()
	}()
	var taskStatus string
	if err := s.db.QueryRow(ctx, `SELECT status FROM platform.import_tasks WHERE id=$1`, taskID).Scan(&taskStatus); err != nil || taskStatus != "running" {
		return
	}

	// Download from S3
	client, err := s.getS3Client(ctx, settings)
	if err != nil {
		s.markRestoreFailed(ctx, taskID, fmt.Sprintf("create S3 client: %v", err))
		return
	}

	output, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(settings.S3Bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		s.markRestoreFailed(ctx, taskID, fmt.Sprintf("S3 GetObject: %v", err))
		return
	}
	defer output.Body.Close()

	// Write to temp file
	tmpDir := os.Getenv("IMPORT_TEMP_DIR")
	if tmpDir == "" {
		tmpDir = "/tmp/imports"
	}
	tmpFile, err := os.CreateTemp(tmpDir, "restore-*.dump")
	if err != nil {
		s.markRestoreFailed(ctx, taskID, fmt.Sprintf("create temp file: %v", err))
		return
	}
	defer os.Remove(tmpFile.Name())

	maxBytes := int64(s.poolManager.Config().ImportMaxSizeMB) * 1024 * 1024
	if maxBytes <= 0 {
		maxBytes = 500 * 1024 * 1024
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, digest), io.LimitReader(output.Body, maxBytes+1))
	if err != nil || written > maxBytes {
		tmpFile.Close()
		s.markRestoreFailed(ctx, taskID, "backup exceeds the restore size limit or could not be downloaded")
		return
	}
	tmpFile.Close()
	if expectedHash != "" && hex.EncodeToString(digest.Sum(nil)) != expectedHash {
		s.markRestoreFailed(ctx, taskID, "backup content does not match its recorded SHA-256 digest")
		return
	}

	// Run pg_restore
	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		s.markRestoreFailed(ctx, taskID, fmt.Sprintf("parse DB URL: %v", err))
		return
	}

	plan, err := preparePGRestore(ctx, s.db, tmpFile.Name())
	if err != nil {
		s.markRestoreFailed(ctx, taskID, fmt.Sprintf("restore compatibility preflight: %v", err))
		return
	}
	cmd := exec.CommandContext(ctx, plan.tool,
		"--exit-on-error",
		"--single-transaction",
		"--exclude-schema=platform",
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		tmpFile.Name(),
	)
	cmd.Env = pgCommandEnv(dbURL)

	out, err := runBoundedCommand(cmd)
	if err != nil {
		outStr := string(out)
		s.markRestoreFailed(ctx, taskID, fmt.Sprintf("pg_restore: %s", outStr))
		return

	}

	tableCount := countRestoredTables(ctx, dbURL)

	if _, err := s.db.Exec(ctx, `
		UPDATE platform.import_tasks
		SET status = 'completed', tables_imported = $1, completed_at = NOW()
		WHERE id = $2 AND status = 'running'
	`, tableCount, taskID); err != nil {
		slog.Error("failed to mark restore as completed", "task_id", taskID, "error", err)
	}

	slog.Info("Restore completed", "task_id", taskID, "tables", tableCount)
}

func (s *BackupService) markRestoreFailed(ctx context.Context, taskID int64, errMsg string) {
	slog.Error("Restore failed", "task_id", taskID, "error", errMsg)
	updateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.db.Exec(updateCtx, `
		UPDATE platform.import_tasks
		SET status = 'failed', error_message = $1, completed_at = NOW()
		WHERE id = $2 AND status = 'running'
	`, errMsg, taskID); err != nil {
		slog.Error("failed to mark restore as failed", "task_id", taskID, "error", err)
	}
}

// --- Internal helpers ---

type projectInfo struct {
	id     string
	dbName string
}

func (s *BackupService) getSettingsInternal(ctx context.Context, userID string) (*backupSettingsInternal, error) {
	var bs backupSettingsInternal
	err := s.db.QueryRow(ctx, `
		SELECT id, user_id, COALESCE(org_id::text, ''), s3_endpoint, s3_region, s3_bucket,
			s3_access_key_encrypted, s3_secret_key_encrypted,
			s3_path_prefix, schedule, retention_days, project_ids, enabled
		FROM platform.backup_settings
		WHERE user_id = $1
	`, userID).Scan(&bs.ID, &bs.UserID, &bs.OrgID, &bs.S3Endpoint, &bs.S3Region,
		&bs.S3Bucket, &bs.S3AccessKeyEncrypted, &bs.S3SecretKeyEncrypted,
		&bs.S3PathPrefix, &bs.Schedule, &bs.RetentionDays, &bs.ProjectIDs, &bs.Enabled)
	if err != nil {
		return nil, err
	}
	if bs.ProjectIDs == nil {
		bs.ProjectIDs = []string{}
	}
	return &bs, nil
}

func (s *BackupService) getSettingsInternalByOrg(ctx context.Context, orgID string) (*backupSettingsInternal, error) {
	var bs backupSettingsInternal
	err := s.db.QueryRow(ctx, `
		SELECT id, user_id, COALESCE(org_id::text, ''), s3_endpoint, s3_region, s3_bucket,
			s3_access_key_encrypted, s3_secret_key_encrypted,
			s3_path_prefix, schedule, retention_days, project_ids, enabled
		FROM platform.backup_settings
		WHERE org_id = $1
		ORDER BY updated_at DESC, id DESC LIMIT 1
	`, orgID).Scan(&bs.ID, &bs.UserID, &bs.OrgID, &bs.S3Endpoint, &bs.S3Region,
		&bs.S3Bucket, &bs.S3AccessKeyEncrypted, &bs.S3SecretKeyEncrypted,
		&bs.S3PathPrefix, &bs.Schedule, &bs.RetentionDays, &bs.ProjectIDs, &bs.Enabled)
	if err != nil {
		return nil, err
	}
	if bs.ProjectIDs == nil {
		bs.ProjectIDs = []string{}
	}
	return &bs, nil
}

func (s *BackupService) getActiveProjects(ctx context.Context, userID string, filterIDs []string) ([]projectInfo, error) {
	var query string
	var args []interface{}

	if len(filterIDs) > 0 {
		query = `SELECT id, db_name FROM platform.projects
			WHERE user_id = $1 AND status = 'active' AND id::text = ANY($2)`
		args = []interface{}{userID, filterIDs}
	} else {
		query = `SELECT id, db_name FROM platform.projects
			WHERE user_id = $1 AND status = 'active'`
		args = []interface{}{userID}
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []projectInfo
	for rows.Next() {
		var p projectInfo
		if err := rows.Scan(&p.id, &p.dbName); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, nil
}

func (s *BackupService) getActiveProjectsByOrg(ctx context.Context, orgID string, filterIDs []string) ([]projectInfo, error) {
	var query string
	var args []interface{}

	if len(filterIDs) > 0 {
		query = `SELECT id, db_name FROM platform.projects
			WHERE org_id = $1 AND status = 'active' AND id::text = ANY($2)`
		args = []interface{}{orgID, filterIDs}
	} else {
		query = `SELECT id, db_name FROM platform.projects
			WHERE org_id = $1 AND status = 'active'`
		args = []interface{}{orgID}
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []projectInfo
	for rows.Next() {
		var p projectInfo
		if err := rows.Scan(&p.id, &p.dbName); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// isDue checks whether a backup should run for a specific project based on the schedule.
// It looks at the most recent completed backup for that project to decide.
func (s *BackupService) isDue(ctx context.Context, schedule, projectID string, now time.Time) bool {
	var lastCompleted time.Time
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(completed_at), '1970-01-01'::timestamptz)
		FROM platform.backup_history
		WHERE project_id = $1 AND status = 'completed'
	`, projectID).Scan(&lastCompleted)
	if err != nil {
		return true // if we can't tell, run anyway
	}

	switch schedule {
	case "hourly":
		return now.Sub(lastCompleted) >= 1*time.Hour
	case "daily":
		return now.Sub(lastCompleted) >= 24*time.Hour
	case "weekly":
		return now.Sub(lastCompleted) >= 7*24*time.Hour
	default:
		return now.Sub(lastCompleted) >= 24*time.Hour
	}
}

// runSingleBackup performs pg_dump and uploads the result to S3 for one project.
func (s *BackupService) runSingleBackup(ctx context.Context, userID, projectID, dbName string, settings *backupSettingsInternal) {
	if s.importService == nil {
		slog.Error("backup worker is not configured")
		return
	}
	release, err := s.importService.reserveJob(projectID)
	if err != nil {
		slog.Warn("backup workers are busy", "project_id", projectID)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	shortID := projectID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	s3Key := fmt.Sprintf("%s%s/%s_%s.dump",
		settings.S3PathPrefix,
		dbName,
		now.Format("2006-01-02T15-04-05.000000000Z"),
		shortID,
	)

	// Insert running record
	var historyID int64
	err = s.db.QueryRow(ctx, `
		INSERT INTO platform.backup_history (user_id, project_id, db_name, s3_key, status)
		VALUES ($1, $2, $3, $4, 'running')
		RETURNING id
	`, userID, projectID, dbName, s3Key).Scan(&historyID)
	if err != nil {
		slog.Error("Backup: failed to insert history", "db_name", dbName, "error", err)
		return
	}

	// Run pg_dump
	reader, err := s.dumpDatabase(ctx, dbName, ExportOptions{Format: "custom"})
	if err != nil {
		if ctx.Err() != nil {
			s.markBackupCancelled(ctx, historyID)
			return
		}
		s.markBackupFailed(ctx, historyID, fmt.Sprintf("pg_dump failed: %v", err))
		return
	}
	defer reader.Close()

	// Upload to S3
	digest := sha256.New()
	sizeBytes, err := s.uploadToS3(ctx, settings, s3Key, io.TeeReader(reader, digest))
	dumpErr := reader.Close()
	if err == nil {
		err = dumpErr
	}
	if err != nil {
		if ctx.Err() != nil {
			// Try to clean up partial upload with a fresh context
			s.deleteS3Object(context.Background(), settings, s3Key)
			s.markBackupCancelled(context.Background(), historyID)
			return
		}
		s.markBackupFailed(ctx, historyID, fmt.Sprintf("S3 upload failed: %v", err))
		return
	}

	// Mark completed
	if _, err := s.db.Exec(ctx, `
		UPDATE platform.backup_history
		SET status = 'completed', size_bytes = $1, completed_at = NOW(), content_sha256=$3,
			source_endpoint=$4, source_region=$5, source_bucket=$6
		WHERE id = $2
	`, sizeBytes, historyID, hex.EncodeToString(digest.Sum(nil)), settings.S3Endpoint, settings.S3Region, settings.S3Bucket); err != nil {
		slog.Error("failed to update backup history", "error", err)
	}

	slog.Info("Backup completed", "db_name", dbName, "s3_key", s3Key, "size_bytes", sizeBytes)

	// Enforce retention policy
	prefix := settings.S3PathPrefix + dbName + "/"
	s.enforceRetention(ctx, settings, prefix)
}

func (s *BackupService) markBackupFailed(ctx context.Context, historyID int64, errMsg string) {
	slog.Error("Backup failed", "history_id", historyID, "error", errMsg)
	if _, err := s.db.Exec(ctx, `
		UPDATE platform.backup_history
		SET status = 'failed', error_message = $1, completed_at = NOW()
		WHERE id = $2
	`, errMsg, historyID); err != nil {
		slog.Error("failed to update backup history", "error", err)
	}
}

func (s *BackupService) markBackupCancelled(ctx context.Context, historyID int64) {
	slog.Info("Backup cancelled", "history_id", historyID)
	if _, err := s.db.Exec(ctx, `
		UPDATE platform.backup_history
		SET status = 'cancelled', completed_at = NOW()
		WHERE id = $1
	`, historyID); err != nil {
		slog.Error("failed to update backup history", "error", err)
	}
}

// deleteS3Object attempts to remove a partial S3 upload after cancellation.
func (s *BackupService) deleteS3Object(ctx context.Context, settings *backupSettingsInternal, key string) {
	client, err := s.getS3Client(ctx, settings)
	if err != nil {
		slog.Error("failed to create S3 client for cleanup", "error", err)
		return
	}
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(settings.S3Bucket),
		Key:    aws.String(key),
	}); err != nil {
		slog.Error("failed to delete partial S3 backup", "key", key, "error", err)
	}
}

// dumpDatabase runs pg_dump for a specific database and returns a reader of the output.
func (s *BackupService) dumpDatabase(ctx context.Context, dbName string, opts ExportOptions) (io.ReadCloser, error) {
	dbURL, err := s.projectDBURL(ctx, dbName)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		return nil, err
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	user := u.User.Username()

	args := buildExportArgs(host, port, user, dbName, opts)
	args = append(args, "--exclude-schema=platform")

	serverMajor, err := pgServerMajor(ctx, s.db)
	if err != nil {
		return nil, err
	}
	clientMajor, _ := pgClientMajor(serverMajor)
	tool, err := pgTool(ctx, "pg_dump", clientMajor)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = pgCommandEnv(dbURL)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pg_dump: %w", err)
	}

	return &dumpReader{cmd: cmd, ReadCloser: stdout}, nil
}

// dumpReader wraps the stdout pipe and waits for the command to finish on Close.
type dumpReader struct {
	cmd *exec.Cmd
	io.ReadCloser
	closed   bool
	closeErr error
}

// A failed pg_dump must also fail consumers which only read the stream. Waiting
// at EOF exposes command failures before an empty export can be reported as OK.
func (r *dumpReader) Read(p []byte) (int, error) {
	if r.closed {
		if r.closeErr != nil {
			return 0, r.closeErr
		}
		return 0, io.EOF
	}
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		r.closeErr = r.cmd.Wait()
		r.closed = true
		if r.closeErr != nil {
			return n, r.closeErr
		}
	}
	return n, err
}

func (r *dumpReader) Close() error {
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	err := r.ReadCloser.Close()
	cmdErr := r.cmd.Wait()
	if err != nil {
		r.closeErr = err
	} else {
		r.closeErr = cmdErr
	}
	return r.closeErr
}

// uploadToS3 decrypts S3 credentials and uploads the dump to S3.
func (s *BackupService) uploadToS3(ctx context.Context, settings *backupSettingsInternal, key string, body io.Reader) (int64, error) {
	client, err := s.getS3Client(ctx, settings)
	if err != nil {
		return 0, fmt.Errorf("create S3 client: %w", err)
	}

	// The uploader buffers bounded parts for an unseekable pg_dump stream.
	// Shared job admission caps total workers; each upload uses at most two parts.
	cr := &countingReader{r: body}
	uploader := transfermanager.New(client, func(u *transfermanager.Options) {
		u.PartSizeBytes = 5 * 1024 * 1024
		u.Concurrency = 2
		u.FailTimeout = 15 * time.Second
	})
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(settings.S3Bucket),
		Key:    aws.String(key),
		Body:   cr,
	})
	if err != nil {
		return 0, fmt.Errorf("S3 upload: %w", err)
	}

	return cr.n, nil
}

// countingReader counts the number of bytes read.
type countingReader struct {
	r io.Reader
	n int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	return n, err
}

// getS3Client creates an S3 client from backup settings.
func (s *BackupService) getS3Client(ctx context.Context, settings *backupSettingsInternal) (*s3.Client, error) {
	httpClient, err := storageHTTPClient(settings.S3Endpoint)
	if err != nil {
		return nil, err
	}
	accessKey, err := DecryptPgPassword(settings.S3AccessKeyEncrypted, s.backupKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt access key: %w", err)
	}
	secretKey, err := DecryptPgPassword(settings.S3SecretKeyEncrypted, s.backupKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret key: %w", err)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(settings.S3Region),
		awsconfig.WithHTTPClient(httpClient),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if settings.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(settings.S3Endpoint)
			o.UsePathStyle = true
		}
	})
	return client, nil
}

// enforceRetention deletes backups older than the retention period from S3.
func (s *BackupService) enforceRetention(ctx context.Context, settings *backupSettingsInternal, prefix string) {
	if settings.RetentionDays <= 0 {
		return
	}

	client, err := s.getS3Client(ctx, settings)
	if err != nil {
		slog.Warn("failed to create S3 client for retention cleanup", "error", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -settings.RetentionDays)

	var continuationToken *string
	for {
		input := &s3.ListObjectsV2Input{
			Bucket:            &settings.S3Bucket,
			Prefix:            &prefix,
			ContinuationToken: continuationToken,
		}

		output, err := client.ListObjectsV2(ctx, input)
		if err != nil {
			slog.Warn("failed to list S3 objects for retention", "error", err)
			return
		}

		for _, obj := range output.Contents {
			if obj.LastModified != nil && obj.LastModified.Before(cutoff) {
				_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
					Bucket: &settings.S3Bucket,
					Key:    obj.Key,
				})
				if err != nil {
					slog.Warn("failed to delete old backup", "key", *obj.Key, "error", err)
				} else {
					slog.Info("deleted old backup", "key", *obj.Key, "age_days", int(time.Since(*obj.LastModified).Hours()/24))
				}
			}
		}

		if output.IsTruncated == nil || !*output.IsTruncated {
			break
		}
		continuationToken = output.NextContinuationToken
	}
}
