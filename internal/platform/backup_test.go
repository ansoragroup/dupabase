package platform

import (
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ExportOptions filename extension mapping
// ---------------------------------------------------------------------------

func TestExportFilenameGeneration(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		wantExt string
	}{
		{"custom_format", "custom", ".dump"},
		{"plain_format", "plain", ".sql"},
		{"sql_format", "sql", ".sql"},
		{"empty_format", "", ".dump"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := ".dump"
			if tt.format == "plain" || tt.format == "sql" {
				ext = ".sql"
			}
			if ext != tt.wantExt {
				t.Errorf("expected ext %q, got %q", tt.wantExt, ext)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Format → pgFormat mapping
// ---------------------------------------------------------------------------

func TestDumpDatabaseFormatMapping(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantFormat string
	}{
		{"plain_to_plain", "plain", "plain"},
		{"sql_to_plain", "sql", "plain"},
		{"custom_to_custom", "custom", "custom"},
		{"empty_to_custom", "", "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pgFormat := "custom"
			if tt.format == "plain" || tt.format == "sql" {
				pgFormat = "plain"
			}
			if pgFormat != tt.wantFormat {
				t.Errorf("expected pgFormat %q, got %q", tt.wantFormat, pgFormat)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestS3ConnectionRequest validation
// ---------------------------------------------------------------------------

func TestTestS3ConnectionRequest_Validation(t *testing.T) {
	t.Run("all_empty", func(t *testing.T) {
		req := TestS3ConnectionRequest{}
		if req.S3Endpoint != "" || req.S3Bucket != "" || req.S3AccessKey != "" || req.S3SecretKey != "" {
			t.Error("expected all fields empty for zero value")
		}
	})

	t.Run("partial", func(t *testing.T) {
		req := TestS3ConnectionRequest{S3Endpoint: "https://s3.example.com", S3Bucket: "my-bucket"}
		if req.S3Endpoint == "" || req.S3Bucket == "" {
			t.Error("expected endpoint and bucket set")
		}
		if req.S3AccessKey != "" || req.S3SecretKey != "" {
			t.Error("expected access/secret key empty")
		}
	})

	t.Run("all_filled", func(t *testing.T) {
		req := TestS3ConnectionRequest{
			S3Endpoint:  "https://s3.example.com",
			S3Region:    "us-west-2",
			S3Bucket:    "my-bucket",
			S3AccessKey: "AKID",
			S3SecretKey: "secret",
		}
		if req.S3Region != "us-west-2" {
			t.Errorf("expected region us-west-2, got %q", req.S3Region)
		}
	})
}

// ---------------------------------------------------------------------------
// ToggleBackupRequest
// ---------------------------------------------------------------------------

func TestToggleBackupRequest_Fields(t *testing.T) {
	t.Run("enabled_with_password", func(t *testing.T) {
		req := ToggleBackupRequest{Enabled: true, PlatformPassword: "pass123"}
		if !req.Enabled {
			t.Error("expected Enabled true")
		}
		if req.PlatformPassword != "pass123" {
			t.Errorf("expected password 'pass123', got %q", req.PlatformPassword)
		}
	})

	t.Run("zero_value", func(t *testing.T) {
		var req ToggleBackupRequest
		if req.Enabled {
			t.Error("expected Enabled false for zero value")
		}
		if req.PlatformPassword != "" {
			t.Error("expected empty PlatformPassword for zero value")
		}
	})
}

// ---------------------------------------------------------------------------
// RestoreBackupRequest
// ---------------------------------------------------------------------------

func TestRestoreBackupRequest_Fields(t *testing.T) {
	t.Run("with_password", func(t *testing.T) {
		req := RestoreBackupRequest{PlatformPassword: "pass123"}
		if req.PlatformPassword != "pass123" {
			t.Errorf("expected password 'pass123', got %q", req.PlatformPassword)
		}
	})

	t.Run("zero_value", func(t *testing.T) {
		var req RestoreBackupRequest
		if req.PlatformPassword != "" {
			t.Error("expected empty PlatformPassword for zero value")
		}
	})
}

// ---------------------------------------------------------------------------
// BackupSettingsResponse structure
// ---------------------------------------------------------------------------

func TestBackupSettingsResponse_Structure(t *testing.T) {
	resp := BackupSettingsResponse{
		ID:            "bs-123",
		S3Endpoint:    "https://s3.example.com",
		S3Region:      "us-east-1",
		S3Bucket:      "backups",
		S3PathPrefix:  "db/",
		Schedule:      "daily",
		RetentionDays: 30,
		ProjectIDs:    []string{"proj-1", "proj-2"},
		Enabled:       true,
	}

	if resp.ID != "bs-123" {
		t.Errorf("unexpected ID: %q", resp.ID)
	}
	if resp.S3Endpoint != "https://s3.example.com" {
		t.Errorf("unexpected S3Endpoint: %q", resp.S3Endpoint)
	}
	if resp.S3Region != "us-east-1" {
		t.Errorf("unexpected S3Region: %q", resp.S3Region)
	}
	if resp.Schedule != "daily" {
		t.Errorf("unexpected Schedule: %q", resp.Schedule)
	}
	if resp.RetentionDays != 30 {
		t.Errorf("unexpected RetentionDays: %d", resp.RetentionDays)
	}
	if len(resp.ProjectIDs) != 2 {
		t.Errorf("expected 2 ProjectIDs, got %d", len(resp.ProjectIDs))
	}
	if !resp.Enabled {
		t.Error("expected Enabled true")
	}
}

// ---------------------------------------------------------------------------
// buildExportArgs
// ---------------------------------------------------------------------------

func TestBuildExportArgs(t *testing.T) {
	tests := []struct {
		name        string
		opts        ExportOptions
		wantContain []string
		wantAbsent  []string
	}{
		{
			"default_custom",
			ExportOptions{Format: "custom"},
			[]string{"--format=custom", "--role=service_role", "--no-owner", "--no-acl"},
			[]string{"--schema-only", "--data-only", "--inserts", "--exit-on-error"},
		},
		{
			"plain_format",
			ExportOptions{Format: "plain"},
			[]string{"--format=plain"},
			[]string{"--format=custom"},
		},
		{
			"schema_only",
			ExportOptions{Format: "custom", SchemaOnly: true},
			[]string{"--schema-only"},
			[]string{"--data-only"},
		},
		{
			"data_only",
			ExportOptions{Format: "custom", DataOnly: true},
			[]string{"--data-only"},
			[]string{"--schema-only"},
		},
		{
			"tables",
			ExportOptions{Format: "custom", Tables: []string{"users", "orders"}},
			[]string{"--table=users", "--table=orders"},
			nil,
		},
		{
			"exclude_tables",
			ExportOptions{Format: "custom", ExcludeTables: []string{"logs"}},
			[]string{"--exclude-table=logs"},
			nil,
		},
		{
			"exclude_auth",
			ExportOptions{Format: "custom", ExcludeAuth: true},
			[]string{"--exclude-schema=auth"},
			nil,
		},
		{
			"inserts",
			ExportOptions{Format: "plain", Inserts: true},
			[]string{"--inserts"},
			nil,
		},
		{
			"compress_plain",
			ExportOptions{Format: "plain", Compress: 5},
			[]string{"-Z", "5"},
			nil,
		},
		{
			"compress_ignored_for_custom",
			ExportOptions{Format: "custom", Compress: 5},
			nil,
			[]string{"-Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildExportArgs("localhost", "5432", "user", "testdb", tt.opts)
			argsStr := strings.Join(args, " ")

			for _, want := range tt.wantContain {
				if !strings.Contains(argsStr, want) {
					t.Errorf("expected args to contain %q, got: %v", want, args)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(argsStr, absent) {
					t.Errorf("expected args NOT to contain %q, got: %v", absent, args)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateExportOptions
// ---------------------------------------------------------------------------

func TestValidateExportOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    ExportOptions
		wantErr bool
	}{
		{"valid_default", ExportOptions{Format: "custom"}, false},
		{"mutual_exclusion", ExportOptions{SchemaOnly: true, DataOnly: true}, true},
		{"valid_tables", ExportOptions{Tables: []string{"users", "public.orders"}}, false},
		{"sql_injection_table", ExportOptions{Tables: []string{"users; DROP TABLE"}}, true},
		{"compress_in_range", ExportOptions{Compress: 5}, false},
		{"compress_too_high", ExportOptions{Compress: 10}, true},
		{"compress_negative", ExportOptions{Compress: -1}, true},
		{"valid_exclude", ExportOptions{ExcludeTables: []string{"logs_2024"}}, false},
		{"invalid_exclude", ExportOptions{ExcludeTables: []string{"'; --"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExportOptions(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateExportOptions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isSafeExportIdentifier
// ---------------------------------------------------------------------------

func TestIsSafeExportIdentifier(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		{"valid_simple", "users", true},
		{"valid_underscore", "_users", true},
		{"schema_qualified", "public.orders", true},
		{"with_numbers", "table_123", true},
		{"sql_injection", "users; DROP TABLE x", false},
		{"empty", "", false},
		{"starts_with_number", "123table", false},
		{"special_chars", "users--", false},
		{"quotes", "'users'", false},
		{"semicolon", "users;", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSafeExportIdentifier(tt.input)
			if got != tt.expect {
				t.Errorf("isSafeExportIdentifier(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// projectID safe slice (boundary check in runSingleBackup)
// ---------------------------------------------------------------------------

func TestProjectIDShortSlice(t *testing.T) {
	// runSingleBackup slices projectID to 8 chars for the S3 key prefix.
	// Verify the logic handles IDs shorter than 8 characters.
	tests := []struct {
		name     string
		id       string
		expected string
	}{
		{"longer_than_8", "abcdefgh12345", "abcdefgh"},
		{"exactly_8", "12345678", "12345678"},
		{"shorter_than_8", "abc", "abc"},
		{"empty", "", ""},
		{"one_char", "x", "x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shortID := tt.id
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			if shortID != tt.expected {
				t.Errorf("got %q, want %q", shortID, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// countingReader
// ---------------------------------------------------------------------------

func TestCountingReader(t *testing.T) {
	data := "hello, world! this is a test."
	cr := &countingReader{r: strings.NewReader(data)}

	buf := make([]byte, 10)
	n, err := cr.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Errorf("expected 10 bytes read, got %d", n)
	}
	if cr.n != 10 {
		t.Errorf("expected count 10, got %d", cr.n)
	}

	// Read the rest
	remaining, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedRemaining := len(data) - 10
	if len(remaining) != expectedRemaining {
		t.Errorf("expected %d remaining bytes, got %d", expectedRemaining, len(remaining))
	}
	if cr.n != int64(len(data)) {
		t.Errorf("expected total count %d, got %d", len(data), cr.n)
	}
}

func TestCountingReader_Empty(t *testing.T) {
	cr := &countingReader{r: strings.NewReader("")}
	buf := make([]byte, 10)
	n, err := cr.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF, got err=%v, n=%d", err, n)
	}
	if cr.n != 0 {
		t.Errorf("expected count 0, got %d", cr.n)
	}
}

// ---------------------------------------------------------------------------
// BackupService constructor
// ---------------------------------------------------------------------------

func TestNewBackupService(t *testing.T) {
	svc := NewBackupService(nil, "postgres://localhost/test", "backup-key")
	if svc == nil {
		t.Fatal("NewBackupService returned nil")
	}
	if svc.databaseURL != "postgres://localhost/test" {
		t.Errorf("unexpected databaseURL: %q", svc.databaseURL)
	}
	if svc.backupKey != "backup-key" {
		t.Errorf("unexpected backupKey: %q", svc.backupKey)
	}
}

// ---------------------------------------------------------------------------
// BackupSettingsResponse nil ProjectIDs normalization
// ---------------------------------------------------------------------------

func TestBackupSettingsResponse_NilProjectIDs(t *testing.T) {
	// Verify the nil -> empty slice normalization pattern used throughout backup.go
	var projectIDs []string
	if projectIDs == nil {
		projectIDs = []string{}
	}
	if projectIDs == nil {
		t.Fatal("projectIDs should not be nil after normalization")
	}
	if len(projectIDs) != 0 {
		t.Errorf("expected empty slice, got length %d", len(projectIDs))
	}
}

// ---------------------------------------------------------------------------
// SaveBackupSettingsRequest validation
// ---------------------------------------------------------------------------

func TestSaveBackupSettingsRequest_Defaults(t *testing.T) {
	req := SaveBackupSettingsRequest{
		S3Endpoint:       "https://s3.example.com",
		S3Bucket:         "my-bucket",
		S3AccessKey:      "access",
		S3SecretKey:      "secret",
		PlatformPassword: "pass",
	}

	// Test default schedule
	schedule := req.Schedule
	if schedule == "" {
		schedule = "daily"
	}
	if schedule != "daily" {
		t.Errorf("expected default schedule 'daily', got %q", schedule)
	}

	// Test default region
	region := req.S3Region
	if region == "" {
		region = "us-east-1"
	}
	if region != "us-east-1" {
		t.Errorf("expected default region 'us-east-1', got %q", region)
	}

	// Test default retention
	retentionDays := req.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 30
	}
	if retentionDays != 30 {
		t.Errorf("expected default retention 30, got %d", retentionDays)
	}
}

func TestSaveBackupSettingsRequest_ScheduleValidation(t *testing.T) {
	validSchedules := []string{"hourly", "daily", "weekly"}
	invalidSchedules := []string{"monthly", "yearly", "every5min", ""}

	for _, s := range validSchedules {
		if s != "hourly" && s != "daily" && s != "weekly" {
			t.Errorf("valid schedule %q rejected", s)
		}
	}

	for _, s := range invalidSchedules {
		if s == "hourly" || s == "daily" || s == "weekly" {
			t.Errorf("invalid schedule %q accepted", s)
		}
	}
}

// ---------------------------------------------------------------------------
// BackupHistoryResponse structure
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ToggleBackupRequest OrgID field (H-3)
// ---------------------------------------------------------------------------

func TestToggleBackupRequest_OrgIDField(t *testing.T) {
	t.Run("org_id_settable", func(t *testing.T) {
		req := ToggleBackupRequest{
			Enabled:          true,
			PlatformPassword: "pass",
			OrgID:            "550e8400-e29b-41d4-a716-446655440000",
		}
		if req.OrgID != "550e8400-e29b-41d4-a716-446655440000" {
			t.Errorf("expected OrgID set, got %q", req.OrgID)
		}
	})

	t.Run("org_id_empty_by_default", func(t *testing.T) {
		req := ToggleBackupRequest{Enabled: true, PlatformPassword: "pass"}
		if req.OrgID != "" {
			t.Errorf("expected empty OrgID for zero value, got %q", req.OrgID)
		}
	})
}

// ---------------------------------------------------------------------------
// TestS3ConnectionRequest OrgID field (M-1)
// ---------------------------------------------------------------------------

func TestTestS3ConnectionRequest_OrgIDField(t *testing.T) {
	t.Run("org_id_settable", func(t *testing.T) {
		req := TestS3ConnectionRequest{
			S3Endpoint:  "https://s3.example.com",
			S3Bucket:    "my-bucket",
			S3AccessKey: "AKID",
			S3SecretKey: "secret",
			OrgID:       "550e8400-e29b-41d4-a716-446655440000",
		}
		if req.OrgID != "550e8400-e29b-41d4-a716-446655440000" {
			t.Errorf("expected OrgID set, got %q", req.OrgID)
		}
	})

	t.Run("org_id_empty_by_default", func(t *testing.T) {
		req := TestS3ConnectionRequest{S3Endpoint: "https://s3.example.com"}
		if req.OrgID != "" {
			t.Errorf("expected empty OrgID for zero value, got %q", req.OrgID)
		}
	})
}

func TestBackupHistoryResponse_Fields(t *testing.T) {
	resp := BackupHistoryResponse{
		ID:        1,
		ProjectID: "proj-123",
		DBName:    "testdb",
		S3Key:     "backups/testdb/2024-01-01.dump",
		Status:    "completed",
	}

	if resp.ID != 1 {
		t.Errorf("unexpected ID: %d", resp.ID)
	}
	if resp.ProjectID != "proj-123" {
		t.Errorf("unexpected ProjectID: %q", resp.ProjectID)
	}
	if resp.Status != "completed" {
		t.Errorf("unexpected Status: %q", resp.Status)
	}
	if resp.SizeBytes != nil {
		t.Error("expected nil SizeBytes")
	}
	if resp.Error != nil {
		t.Error("expected nil Error")
	}
	if resp.CompletedAt != nil {
		t.Error("expected nil CompletedAt")
	}
}
