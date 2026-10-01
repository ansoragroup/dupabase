package platform

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImportService handles database import operations.
type ImportService struct {
	db          *pgxpool.Pool
	databaseURL string
	poolManager *database.PoolManager

	mu             sync.Mutex
	processes      map[int64]*exec.Cmd // taskID -> running process for cancellation
	cancels        map[int64]context.CancelFunc
	activeProjects map[string]bool
}

// NewImportService creates a new ImportService.
func NewImportService(db *pgxpool.Pool, databaseURL string) *ImportService {
	return &ImportService{
		db:             db,
		databaseURL:    databaseURL,
		processes:      make(map[int64]*exec.Cmd),
		cancels:        make(map[int64]context.CancelFunc),
		activeProjects: make(map[string]bool),
	}
}

func (s *ImportService) SetPoolManager(pm *database.PoolManager) { s.poolManager = pm }

func (s *ImportService) projectDBURL(ctx context.Context, dbName string) (string, error) {
	if s.poolManager == nil {
		return "", fmt.Errorf("project credential provider is not configured")
	}
	return s.poolManager.ProjectDatabaseURL(ctx, dbName)
}

func (s *ImportService) reserveJob(projectID string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProjects[projectID] || len(s.activeProjects) >= 4 {
		return nil, fmt.Errorf("an import is already running or all import workers are busy")
	}
	s.activeProjects[projectID] = true
	return func() { s.mu.Lock(); delete(s.activeProjects, projectID); s.mu.Unlock() }, nil
}

// ImportOptions controls import behavior.
type ImportOptions struct {
	CleanImport      bool `json:"clean_import"`
	SkipAuthSchema   bool `json:"skip_auth_schema"`
	DisableTriggers  bool `json:"disable_triggers"`
	MigrateAuthUsers bool `json:"migrate_auth_users"`
}

// DumpAnalysis contains the results of analyzing a database dump file.
type DumpAnalysis struct {
	IsSupabaseDump    bool     `json:"is_supabase_dump"`
	Format            string   `json:"format"`
	HasAuthUsers      bool     `json:"has_auth_users"`
	HasMigrations     bool     `json:"has_migrations"`
	SupabaseSchemas   []string `json:"supabase_schemas"`
	DetectedSignals   []string `json:"detected_signals"`
	RecommendedAction string   `json:"recommended_action"`
}

// supabaseSignatures are strings that indicate a Supabase dump.
var supabaseSignatures = []struct {
	pattern string
	signal  string
}{
	{"supabase_migrations", "supabase_migrations schema"},
	{"supabase_admin", "supabase_admin role"},
	{"supabase_auth_admin", "supabase_auth_admin role"},
	{"supabase_storage_admin", "supabase_storage_admin role"},
	{"authenticator", "authenticator role"},
	{"CREATE SCHEMA IF NOT EXISTS auth", "auth schema creation"},
	{"auth.users", "auth.users table"},
	{"auth.sessions", "auth.sessions table"},
	{"auth.refresh_tokens", "auth.refresh_tokens table"},
	{"extensions.uuid-ossp", "uuid-ossp extension"},
	{"pgsodium", "pgsodium schema"},
	{"supabase_functions", "supabase_functions schema"},
	{"realtime", "realtime schema"},
	{"graphql_public", "graphql_public schema"},
	{"storage.objects", "storage.objects table"},
}

var supabaseSchemaNames = []string{
	"auth", "extensions", "supabase_functions", "graphql", "graphql_public",
	"realtime", "_realtime", "storage", "vault", "pgsodium",
}

// ImportTaskResponse is the public-facing import task record.
type ImportTaskResponse struct {
	ID             int64      `json:"id"`
	ProjectID      string     `json:"project_id"`
	DBName         string     `json:"db_name"`
	FileName       string     `json:"file_name"`
	FileSize       int64      `json:"file_size"`
	Format         string     `json:"format"`
	Status         string     `json:"status"`
	ErrorMessage   *string    `json:"error_message"`
	TablesImported *int       `json:"tables_imported"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

// Regex patterns for filtering Supabase-specific SQL statements
var sqlFilterPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*CREATE\s+SCHEMA\s+(IF\s+NOT\s+EXISTS\s+)?(auth|extensions|supabase_functions|graphql|graphql_public|realtime|_realtime|storage|vault|pgsodium)\b`),
	regexp.MustCompile(`(?i)^\s*ALTER\s+SCHEMA\s+(auth|extensions|supabase_functions|graphql|graphql_public|realtime|_realtime|storage|vault|pgsodium)\b`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?auth\.`),
	regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(ONLY\s+)?auth\.`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+(UNIQUE\s+)?INDEX\s+.+\bON\s+auth\.`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?extensions\.`),
	regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(ONLY\s+)?extensions\.`),
	regexp.MustCompile(`(?i)^\s*COPY\s+(auth|extensions)\.`),
	regexp.MustCompile(`(?i)^\s*INSERT\s+INTO\s+auth\.`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+ROLE\b`),
	regexp.MustCompile(`(?i)^\s*ALTER\s+ROLE\b`),
	regexp.MustCompile(`(?i)^\s*DROP\s+ROLE\b`),
	regexp.MustCompile(`(?i)^\s*GRANT\s+.*\bTO\s+(supabase_admin|supabase_auth_admin|supabase_storage_admin|authenticator|dashboard_user|pgbouncer|pgsodium_keyholder|pgsodium_keyiduser|pgsodium_keymaker|service_role|anon|authenticated)\b`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+(OR\s+REPLACE\s+)?FUNCTION\s+auth\.`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+TRIGGER\s+.*\bON\s+auth\.`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+POLICY\s+.*\bON\s+auth\.`),
	regexp.MustCompile(`(?i)^\s*CREATE\s+EXTENSION\b`),
	regexp.MustCompile(`(?i)^\s*COMMENT\s+ON\s+EXTENSION\b`),
	regexp.MustCompile(`(?i)^\s*SET\s+.*search_path\s*=.*\bauth\b`),
}

// TOC filter patterns for custom dump format.
// Match the actual schema field, so a public table/function named "auth" or
// "extensions" is preserved. This is a compatibility filter, not isolation.
var tocFilterPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*\d+;\s+\d+\s+\d+\s+[A-Z][A-Z ]*\s+(auth|extensions|supabase_[a-z0-9_]*)\s`),
	regexp.MustCompile(`^\s*\d+;\s+\d+\s+\d+\s+(SCHEMA|ACL|COMMENT)\s+-\s+(SCHEMA\s+)?(auth|extensions|supabase_[a-z0-9_]*)\s`),
	regexp.MustCompile(`^\s*\d+;\s+\d+\s+\d+\s+(CREATE ROLE|ALTER ROLE|CREATE EXTENSION)\s`),
}

