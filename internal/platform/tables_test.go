package platform

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// isSafeIdentifier
// ---------------------------------------------------------------------------

func TestIsSafeIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		// Valid identifiers
		{"simple_name", "users", true},
		{"with_underscore", "my_table", true},
		{"starts_with_underscore", "_private", true},
		{"with_numbers", "table123", true},
		{"single_letter", "a", true},
		{"max_length_63", strings.Repeat("a", 63), true},

		// Invalid identifiers
		{"empty", "", false},
		{"starts_with_number", "1table", false},
		{"has_space", "my table", false},
		{"has_hyphen", "my-table", false},
		{"has_dot", "schema.table", false},
		{"has_semicolon", "table;", false},
		{"sql_injection_1", "'; DROP TABLE users;--", false},
		{"sql_injection_2", "table\" OR 1=1", false},
		{"too_long_64", strings.Repeat("a", 64), false},
		{"unicode_letter", "tablé", false},
		{"has_parentheses", "func()", false},
		{"has_star", "tab*le", false},
		{"only_numbers", "123", false},
		{"starts_with_dollar", "$table", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSafeIdentifier(tt.input)
			if got != tt.valid {
				t.Errorf("isSafeIdentifier(%q) = %v, want %v", tt.input, got, tt.valid)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// quoteIdent (tables.go version)
// ---------------------------------------------------------------------------

func TestTablesQuoteIdent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"normal_name", "users", `"users"`},
		{"with_underscore", "my_table", `"my_table"`},
		{"name_with_double_quotes", `my"table`, `"my""table"`},
		{"empty_string", "", `""`},
		{"reserved_word", "SELECT", `"SELECT"`},
		{"sql_injection", `"; DROP TABLE users;--`, `"""; DROP TABLE users;--"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteIdent(tt.input)
			if got != tt.expected {
				t.Errorf("quoteIdent(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateSchemaTable
// ---------------------------------------------------------------------------

func TestValidateSchemaTable(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		table   string
		wantErr bool
		errMsg  string
	}{
		{"valid_public_users", "public", "users", false, ""},
		{"protected_auth_sessions", "auth", "sessions", true, "access to this schema is not allowed"},
		{"valid_with_underscores", "my_schema", "my_table", false, ""},
		{"invalid_schema_empty", "", "users", true, "invalid schema name"},
		{"invalid_schema_number_start", "1schema", "users", true, "invalid schema name"},
		{"invalid_schema_special_chars", "sch;ema", "users", true, "invalid schema name"},
		{"invalid_table_empty", "public", "", true, "invalid table name"},
		{"invalid_table_number_start", "public", "1table", true, "invalid table name"},
		{"invalid_table_special_chars", "public", "tab;le", true, "invalid table name"},
		{"both_invalid", "1schema", "1table", true, "invalid schema name"},
		{"schema_sql_injection", "'; DROP TABLE", "users", true, "invalid schema name"},
		{"table_sql_injection", "public", "'; DROP TABLE", true, "invalid table name"},
		{"schema_too_long", strings.Repeat("a", 64), "users", true, "invalid schema name"},
		{"table_too_long", "public", strings.Repeat("a", 64), true, "invalid table name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSchemaTable(tt.schema, tt.table)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewTableService constructor
// ---------------------------------------------------------------------------

func TestNewTableService_ReturnsNonNil(t *testing.T) {
	svc := NewTableService(nil, nil)
	if svc == nil {
		t.Fatal("NewTableService returned nil")
	}
}

func TestNewTableService_Fields(t *testing.T) {
	svc := NewTableService(nil, nil)
	if svc.db != nil {
		t.Error("expected nil db")
	}
	if svc.poolManager != nil {
		t.Error("expected nil poolManager")
	}
}

// ---------------------------------------------------------------------------
// safeIdentRegex boundary tests
// ---------------------------------------------------------------------------

func TestSafeIdentRegex_ExactLength63(t *testing.T) {
	ident := strings.Repeat("a", 63)
	if !safeIdentRegex.MatchString(ident) {
		t.Error("63-char identifier should be valid")
	}
}

func TestSafeIdentRegex_Length64(t *testing.T) {
	ident := strings.Repeat("a", 64)
	if safeIdentRegex.MatchString(ident) {
		t.Error("64-char identifier should be invalid")
	}
}

func TestSafeIdentRegex_SingleChar(t *testing.T) {
	if !safeIdentRegex.MatchString("a") {
		t.Error("single letter should be valid")
	}
	if !safeIdentRegex.MatchString("_") {
		t.Error("single underscore should be valid")
	}
	if safeIdentRegex.MatchString("1") {
		t.Error("single digit should be invalid")
	}
}

// ---------------------------------------------------------------------------
// Type construction -- TableInfo, ColumnInfo, TableRowsResponse
// ---------------------------------------------------------------------------

func TestTableInfo_Fields(t *testing.T) {
	ti := TableInfo{
		Schema:      "public",
		Name:        "users",
		ColumnCount: 5,
	}
	if ti.Schema != "public" {
		t.Errorf("expected Schema 'public', got %q", ti.Schema)
	}
	if ti.Name != "users" {
		t.Errorf("expected Name 'users', got %q", ti.Name)
	}
	if ti.ColumnCount != 5 {
		t.Errorf("expected ColumnCount 5, got %d", ti.ColumnCount)
	}
}

func TestColumnInfo_Fields(t *testing.T) {
	defVal := "nextval('users_id_seq'::regclass)"
	maxLen := 255
	ci := ColumnInfo{
		Name:      "id",
		Type:      "integer",
		Nullable:  false,
		Default:   &defVal,
		MaxLength: &maxLen,
	}
	if ci.Name != "id" {
		t.Errorf("expected Name 'id', got %q", ci.Name)
	}
	if ci.Nullable {
		t.Error("expected Nullable false")
	}
	if *ci.Default != defVal {
		t.Errorf("expected Default %q, got %q", defVal, *ci.Default)
	}
	if *ci.MaxLength != 255 {
		t.Errorf("expected MaxLength 255, got %d", *ci.MaxLength)
	}
}

func TestTableRowsResponse_ZeroValue(t *testing.T) {
	var trr TableRowsResponse
	if trr.Columns != nil {
		t.Error("expected nil Columns")
	}
	if trr.Rows != nil {
		t.Error("expected nil Rows")
	}
	if trr.Total != 0 {
		t.Errorf("expected Total 0, got %d", trr.Total)
	}
	if trr.Page != 0 {
		t.Errorf("expected Page 0, got %d", trr.Page)
	}
	if trr.PerPage != 0 {
		t.Errorf("expected PerPage 0, got %d", trr.PerPage)
	}
}