// StartImport validates the project exists and is active, saves metadata, and launches async import.
// Access control is handled at the router level via org membership.
func (s *ImportService) StartImport(ctx context.Context, userID, projectID, filePath, fileName string, fileSize int64, opts ImportOptions) (*ImportTaskResponse, int, error) {
	// Validate project exists and is active (org access checked by router)
	var dbName string
	err := s.db.QueryRow(ctx, `
		SELECT db_name FROM platform.projects
		WHERE id = $1 AND status = 'active'
	`, projectID).Scan(&dbName)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found or not active")
	}

	// Detect format
	format := detectFormat(filePath)
	release, err := s.reserveJob(projectID)
	if err != nil {
		return nil, http.StatusTooManyRequests, err
	}

	// Insert task record
	var taskID int64
	err = s.db.QueryRow(ctx, `
		INSERT INTO platform.import_tasks (user_id, project_id, db_name, file_name, file_size, format, options, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'running')
		RETURNING id
	`, userID, projectID, dbName, fileName, fileSize, format,
		fmt.Sprintf(`{"clean_import":%t,"skip_auth_schema":%t,"disable_triggers":%t}`,
			opts.CleanImport, opts.SkipAuthSchema, opts.DisableTriggers),
	).Scan(&taskID)
	if err != nil {
		release()
		return nil, http.StatusInternalServerError, fmt.Errorf("create import task: %w", err)
	}

	// Launch async import
	go func() { defer release(); s.executeImport(taskID, dbName, filePath, format, opts) }()

	task := &ImportTaskResponse{
		ID:        taskID,
		ProjectID: projectID,
		DBName:    dbName,
		FileName:  fileName,
		FileSize:  fileSize,
		Format:    format,
		Status:    "running",
		StartedAt: time.Now(),
	}

	return task, http.StatusAccepted, nil
}

// GetImportStatus returns the latest import task for a project.
func (s *ImportService) GetImportStatus(ctx context.Context, projectID string, taskID int64) (*ImportTaskResponse, int, error) {
	var t ImportTaskResponse
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, db_name, file_name, file_size, format,
			status, error_message, tables_imported, started_at, completed_at
		FROM platform.import_tasks
		WHERE id = $1 AND project_id = $2
	`, taskID, projectID).Scan(
		&t.ID, &t.ProjectID, &t.DBName, &t.FileName, &t.FileSize, &t.Format,
		&t.Status, &t.ErrorMessage, &t.TablesImported, &t.StartedAt, &t.CompletedAt,
	)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("import task not found")
	}
	return &t, http.StatusOK, nil
}

// GetImportHistory returns all import tasks for a project.
// Auth is handled at the handler level via org membership checks.
func (s *ImportService) GetImportHistory(ctx context.Context, projectID string) ([]ImportTaskResponse, int, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, db_name, file_name, file_size, format,
			status, error_message, tables_imported, started_at, completed_at
		FROM platform.import_tasks
		WHERE project_id = $1
		ORDER BY started_at DESC
		LIMIT 50
	`, projectID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query import history: %w", err)
	}
	defer rows.Close()

	var tasks []ImportTaskResponse
	for rows.Next() {
		var t ImportTaskResponse
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.DBName, &t.FileName, &t.FileSize, &t.Format,
			&t.Status, &t.ErrorMessage, &t.TablesImported, &t.StartedAt, &t.CompletedAt); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan import task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []ImportTaskResponse{}
	}
	return tasks, http.StatusOK, nil
}

// CancelImport cancels a running import.
func (s *ImportService) CancelImport(ctx context.Context, taskID int64) (int, error) {
	// Claim cancellation before signalling the worker; a completed job must not
	// be overwritten by a concurrent cancellation or a late worker error.
	tag, err := s.db.Exec(ctx, `UPDATE platform.import_tasks
		SET status='cancelled', completed_at=NOW() WHERE id=$1 AND status='running'`, taskID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("cancel import: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		err = s.db.QueryRow(ctx, `
		SELECT status FROM platform.import_tasks
		WHERE id = $1
	`, taskID).Scan(&status)
		if err != nil {
			return http.StatusNotFound, fmt.Errorf("import task not found")
		}
		return http.StatusBadRequest, fmt.Errorf("import is not running (status: %s)", status)
	}

	s.mu.Lock()
	cancel := s.cancels[taskID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	return http.StatusOK, nil
}

// --- Internal ---

// executeImport runs the actual import in a goroutine.
func (s *ImportService) executeImport(taskID int64, dbName, filePath, format string, opts ImportOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	s.mu.Lock()
	s.cancels[taskID] = cancel
	s.mu.Unlock()

	defer os.Remove(filePath)

	defer func() {
		// Clean up process reference
		s.mu.Lock()
		delete(s.processes, taskID)
		delete(s.cancels, taskID)
		s.mu.Unlock()
	}()

	var taskStatus string
	if err := s.db.QueryRow(ctx, `SELECT status FROM platform.import_tasks WHERE id=$1`, taskID).Scan(&taskStatus); err != nil || taskStatus != "running" {
		return
	}

	// Build target database URL
	dbURL, err := s.projectDBURL(ctx, dbName)
	if err != nil {
		s.markImportFailed(ctx, taskID, fmt.Sprintf("build DB URL: %v", err))
		return
	}

	// Check version compatibility before cleaning data or changing triggers.
	var restorePlan *pgRestorePlan
	if format == "custom" {
		restorePlan, err = preparePGRestore(ctx, s.db, filePath)
	} else {
		err = preflightSQLDump(ctx, s.db, filePath)
	}
	if err != nil {
		s.markImportFailed(ctx, taskID, fmt.Sprintf("import compatibility preflight: %v", err))
		return
	}

	// Pre-import: handle clean import and disable triggers
	if opts.CleanImport || opts.DisableTriggers {
		if err := s.preImport(ctx, dbURL, opts); err != nil {
			s.markImportFailed(ctx, taskID, fmt.Sprintf("pre-import: %v", err))
			return
		}
	}
	triggersPending := opts.DisableTriggers
	restoreTriggers := func() error {
		if !triggersPending {
			return nil
		}
		triggersPending = false
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		return s.postImport(cleanupCtx, dbURL)
	}
	defer func() {
		if err := restoreTriggers(); err != nil {
			slog.Error("restore import triggers", "task_id", taskID, "error", err)
		}
	}()

	var importErr error
	var tableCount int

	if format == "custom" {
		tableCount, importErr = s.importCustomDump(ctx, taskID, dbURL, filePath, opts, restorePlan)
	} else {
		tableCount, importErr = s.importPlainSQL(ctx, taskID, dbURL, filePath, opts)
	}

	// Post-import: re-enable triggers
	if err := restoreTriggers(); err != nil && importErr == nil {
		importErr = err
	}

	if importErr != nil {
		s.markImportFailed(ctx, taskID, importErr.Error())
		return
	}

	// Migrate auth users if requested (run on the original file before cleanup)
	if opts.MigrateAuthUsers {
		users, extractErr := extractSupabaseAuthUsers(filePath)
		if extractErr != nil {
			slog.Warn("Auth user extraction failed (non-fatal)", "task_id", taskID, "error", extractErr)
		} else if len(users) > 0 {
			migrated, insertErr := insertMigratedUsers(ctx, dbURL, users)
			if insertErr != nil {
				slog.Warn("Auth user migration had errors", "task_id", taskID, "error", insertErr)
			}
			slog.Info("Auth users migrated", "task_id", taskID, "migrated", migrated, "total", len(users))
		}
	}

	// Mark completed
	if _, err := s.db.Exec(ctx, `
		UPDATE platform.import_tasks
		SET status = 'completed', tables_imported = $1, completed_at = NOW()
		WHERE id = $2 AND status = 'running'
	`, tableCount, taskID); err != nil {
		slog.Error("failed to mark import as completed", "task_id", taskID, "error", err)
	}

	slog.Info("Import completed", "task_id", taskID, "db", dbName, "tables", tableCount)
}

func (s *ImportService) buildDBURL(dbName string) (string, error) {
	u, err := url.Parse(s.databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func (s *ImportService) preImport(ctx context.Context, dbURL string, opts ImportOptions) error {
	var stmts []string

	if opts.DisableTriggers {
		stmts = append(stmts, "DO $$ DECLARE r RECORD; BEGIN FOR r IN (SELECT schemaname,tablename FROM pg_tables WHERE schemaname='public') LOOP EXECUTE format('ALTER TABLE %I.%I DISABLE TRIGGER USER',r.schemaname,r.tablename); END LOOP; END $$;")
	}

	if opts.CleanImport {
		stmts = append(stmts,
			"DO $$ DECLARE r RECORD; BEGIN FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP EXECUTE 'DROP TABLE IF EXISTS public.' || quote_ident(r.tablename) || ' CASCADE'; END LOOP; END $$;",
		)
	}

	if len(stmts) == 0 {
		return nil
	}

	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return fmt.Errorf("parse DB URL: %w", err)
	}

	sqlStr := strings.Join(stmts, "\n")
	cmd, err := pgPSQLCommand(ctx,
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		"-c", sqlStr,
	)
	if err != nil {
		return err
	}
	cmd.Env = pgCommandEnv(dbURL)
	out, err := runBoundedCommand(cmd)
	if err != nil {
		return fmt.Errorf("pre-import SQL: %s: %w", string(out), err)
	}
	return nil
}

func (s *ImportService) postImport(ctx context.Context, dbURL string) error {
	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return fmt.Errorf("post-import DB URL: %w", err)
	}
	cmd, err := pgPSQLCommand(ctx,
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		"-c", "DO $$ DECLARE r RECORD; BEGIN FOR r IN (SELECT schemaname,tablename FROM pg_tables WHERE schemaname='public') LOOP EXECUTE format('ALTER TABLE %I.%I ENABLE TRIGGER USER',r.schemaname,r.tablename); END LOOP; END $$;",
	)
	if err != nil {
		return err
	}
	cmd.Env = pgCommandEnv(dbURL)
	out, err := runBoundedCommand(cmd)
	if err != nil {
		return fmt.Errorf("re-enable import triggers: %s: %w", out, err)
	}
	return nil
}

// importCustomDump handles pg_restore for custom format dumps.
func (s *ImportService) importCustomDump(ctx context.Context, taskID int64, dbURL, filePath string, opts ImportOptions, plan *pgRestorePlan) (int, error) {
	cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	if opts.SkipAuthSchema {
		// Use pg_restore --list to get TOC, filter it, then use --use-list
		return s.importCustomDumpFiltered(cancelCtx, taskID, dbURL, filePath, plan)
	}

	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return 0, fmt.Errorf("parse DB URL: %w", err)
	}

	// Direct restore (--clean --if-exists drops before creating)
	cmd := exec.CommandContext(cancelCtx, plan.tool,
		"--exit-on-error",
		"--single-transaction",
		"--exclude-schema=platform",
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		filePath,
	)
	cmd.Env = pgCommandEnv(dbURL)

	s.mu.Lock()
	s.processes[taskID] = cmd
	s.mu.Unlock()

	out, err := runBoundedCommand(cmd)
	if err != nil {
		// pg_restore returns exit code 1 for warnings too, check output
		outStr := string(out)
		return 0, fmt.Errorf("pg_restore: %s", outStr)

	}

	tableCount := countRestoredTables(ctx, dbURL)
	return tableCount, nil
}

// importCustomDumpFiltered does TOC-based filtering for custom dumps.
func (s *ImportService) importCustomDumpFiltered(ctx context.Context, taskID int64, dbURL, filePath string, plan *pgRestorePlan) (int, error) {
	// Reuse the TOC inspected before destructive pre-import work.
	filteredTOC := filterTOC(plan.toc)

	// Step 3: Write filtered TOC to temp file
	tocFile, err := os.CreateTemp("", "import-toc-*.list")
	if err != nil {
		return 0, fmt.Errorf("create TOC temp file: %w", err)
	}
	defer os.Remove(tocFile.Name())

	if _, err := tocFile.WriteString(filteredTOC); err != nil {
		tocFile.Close()
		return 0, fmt.Errorf("write TOC: %w", err)
	}
	tocFile.Close()

	// Step 4: Restore with filtered TOC (--clean --if-exists drops before creating)
	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return 0, fmt.Errorf("parse DB URL: %w", err)
	}

	cmd := exec.CommandContext(ctx, plan.tool,
		"--exit-on-error",
		"--single-transaction",
		"--exclude-schema=platform",
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
		"--use-list="+tocFile.Name(),
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		filePath,
	)
	cmd.Env = pgCommandEnv(dbURL)

	s.mu.Lock()
	s.processes[taskID] = cmd
	s.mu.Unlock()

	out, err := runBoundedCommand(cmd)
	if err != nil {
		outStr := string(out)
		return 0, fmt.Errorf("pg_restore: %s", outStr)

	}

	tableCount := countRestoredTables(ctx, dbURL)
	return tableCount, nil
}

// importPlainSQL handles plain SQL file import.
func (s *ImportService) importPlainSQL(ctx context.Context, taskID int64, dbURL, filePath string, opts ImportOptions) (int, error) {
	cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	importFile, err := filterSQLFileOptions(filePath, opts.SkipAuthSchema)
	if err != nil {
		return 0, fmt.Errorf("filter SQL: %w", err)
	}
	defer os.Remove(importFile)
	input, err := os.Open(importFile)
	if err != nil {
		return 0, err
	}
	defer input.Close()

	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return 0, fmt.Errorf("parse DB URL: %w", err)
	}

	cmd, err := pgPSQLCommand(cancelCtx,
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		"-X", "--set=ON_ERROR_STOP=on",
	)
	if err != nil {
		return 0, err
	}
	// Restriction belongs to the worker, not the uploaded dump. On older psql,
	// the unknown command is fatal, rather than silently running unprotected.
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return 0, err
	}
	key := hex.EncodeToString(keyBytes)
	cmd.Stdin = io.MultiReader(strings.NewReader("\\restrict "+key+"\n"), input, strings.NewReader("\n"))
	cmd.Env = pgCommandEnv(dbURL)

	s.mu.Lock()
	s.processes[taskID] = cmd
	s.mu.Unlock()

	out, err := runBoundedCommand(cmd)
	outStr := string(out)
	if err != nil {
		return 0, fmt.Errorf("psql import: %s", outStr)
	}

	tableCount := countRestoredTables(cancelCtx, dbURL)
	if tableCount > 0 && !isOnlyWarnings(outStr) {
		slog.Warn("import completed with errors", "task_id", taskID, "tables", tableCount, "stderr", outStr)
	}
	return tableCount, nil
}

func (s *ImportService) markImportFailed(ctx context.Context, taskID int64, errMsg string) {
	slog.Error("Import failed", "task_id", taskID, "error", errMsg)
	updateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.db.Exec(updateCtx, `
		UPDATE platform.import_tasks
		SET status = 'failed', error_message = $1, completed_at = NOW()
		WHERE id = $2 AND status = 'running'
	`, errMsg, taskID)
	if err != nil {
		slog.Error("record failed import", "task_id", taskID, "error", err)
	}
}

// splitDBURL parses a PostgreSQL connection URL into its components.
func splitDBURL(dbURL string) (host, port, user, password, dbName string, err error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", "", "", "", "", err
	}
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		port = "5432"
	}
	user = u.User.Username()
	password, _ = u.User.Password()
	dbName = strings.TrimPrefix(u.Path, "/")
	return
}

// --- Helpers ---

// detectFormat checks file magic bytes to determine if it's a custom pg_dump or plain SQL.
func detectFormat(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return "sql"
	}
	defer f.Close()

	header := make([]byte, 5)
	n, err := f.Read(header)
	if err != nil || n < 5 {
		return "sql"
	}

	// Custom pg_dump format starts with "PGDMP"
	if string(header) == "PGDMP" {
		return "custom"
	}

	return "sql"
}

// filterTOC filters a pg_restore TOC listing, removing auth/extensions entries.
func filterTOC(toc string) string {
	var filtered strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(toc))

	for scanner.Scan() {
		line := scanner.Text()

		// Always keep comment lines (starting with ;)
		if strings.HasPrefix(line, ";") {
			filtered.WriteString(line)
			filtered.WriteString("\n")
			continue
		}

		skip := false
		for _, pattern := range tocFilterPatterns {
			if pattern.MatchString(line) {
				skip = true
				break
			}
		}

		if !skip {
			filtered.WriteString(line)
			filtered.WriteString("\n")
		}
	}

	return filtered.String()
}

// isOnlyWarnings checks if pg_restore/psql output contains only warnings.
// Returns true if no error-level lines are found.
// NOTE: Detects both PostgreSQL ERROR/FATAL/PANIC lines and pg_restore: error: lines.
func isOnlyWarnings(output string) bool {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// pg_restore warnings start with "pg_restore: warning:" or "WARNING:"
		if strings.HasPrefix(line, "pg_restore: warning:") ||
			strings.HasPrefix(line, "WARNING:") ||
			strings.HasPrefix(line, "DETAIL:") ||
			strings.HasPrefix(line, "HINT:") {
			continue
		}
		// Specific error patterns from pg_restore/pg_dump/psql and PostgreSQL
		if strings.Contains(line, "pg_restore: error:") || strings.Contains(line, "pg_dump: error:") ||
			strings.HasPrefix(line, "ERROR:") ||
			strings.Contains(line, "FATAL:") || strings.Contains(line, "PANIC:") {
			return false
		}
		// pg_restore archiver errors
		if strings.Contains(line, "pg_restore: [archiver]") {
			return false
		}
	}
	return true
}

// countRestoredTables counts public tables in the target database.
func countRestoredTables(ctx context.Context, dbURL string) int {
	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return 0
	}
	cmd, err := pgPSQLCommand(ctx,
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		"-tAc", "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'",
	)
	if err != nil {
		return 0
	}
	cmd.Env = pgCommandEnv(dbURL)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var count int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
	return count
}

// AnalyzeDump inspects a dump file and detects whether it's a Supabase dump.
// It reads up to the first 10MB of the file looking for Supabase-specific signatures.
func (s *ImportService) AnalyzeDump(filePath string) (*DumpAnalysis, int, error) {
	format := detectFormat(filePath)

	analysis := &DumpAnalysis{
		Format:          format,
		SupabaseSchemas: []string{},
		DetectedSignals: []string{},
	}

	if format == "custom" {
		// For custom format, use pg_restore --list to get TOC and analyze it
		analysisCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tool, err := pgLatestTool(analysisCtx, "pg_restore")
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		cmd := exec.CommandContext(analysisCtx, tool, "--list", filePath)
		cmd.Env = pgCommandEnv("")
		tocOutput, err := runBoundedCommand(cmd)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("pg_restore --list: %w", err)
		}
		s.analyzeContent(string(tocOutput), analysis)
	} else {
		// For SQL files, read first 10MB
		f, err := os.Open(filePath)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("open file: %w", err)
		}
		defer f.Close()

		buf := make([]byte, 10*1024*1024)
		n, _ := f.Read(buf)
		content := string(buf[:n])
		s.analyzeContent(content, analysis)
	}

	// Set recommendation
	if analysis.IsSupabaseDump {
		if analysis.HasAuthUsers {
			analysis.RecommendedAction = "Import with 'Skip Auth Schema' enabled and optionally 'Migrate Auth Users' to transfer existing users to Dupabase's auth system."
		} else {
			analysis.RecommendedAction = "Import with 'Skip Auth Schema' enabled. Supabase-specific schemas will be filtered automatically."
		}
	} else {
		analysis.RecommendedAction = "Standard import. No Supabase-specific handling needed."
	}

	return analysis, http.StatusOK, nil
}

// analyzeContent scans content for Supabase signatures and populates the analysis.
func (s *ImportService) analyzeContent(content string, analysis *DumpAnalysis) {
	signalCount := 0
	for _, sig := range supabaseSignatures {
		if strings.Contains(content, sig.pattern) {
			analysis.DetectedSignals = append(analysis.DetectedSignals, sig.signal)
			signalCount++
		}
	}

	// Detect Supabase schemas present
	for _, schema := range supabaseSchemaNames {
		// Look for schema references (in TOC or SQL)
		if strings.Contains(content, schema+".") || strings.Contains(content, "SCHEMA "+schema) || strings.Contains(content, schema+" ") {
			analysis.SupabaseSchemas = append(analysis.SupabaseSchemas, schema)
		}
	}

	// Check for auth users data
	analysis.HasAuthUsers = strings.Contains(content, "auth.users") || strings.Contains(content, "COPY auth.users") || strings.Contains(content, "INSERT INTO auth.users")

	// Check for supabase_migrations
	analysis.HasMigrations = strings.Contains(content, "supabase_migrations")

	// Consider it a Supabase dump if we found 3+ signals
	analysis.IsSupabaseDump = signalCount >= 3
}

// MigrateSupabaseAuthUsers extracts auth users from a Supabase SQL dump
// and inserts them into the project's auth.users table with column mapping.
// Only works with plain SQL format dumps containing COPY or INSERT statements.
func (s *ImportService) MigrateSupabaseAuthUsers(ctx context.Context, projectID, filePath string) (int, int, error) {
	// Get project pool
	var dbName string
	err := s.db.QueryRow(ctx, `
		SELECT db_name FROM platform.projects WHERE id = $1 AND status = 'active'
	`, projectID).Scan(&dbName)
	if err != nil {
		return 0, http.StatusNotFound, fmt.Errorf("project not found or not active")
	}

	dbURL, err := s.buildDBURL(dbName)
	if err != nil {
		return 0, http.StatusInternalServerError, fmt.Errorf("build DB URL: %w", err)
	}

	// Read the file and extract auth users
	users, err := extractSupabaseAuthUsers(filePath)
	if err != nil {
		return 0, http.StatusBadRequest, fmt.Errorf("extract auth users: %w", err)
	}

	if len(users) == 0 {
		return 0, http.StatusOK, nil
	}

	// Insert users into the project's auth.users table
	migrated, err := insertMigratedUsers(ctx, dbURL, users)
	if err != nil {
		return migrated, http.StatusInternalServerError, fmt.Errorf("insert users: %w", err)
	}

	slog.Info("Supabase auth user migration completed", "project_id", projectID, "migrated", migrated, "total", len(users))
	return migrated, http.StatusOK, nil
}

// supabaseAuthUser represents a user extracted from a Supabase auth.users dump.
type supabaseAuthUser struct {
	id                string
	email             string
	encryptedPassword string
	emailConfirmedAt  string
	phone             string
	phoneConfirmedAt  string
	lastSignInAt      string
	rawAppMetaData    string
	rawUserMetaData   string
	isAnonymous       string
	bannedUntil       string
	createdAt         string
	updatedAt         string
}

// extractSupabaseAuthUsers parses a SQL dump file for COPY auth.users or INSERT INTO auth.users.
func extractSupabaseAuthUsers(filePath string) ([]supabaseAuthUser, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var users []supabaseAuthUser
	inCopy := false
	var copyColumns []string
	var insertAccum strings.Builder // accumulates multi-line INSERT statements

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if inCopy {
			if line == "\\." {
				inCopy = false
				continue
			}
			// Parse tab-separated COPY data
			user := parseCopyRow(line, copyColumns)
			if user != nil {
				users = append(users, *user)
			}
			continue
		}

		// If accumulating a multi-line INSERT statement
		if insertAccum.Len() > 0 {
			insertAccum.WriteString(" ")
			insertAccum.WriteString(trimmed)
			if strings.HasSuffix(trimmed, ";") {
				parsed := parseInsertIntoAuthUsers(insertAccum.String())
				users = append(users, parsed...)
				insertAccum.Reset()
			}
			continue
		}

		// Detect COPY auth.users ... FROM stdin
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "COPY AUTH.USERS") && strings.Contains(upper, "FROM STDIN") {
			copyColumns = parseCopyColumns(trimmed)
			if len(copyColumns) > 0 {
				inCopy = true
			}
			continue
		}

		// Detect INSERT INTO auth.users
		if strings.HasPrefix(upper, "INSERT INTO AUTH.USERS") {
			if strings.HasSuffix(trimmed, ";") {
				// Single-line INSERT
				parsed := parseInsertIntoAuthUsers(trimmed)
				users = append(users, parsed...)
			} else {
				// Multi-line INSERT — accumulate until semicolon
				insertAccum.WriteString(trimmed)
			}
			continue
		}
	}

	// Flush any remaining accumulated INSERT (missing semicolon at EOF)
	if insertAccum.Len() > 0 {
		parsed := parseInsertIntoAuthUsers(insertAccum.String())
		users = append(users, parsed...)
	}

	return users, scanner.Err()
}

// parseCopyColumns extracts column names from a COPY statement.
// e.g., "COPY auth.users (id, email, encrypted_password, ...) FROM stdin;"
func parseCopyColumns(stmt string) []string {
	start := strings.Index(stmt, "(")
	end := strings.Index(stmt, ")")
	if start < 0 || end < 0 || end <= start {
		return nil
	}

	colStr := stmt[start+1 : end]
	parts := strings.Split(colStr, ",")
	columns := make([]string, 0, len(parts))
	for _, p := range parts {
		columns = append(columns, strings.TrimSpace(p))
	}
	return columns
}

// parseCopyRow converts a tab-separated COPY data row into a supabaseAuthUser.
func parseCopyRow(line string, columns []string) *supabaseAuthUser {
	fields := strings.Split(line, "\t")
	if len(fields) < len(columns) {
		return nil
	}

	// Build column->value map
	colMap := make(map[string]string, len(columns))
	for i, col := range columns {
		if i < len(fields) {
			val := fields[i]
			if val == "\\N" {
				val = ""
			}
			colMap[col] = val
		}
	}

	return &supabaseAuthUser{
		id:                colMap["id"],
		email:             colMap["email"],
		encryptedPassword: colMap["encrypted_password"],
		emailConfirmedAt:  colMap["email_confirmed_at"],
		phone:             colMap["phone"],
		phoneConfirmedAt:  colMap["phone_confirmed_at"],
		lastSignInAt:      colMap["last_sign_in_at"],
		rawAppMetaData:    colMap["raw_app_meta_data"],
		rawUserMetaData:   colMap["raw_user_meta_data"],
		isAnonymous:       colMap["is_anonymous"],
		bannedUntil:       colMap["banned_until"],
		createdAt:         colMap["created_at"],
		updatedAt:         colMap["updated_at"],
	}
}

// insertMigratedUsers inserts extracted Supabase auth users into the target database.
// Existing users (by ID or email) are skipped.
func insertMigratedUsers(ctx context.Context, dbURL string, users []supabaseAuthUser) (int, error) {
	host, port, user, _, dbName, err := splitDBURL(dbURL)
	if err != nil {
		return 0, fmt.Errorf("parse DB URL: %w", err)
	}

	// Build a SQL script with INSERT statements
	var sb strings.Builder
	sb.WriteString("BEGIN;\n")
	for _, u := range users {
		if u.id == "" || u.email == "" {
			continue
		}

		// Use INSERT ... ON CONFLICT DO NOTHING to skip existing users
		sb.WriteString(fmt.Sprintf(
			`INSERT INTO auth.users (id, email, encrypted_password, email_confirmed_at, phone, phone_confirmed_at, last_sign_in_at, raw_app_meta_data, raw_user_meta_data, is_anonymous, banned_until, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s) ON CONFLICT (id) DO NOTHING;`+"\n",
			sqlQuote(u.id),
			sqlQuote(u.email),
			sqlQuote(u.encryptedPassword),
			sqlQuoteNullable(u.emailConfirmedAt),
			sqlQuoteNullable(u.phone),
			sqlQuoteNullable(u.phoneConfirmedAt),
			sqlQuoteNullable(u.lastSignInAt),
			sqlQuoteJSON(u.rawAppMetaData),
			sqlQuoteJSON(u.rawUserMetaData),
			sqlQuoteBool(u.isAnonymous),
			sqlQuoteNullable(u.bannedUntil),
			sqlQuoteTimestamp(u.createdAt),
			sqlQuoteTimestamp(u.updatedAt),
		))
	}
	sb.WriteString("COMMIT;\n")

	// Write to temp file and execute via psql
	tmpFile, err := os.CreateTemp("", "auth-migrate-*.sql")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(sb.String()); err != nil {
		tmpFile.Close()
		return 0, fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	cmd, err := pgPSQLCommand(ctx,
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		"-f", tmpFile.Name(),
	)
	if err != nil {
		return 0, err
	}
	cmd.Env = pgCommandEnv(dbURL)
	out, err := runBoundedCommand(cmd)
	if err != nil {
		outStr := string(out)
		slog.Warn("auth user migration had errors", "output", outStr)
	}

	// Count how many were actually inserted
	countCmd, err := pgPSQLCommand(ctx,
		"--host="+host, "--port="+port, "--username="+user, "--dbname="+dbName,
		"-tAc", "SELECT count(*) FROM auth.users",
	)
	if err != nil {
		return 0, err
	}
	countCmd.Env = pgCommandEnv(dbURL)
	countOut, err := countCmd.Output()
	if err != nil {
		return len(users), nil
	}
	var count int
	fmt.Sscanf(strings.TrimSpace(string(countOut)), "%d", &count)
	return count, nil
}

// --- INSERT INTO auth.users parsing ---

// parseInsertIntoAuthUsers parses an INSERT INTO auth.users (cols) VALUES (v1), (v2) statement.
func parseInsertIntoAuthUsers(stmt string) []supabaseAuthUser {
	// Extract column list between first ( and its matching )
	colStart := strings.Index(stmt, "(")
	if colStart < 0 {
		return nil
	}
	colEnd := strings.Index(stmt[colStart:], ")")
	if colEnd < 0 {
		return nil
	}
	colEnd += colStart
	colStr := stmt[colStart+1 : colEnd]
	columns := splitSQLValues(colStr)
	for i := range columns {
		columns[i] = strings.TrimSpace(columns[i])
	}
	if len(columns) == 0 {
		return nil
	}

	// Find VALUES keyword after the column list
	remainder := stmt[colEnd+1:]
	upperRem := strings.ToUpper(strings.TrimSpace(remainder))
	if !strings.HasPrefix(upperRem, "VALUES") {
		return nil
	}
	valuesIdx := strings.Index(strings.ToUpper(remainder), "VALUES")
	valuesStr := strings.TrimSpace(remainder[valuesIdx+6:])
	// Remove trailing semicolon
	valuesStr = strings.TrimSuffix(strings.TrimSpace(valuesStr), ";")

	// Split into individual value groups: (v1, v2), (v3, v4)
	groups := splitValueGroups(valuesStr)

	var users []supabaseAuthUser
	for _, group := range groups {
		// Remove outer parens
		group = strings.TrimSpace(group)
		if len(group) < 2 || group[0] != '(' || group[len(group)-1] != ')' {
			continue
		}
		inner := group[1 : len(group)-1]
		vals := splitSQLValues(inner)
		if len(vals) < len(columns) {
			continue
		}

		colMap := make(map[string]string, len(columns))
		for i, col := range columns {
			if i < len(vals) {
				colMap[col] = unquoteSQLValue(strings.TrimSpace(vals[i]))
			}
		}

		users = append(users, supabaseAuthUser{
			id:                colMap["id"],
			email:             colMap["email"],
			encryptedPassword: colMap["encrypted_password"],
			emailConfirmedAt:  colMap["email_confirmed_at"],
			phone:             colMap["phone"],
			phoneConfirmedAt:  colMap["phone_confirmed_at"],
			lastSignInAt:      colMap["last_sign_in_at"],
			rawAppMetaData:    colMap["raw_app_meta_data"],
			rawUserMetaData:   colMap["raw_user_meta_data"],
			isAnonymous:       colMap["is_anonymous"],
			bannedUntil:       colMap["banned_until"],
			createdAt:         colMap["created_at"],
			updatedAt:         colMap["updated_at"],
		})
	}
	return users
}

// splitValueGroups splits "(v1, v2), (v3, v4)" into ["(v1, v2)", "(v3, v4)"]
// respecting quoted strings and nested parentheses.
func splitValueGroups(s string) []string {
	var groups []string
	depth := 0
	inSingleQuote := false
	start := -1

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingleQuote {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++ // escaped quote
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inSingleQuote = true
		case '(':
			if depth == 0 {
				start = i
			}
			depth++
		case ')':
			depth--
			if depth == 0 && start >= 0 {
				groups = append(groups, s[start:i+1])
				start = -1
			}
		}
	}
	return groups
}

// splitSQLValues splits comma-separated SQL values, respecting single-quoted strings
// and nested parentheses (e.g., function calls, type casts).
func splitSQLValues(s string) []string {
	var result []string
	depth := 0
	inSingleQuote := false
	start := 0

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingleQuote {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++ // escaped quote
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inSingleQuote = true
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				result = append(result, s[start:i])
				start = i + 1
			}
		}
	}
	// Last segment
	if start <= len(s) {
		result = append(result, s[start:])
	}
	return result
}

// unquoteSQLValue removes SQL quotes, type casts (::jsonb, ::timestamptz), and handles NULL.
func unquoteSQLValue(s string) string {
	if s == "" {
		return ""
	}

	upper := strings.ToUpper(s)
	if upper == "NULL" {
		return ""
	}
	if upper == "TRUE" || upper == "T" {
		return "true"
	}
	if upper == "FALSE" || upper == "F" {
		return "false"
	}

	// Remove type casts like ::jsonb, ::uuid, ::timestamptz
	// Only strip if the cast is outside quotes
	if s[0] == '\'' {
		// Find matching closing quote (handle escaped quotes)
		end := -1
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				end = i
				break
			}
		}
		if end > 0 {
			inner := s[1:end]
			// Unescape doubled quotes
			return strings.ReplaceAll(inner, "''", "'")
		}
	}

	return s
}

// SQL escaping helpers for migration
func sqlQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sqlQuoteNullable(s string) string {
	if s == "" {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sqlQuoteJSON(s string) string {
	if s == "" || s == "\\N" {
		return "'{}'::jsonb"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'::jsonb"
}

func sqlQuoteBool(s string) string {
	if s == "true" || s == "t" {
		return "true"
	}
	return "false"
}

func sqlQuoteTimestamp(s string) string {
	if s == "" {
		return "NOW()"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
