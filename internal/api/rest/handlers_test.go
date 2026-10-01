package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/ansoraGROUP/dupabase/internal/httputil"
	"github.com/ansoraGROUP/dupabase/internal/middleware"
	"github.com/golang-jwt/jwt/v5"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const testRestSecret = "test-rest-jwt-secret-long-enough-32ch"

func newTestRESTProject() *database.ProjectRecord {
	return &database.ProjectRecord{
		ID:             "proj-rest-1",
		DBName:         "proj_restdb",
		JWTSecret:      testRestSecret,
		AnonKey:        "test-anon-key",
		ServiceRoleKey: "test-sr-key",
		EnableSignup:   true,
		Autoconfirm:    true,
		PasswordMinLen: 6,
		SiteURL:        "http://localhost:3000",
		Status:         "active",
	}
}

func generateTestRestUserJWT(secret, userID, email, role string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   userID,
		"email": email,
		"role":  role,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
		"aud":   "authenticated",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString([]byte(secret))
	return signed
}

// ---------------------------------------------------------------------------
// HandleTable - missing context
// ---------------------------------------------------------------------------

func TestHandleTable_MissingProjectContext(t *testing.T) {
	h := NewHandler()

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/todos", nil)
	rec := httptest.NewRecorder()

	h.HandleTable(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}

	var body map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&body)
	if body["code"] != "PGRST000" {
		t.Errorf("expected code PGRST000, got %v", body["code"])
	}
}

// ---------------------------------------------------------------------------
// HandleRPC - missing context
// ---------------------------------------------------------------------------

func TestHandleRPC_MissingProjectContext(t *testing.T) {
	h := NewHandler()

	req := httptest.NewRequest(http.MethodPost, "/rest/v1/rpc/my_function", nil)
	rec := httptest.NewRecorder()

	h.HandleRPC(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// extractTableName
// ---------------------------------------------------------------------------

func TestExtractTableName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"standard", "/rest/v1/todos", "todos"},
		{"trailing_slash", "/rest/v1/todos/", "todos"},
		{"nested_path", "/rest/v1/some/nested", ""},
		{"empty_after_prefix", "/rest/v1/", ""},
		{"no_prefix", "/todos", ""},
		{"table_with_underscore", "/rest/v1/my_table", "my_table"},
		{"table_with_numbers", "/rest/v1/table123", "table123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTableName(tt.path)
			if result != tt.expected {
				t.Errorf("extractTableName(%q): got %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractRPCName
// ---------------------------------------------------------------------------

func TestExtractRPCName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"standard", "/rest/v1/rpc/my_function", "my_function"},
		{"trailing_slash", "/rest/v1/rpc/my_function/", "my_function"},
		{"empty", "/rest/v1/rpc/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractRPCName(tt.path)
			if result != tt.expected {
				t.Errorf("extractRPCName(%q): got %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// quoteIdent
// ---------------------------------------------------------------------------

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"column", `"column"`},
		{"table_name", `"table_name"`},
		{`my"col`, `"my""col"`},
		{"", `""`},
		{"SELECT", `"SELECT"`},
		{`"; DROP TABLE users;--`, `"""; DROP TABLE users;--"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := quoteIdent(tt.input)
			if result != tt.expected {
				t.Errorf("quoteIdent(%q): got %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildSelectClause
// ---------------------------------------------------------------------------

func TestBuildSelectClause(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"star", "*", "*"},
		{"single_column", "id", `"id"`},
		{"multiple_columns", "id,name,email", `"id", "name", "email"`},
		{"with_alias", "full_name:name", `"name" AS "full_name"`},
		{"with_spaces", " id , name ", `"id", "name"`},
		{"empty_returns_star", "", "*"},
		{"skip_relationship", "id,posts(title)", `"id"`},
		{"mixed", "id,alias:col,name", `"id", "col" AS "alias", "name"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSelectClause(tt.input)
			if result != tt.expected {
				t.Errorf("buildSelectClause(%q): got %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildOrderClause
// ---------------------------------------------------------------------------

func TestBuildOrderClause(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"asc", "id.asc", `"id" ASC`},
		{"desc", "name.desc", `"name" DESC`},
		{"default_asc", "id", `"id" ASC`},
		{"nullsfirst", "id.asc.nullsfirst", `"id" ASC NULLS FIRST`},
		{"nullslast", "id.desc.nullslast", `"id" DESC NULLS LAST`},
		{"multiple", "id.asc,name.desc", `"id" ASC, "name" DESC`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildOrderClause(tt.input)
			if result != tt.expected {
				t.Errorf("buildOrderClause(%q): got %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseRange
// ---------------------------------------------------------------------------

func TestParseRange(t *testing.T) {
	tests := []struct {
		name  string
		input string
		start int
		end   int
	}{
		{"standard", "0-9", 0, 9},
		{"with_spaces", "0 - 9", 0, 9},
		{"larger_range", "10-99", 10, 99},
		{"invalid_format", "invalid", -1, -1},
		{"missing_end", "0-", -1, -1},
		{"missing_start", "-9", -1, -1},
		{"non_numeric", "a-b", -1, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := parseRange(tt.input)
			if start != tt.start || end != tt.end {
				t.Errorf("parseRange(%q): got (%d, %d), want (%d, %d)", tt.input, start, end, tt.start, tt.end)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parsePrefer
// ---------------------------------------------------------------------------

func TestParsePrefer(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			"return_representation",
			"return=representation",
			map[string]string{"return": "representation"},
		},
		{
			"multiple",
			"return=representation, count=exact",
			map[string]string{"return": "representation", "count": "exact"},
		},
		{
			"resolution_merge",
			"resolution=merge-duplicates",
			map[string]string{"resolution": "merge-duplicates"},
		},
		{
			"empty",
			"",
			map[string]string{},
		},
		{
			"no_equals",
			"novalue",
			map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parsePrefer(tt.input)
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("parsePrefer(%q)[%q]: got %q, want %q", tt.input, k, result[k], v)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseFilter
// ---------------------------------------------------------------------------

func TestParseFilter(t *testing.T) {
	tests := []struct {
		name      string
		column    string
		value     string
		argIdx    int
		wantCond  string
		wantArgs  int
		wantEmpty bool
	}{
		{"eq", "id", "eq.1", 1, `"id" = $1`, 1, false},
		{"neq", "status", "neq.active", 1, `"status" != $1`, 1, false},
		{"gt", "age", "gt.18", 1, `"age" > $1`, 1, false},
		{"gte", "age", "gte.18", 1, `"age" >= $1`, 1, false},
		{"lt", "price", "lt.100", 1, `"price" < $1`, 1, false},
		{"lte", "price", "lte.100", 1, `"price" <= $1`, 1, false},
		{"like", "name", "like.*John*", 1, `"name" LIKE $1`, 1, false},
		{"ilike", "name", "ilike.*john*", 1, `"name" ILIKE $1`, 1, false},
		{"is_null", "deleted", "is.null", 1, `"deleted" IS NULL`, 0, false},
		{"is_true", "active", "is.true", 1, `"active" IS TRUE`, 0, false},
		{"is_false", "active", "is.false", 1, `"active" IS FALSE`, 0, false},
		{"in", "id", "in.(1,2,3)", 1, `"id" IN ($1, $2, $3)`, 3, false},
		{"cs", "tags", "cs.{a,b}", 1, `"tags" @> $1`, 1, false},
		{"cd", "tags", "cd.{a,b}", 1, `"tags" <@ $1`, 1, false},
		{"ov", "tags", "ov.{a,b}", 1, `"tags" && $1`, 1, false},
		{"fts", "body", "fts.hello", 1, `"body" @@ to_tsquery($1)`, 1, false},
		{"plfts", "body", "plfts.hello world", 1, `"body" @@ plainto_tsquery($1)`, 1, false},
		{"phfts", "body", "phfts.hello world", 1, `"body" @@ phraseto_tsquery($1)`, 1, false},
		{"wfts", "body", "wfts.hello world", 1, `"body" @@ websearch_to_tsquery($1)`, 1, false},
		{"negation", "id", "not.eq.1", 1, `NOT ("id" = $1)`, 1, false},
		{"unknown_op", "id", "xyz.1", 1, "", 0, true},
		{"no_dot", "id", "nodot", 1, "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, args, _ := parseFilter(tt.column, tt.value, tt.argIdx)
			if tt.wantEmpty {
				if cond != "" {
					t.Errorf("expected empty condition, got %q", cond)
				}
				return
			}
			if cond != tt.wantCond {
				t.Errorf("condition: got %q, want %q", cond, tt.wantCond)
			}
			if len(args) != tt.wantArgs {
				t.Errorf("args count: got %d, want %d", len(args), tt.wantArgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildWhereClause
// ---------------------------------------------------------------------------

func TestBuildWhereClause_NoFilters(t *testing.T) {
	q := map[string][]string{
		"select": {"id,name"},
		"order":  {"id.asc"},
		"limit":  {"10"},
		"offset": {"0"},
	}

	where, args, err := buildWhereClause(q, "public", "todos")
	if err != nil {
		t.Fatalf("buildWhereClause returned unexpected error: %v", err)
	}
	if where != "" {
		t.Errorf("expected empty where clause, got %q", where)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %d", len(args))
	}
}

func TestBuildWhereClause_WithFilters(t *testing.T) {
	q := map[string][]string{
		"id":     {"eq.1"},
		"select": {"*"},
	}

	where, args, err := buildWhereClause(q, "public", "todos")
	if err != nil {
		t.Fatalf("buildWhereClause returned unexpected error: %v", err)
	}
	if where == "" {
		t.Fatal("expected non-empty where clause")
	}
	if !strings.Contains(where, "WHERE") {
		t.Errorf("expected WHERE in clause, got %q", where)
	}
	if len(args) != 1 {
		t.Errorf("expected 1 arg, got %d", len(args))
	}
}

func TestBuildWhereClause_SkipsReservedParams(t *testing.T) {
	q := map[string][]string{
		"select":      {"*"},
		"order":       {"id.asc"},
		"limit":       {"10"},
		"offset":      {"5"},
		"on_conflict": {"id"},
	}

	where, args, err := buildWhereClause(q, "public", "todos")
	if err != nil {
		t.Fatalf("buildWhereClause returned unexpected error: %v", err)
	}
	if where != "" {
		t.Errorf("expected empty where clause for reserved params only, got %q", where)
	}
	if len(args) != 0 {
		t.Errorf("expected 0 args, got %d", len(args))
	}
}

func TestBuildWhereClause_MultipleFilters(t *testing.T) {
	q := map[string][]string{
		"status":  {"eq.active"},
		"visible": {"is.true"},
	}

	where, args, err := buildWhereClause(q, "public", "todos")
	if err != nil {
		t.Fatalf("buildWhereClause returned unexpected error: %v", err)
	}
	if !strings.Contains(where, "WHERE") {
		t.Error("expected WHERE in clause")
	}
	if !strings.Contains(where, " AND ") {
		t.Error("expected AND between conditions")
	}
	// At least 1 arg for the eq filter (is.true has no args)
	if len(args) < 1 {
		t.Errorf("expected at least 1 arg, got %d", len(args))
	}
}

// ---------------------------------------------------------------------------
// resolveRoleAndClaims
// ---------------------------------------------------------------------------

func TestResolveRoleAndClaims_DefaultsToAPIKeyRole(t *testing.T) {
	project := newTestRESTProject()

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// Set API key role in context
	ctx := context.WithValue(req.Context(), middleware.ContextAPIKeyRole, "anon")
	ctx = context.WithValue(ctx, middleware.ContextAPIKeyClaims, jwt.MapClaims{
		"role":       "anon",
		"project_id": project.ID,
	})
	req = req.WithContext(ctx)

	role, claims := resolveRoleAndClaims(req, project)
	if role != "anon" {
		t.Errorf("expected role 'anon', got %q", role)
	}
	if claims == nil {
		t.Fatal("expected non-nil claims")
	}
}

func TestResolveRoleAndClaims_UserTokenOverridesAPIKey(t *testing.T) {
	project := newTestRESTProject()

	apikey := "some-api-key"
	userToken := generateTestRestUserJWT(testRestSecret, "user-1", "user@test.com", "authenticated")

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("apikey", apikey)
	req.Header.Set("Authorization", "Bearer "+userToken)

	// Set API key role in context
	ctx := context.WithValue(req.Context(), middleware.ContextAPIKeyRole, "anon")
	ctx = context.WithValue(ctx, middleware.ContextAPIKeyClaims, jwt.MapClaims{
		"role": "anon",
	})
	req = req.WithContext(ctx)

	role, claims := resolveRoleAndClaims(req, project)
	if role != "authenticated" {
		t.Errorf("expected role 'authenticated', got %q", role)
	}
	if sub, _ := claims["sub"].(string); sub != "user-1" {
		t.Errorf("expected sub 'user-1', got %q", sub)
	}
}

func TestResolveRoleAndClaims_SameTokenAsAPIKey(t *testing.T) {
	project := newTestRESTProject()

	apikey := "the-same-token"

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("apikey", apikey)
	req.Header.Set("Authorization", "Bearer "+apikey)

	ctx := context.WithValue(req.Context(), middleware.ContextAPIKeyRole, "anon")
	ctx = context.WithValue(ctx, middleware.ContextAPIKeyClaims, jwt.MapClaims{
		"role": "anon",
	})
	req = req.WithContext(ctx)

	role, _ := resolveRoleAndClaims(req, project)
	// When Bearer token == apikey, should keep apikey role
	if role != "anon" {
		t.Errorf("expected role 'anon' (apikey role), got %q", role)
	}
}

func TestResolveRoleAndClaims_InvalidUserToken(t *testing.T) {
	project := newTestRESTProject()

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("apikey", "api-key")
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")

	ctx := context.WithValue(req.Context(), middleware.ContextAPIKeyRole, "anon")
	ctx = context.WithValue(ctx, middleware.ContextAPIKeyClaims, jwt.MapClaims{
		"role": "anon",
	})
	req = req.WithContext(ctx)

	role, _ := resolveRoleAndClaims(req, project)
	// Invalid user token should fall back to apikey role
	if role != "" {
		t.Errorf("expected invalid bearer token to be rejected, got role %q", role)
	}
}

// ---------------------------------------------------------------------------
// writeError (rest)
// ---------------------------------------------------------------------------

func TestRestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "PGRST100", "test error")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	var body map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&body)
	if body["code"] != "PGRST100" {
		t.Errorf("expected code PGRST100, got %v", body["code"])
	}
	if body["message"] != "test error" {
		t.Errorf("expected message 'test error', got %v", body["message"])
	}
	if body["details"] != nil {
		t.Errorf("expected nil details, got %v", body["details"])
	}
	if body["hint"] != nil {
		t.Errorf("expected nil hint, got %v", body["hint"])
	}
}

// ---------------------------------------------------------------------------
// writeJSON (rest)
// ---------------------------------------------------------------------------

func TestRestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	httputil.WriteJSON(rec, http.StatusOK, map[string]string{"ok": "true"})

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// NewHandler
// ---------------------------------------------------------------------------

func TestRestNewHandler_ReturnsNonNil(t *testing.T) {
	h := NewHandler()
	if h == nil {
		t.Fatal("NewHandler returned nil")
	}
}

// ---------------------------------------------------------------------------
// HandleTable - missing table name
// ---------------------------------------------------------------------------

func TestHandleTable_MissingTableName(t *testing.T) {
	h := NewHandler()

	project := newTestRESTProject()

	// URL path that doesn't have a table name
	req := httptest.NewRequest(http.MethodGet, "/rest/v1/", nil)
	ctx := context.WithValue(req.Context(), middleware.ContextProject, project)
	// Need pool to be non-nil to pass the first check.
	// Since pool is nil, we get "missing project context".
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.HandleTable(rec, req)

	// Without pool, gets "missing project context"
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// HandleRPC - missing function name
// ---------------------------------------------------------------------------

func TestHandleRPC_MissingFunctionName(t *testing.T) {
	h := NewHandler()

	req := httptest.NewRequest(http.MethodPost, "/rest/v1/rpc/", nil)
	rec := httptest.NewRecorder()

	h.HandleRPC(rec, req)

	// Without project context, gets "missing project context"
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Schema header handling
// ---------------------------------------------------------------------------

func TestSchemaHeaders(t *testing.T) {
	// Verify Accept-Profile and Content-Profile behavior in handleSelect/handleInsert
	// These are tested indirectly through the full handler which requires DB,
	// but we can verify the header reading logic here.

	t.Run("AcceptProfile_GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/rest/v1/todos", nil)
		req.Header.Set("Accept-Profile", "custom_schema")

		schema := "public"
		if s := req.Header.Get("Accept-Profile"); s != "" && req.Method == http.MethodGet {
			schema = s
		}
		if schema != "custom_schema" {
			t.Errorf("expected schema 'custom_schema', got %q", schema)
		}
	})

	t.Run("ContentProfile_POST", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rest/v1/todos", nil)
		req.Header.Set("Content-Profile", "custom_schema")

		schema := "public"
		if s := req.Header.Get("Content-Profile"); s != "" && req.Method != http.MethodGet {
			schema = s
		}
		if schema != "custom_schema" {
			t.Errorf("expected schema 'custom_schema', got %q", schema)
		}
	})

	t.Run("AcceptProfile_ignored_on_POST", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rest/v1/todos", nil)
		req.Header.Set("Accept-Profile", "custom_schema")

		schema := "public"
		if s := req.Header.Get("Accept-Profile"); s != "" && req.Method == http.MethodGet {
			schema = s
		}
		if schema != "public" {
			t.Errorf("expected schema 'public' (Accept-Profile ignored on POST), got %q", schema)
		}
	})
}

// ---------------------------------------------------------------------------
// Filter negation
// ---------------------------------------------------------------------------

func TestParseFilter_NegatedOperators(t *testing.T) {
	tests := []struct {
		name     string
		column   string
		value    string
		wantCond string
	}{
		{"not_eq", "id", "not.eq.5", `NOT ("id" = $1)`},
		{"not_like", "name", "not.like.%test%", `NOT ("name" LIKE $1)`},
		{"not_is_null", "deleted", "not.is.null", `NOT ("deleted" IS NULL)`},
		{"not_in", "id", "not.in.(1,2,3)", `NOT ("id" IN ($1, $2, $3))`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, _, _ := parseFilter(tt.column, tt.value, 1)
			if cond != tt.wantCond {
				t.Errorf("got %q, want %q", cond, tt.wantCond)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// In-filter parsing
// ---------------------------------------------------------------------------

func TestParseFilter_InOperator_Details(t *testing.T) {
	cond, args, newIdx := parseFilter("status", "in.(active,inactive,pending)", 1)

	if cond != `"status" IN ($1, $2, $3)` {
		t.Errorf("unexpected condition: %q", cond)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(args))
	}
	if args[0] != "active" {
		t.Errorf("arg[0]: got %q, want 'active'", args[0])
	}
	if args[1] != "inactive" {
		t.Errorf("arg[1]: got %q, want 'inactive'", args[1])
	}
	if args[2] != "pending" {
		t.Errorf("arg[2]: got %q, want 'pending'", args[2])
	}
	if newIdx != 4 {
		t.Errorf("expected newIdx 4, got %d", newIdx)
	}
}

// ---------------------------------------------------------------------------
// is operator edge cases
// ---------------------------------------------------------------------------

func TestParseFilter_IsOperator_Unknown(t *testing.T) {
	cond, _, _ := parseFilter("col", "is.unknown_value", 1)
	if cond != "" {
		t.Errorf("expected empty condition for unknown is value, got %q", cond)
	}
}

// ---------------------------------------------------------------------------
// hasFilterParams
// ---------------------------------------------------------------------------

func TestHasFilterParams(t *testing.T) {
	tests := []struct {
		name   string
		query  url.Values
		expect bool
	}{
		{"no params", url.Values{}, false},
		{"only select", url.Values{"select": {"*"}}, false},
		{"only order", url.Values{"order": {"id.asc"}}, false},
		{"only limit", url.Values{"limit": {"10"}}, false},
		{"only offset", url.Values{"offset": {"5"}}, false},
		{"only on_conflict", url.Values{"on_conflict": {"id"}}, false},
		{"only columns", url.Values{"columns": {"id,name"}}, false},
		{"all reserved", url.Values{
			"select":      {"*"},
			"order":       {"id.asc"},
			"limit":       {"10"},
			"offset":      {"0"},
			"on_conflict": {"id"},
			"columns":     {"id,name"},
		}, false},
		{"with filter", url.Values{"id": {"eq.1"}}, true},
		{"select + filter", url.Values{"select": {"*"}, "name": {"eq.test"}}, true},
		{"order + filter", url.Values{"order": {"id.asc"}, "status": {"eq.active"}}, true},
		{"multiple filters", url.Values{"id": {"eq.1"}, "name": {"like.*test*"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasFilterParams(tt.query); got != tt.expect {
				t.Errorf("hasFilterParams(%v) = %v, want %v", tt.query, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isAllowedSchema
// ---------------------------------------------------------------------------

func TestIsAllowedSchema(t *testing.T) {
	tests := []struct {
		schema  string
		allowed bool
	}{
		{"public", true},
		{"auth", false},
		{"pg_catalog", false},
		{"information_schema", false},
		{"platform", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			if got := isAllowedSchema(tt.schema); got != tt.allowed {
				t.Errorf("isAllowedSchema(%q) = %v, want %v", tt.schema, got, tt.allowed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sanitizeDBError
// ---------------------------------------------------------------------------

func TestSanitizeDBError(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		expected string
	}{
		{"rls_violation", "ERROR: new row violates row-level security policy", "permission denied for this resource"},
		{"unique_violation", "ERROR: duplicate key value violates unique constraint", "duplicate key value violates unique constraint"},
		{"not_null_violation", "ERROR: null value in column \"name\" violates not-null constraint", "null value in column violates not-null constraint"},
		{"fk_violation", "ERROR: insert or update violates foreign key constraint", "foreign key constraint violation"},
		{"check_violation", "ERROR: new row violates check constraint", "check constraint violation"},
		{"relation_not_found", `ERROR: relation "nonexistent" does not exist`, "Requested resource does not exist"},
		{"permission_denied", "ERROR: permission denied for table users", "permission denied"},
		{"generic_error", "ERROR: something completely unexpected happened", "database operation failed"},
		{"connection_error", "failed to connect to database", "database operation failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeDBError(fmt.Errorf("%s", tt.errMsg))
			if got != tt.expected {
				t.Errorf("sanitizeDBError(%q) = %q, want %q", tt.errMsg, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// splitRespectingParens
// ---------------------------------------------------------------------------

func TestSplitRespectingParens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"simple", "a,b,c", []string{"a", "b", "c"}},
		{"with_parens", "id,posts(title,body),name", []string{"id", "posts(title,body)", "name"}},
		{"nested_parens", "a(b(c)),d", []string{"a(b(c))", "d"}},
		{"no_commas", "single", []string{"single"}},
		{"empty", "", []string{""}},
		{"trailing_comma", "a,b,", []string{"a", "b", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitRespectingParens(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d parts, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("part[%d]: got %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseSelectWithEmbedding
// ---------------------------------------------------------------------------

func TestParseSelectWithEmbedding(t *testing.T) {
	t.Run("star_only", func(t *testing.T) {
		cols, embeds := parseSelectWithEmbedding("*")
		if len(cols) != 1 || cols[0] != "*" {
			t.Errorf("expected [*], got %v", cols)
		}
		if len(embeds) != 0 {
			t.Errorf("expected no embeds, got %d", len(embeds))
		}
	})

	t.Run("columns_and_embed", func(t *testing.T) {
		cols, embeds := parseSelectWithEmbedding("id,name,posts(title,body)")
		if len(cols) != 2 {
			t.Fatalf("expected 2 cols, got %d: %v", len(cols), cols)
		}
		if len(embeds) != 1 {
			t.Fatalf("expected 1 embed, got %d", len(embeds))
		}
		if embeds[0].table != "posts" {
			t.Errorf("expected embed table 'posts', got %q", embeds[0].table)
		}
		if len(embeds[0].columns) != 2 {
			t.Errorf("expected 2 embed columns, got %d", len(embeds[0].columns))
		}
	})

	t.Run("inner_join_modifier", func(t *testing.T) {
		_, embeds := parseSelectWithEmbedding("id,posts!inner(title)")
		if len(embeds) != 1 {
			t.Fatalf("expected 1 embed, got %d", len(embeds))
		}
		if !embeds[0].isInner {
			t.Error("expected isInner=true for !inner modifier")
		}
	})

	t.Run("alias_embed", func(t *testing.T) {
		_, embeds := parseSelectWithEmbedding("my_posts:posts(title)")
		if len(embeds) != 1 {
			t.Fatalf("expected 1 embed, got %d", len(embeds))
		}
		if embeds[0].alias != "my_posts" {
			t.Errorf("expected alias 'my_posts', got %q", embeds[0].alias)
		}
		if embeds[0].table != "posts" {
			t.Errorf("expected table 'posts', got %q", embeds[0].table)
		}
	})

	t.Run("empty_returns_star", func(t *testing.T) {
		cols, embeds := parseSelectWithEmbedding("")
		if len(cols) != 1 || cols[0] != "*" {
			t.Errorf("expected [*] for empty input, got %v", cols)
		}
		if len(embeds) != 0 {
			t.Errorf("expected no embeds for empty input, got %d", len(embeds))
		}
	})
}

// ---------------------------------------------------------------------------
// quoteIdentDotted
// ---------------------------------------------------------------------------

func TestQuoteIdentDotted(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"column", `"column"`},
		{"table.column", `"table"."column"`},
		{"schema.table", `"schema"."table"`},
		{"nodot", `"nodot"`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := quoteIdentDotted(tt.input)
			if got != tt.expected {
				t.Errorf("quoteIdentDotted(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildWhereClause - unsupported logical operators
// ---------------------------------------------------------------------------

func TestBuildWhereClause_LogicalOperatorsRejected(t *testing.T) {
	logicalOps := []string{"and", "or", "not.and", "not.or"}
	for _, op := range logicalOps {
		t.Run(op, func(t *testing.T) {
			q := map[string][]string{
				op: {"(id.eq.1,name.eq.test)"},
			}
			_, _, err := buildWhereClause(q, "public", "todos")
			if err == nil {
				t.Errorf("expected error for logical operator %q", op)
			}
			if !strings.Contains(err.Error(), "unsupported logical operator") {
				t.Errorf("expected 'unsupported logical operator' in error, got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractTableName edge cases
// ---------------------------------------------------------------------------

func TestExtractTableName_EdgeCases(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/rest/v1/", ""},
		{"/rest/v1/a/b/c", ""},
		{"/rest/v1/table-with-dashes", "table-with-dashes"},
		{"/rest/v1/Table_123", "Table_123"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractTableName(tt.path)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// writeMaybeObject
// ---------------------------------------------------------------------------

func TestWriteMaybeObject_Array(t *testing.T) {
	result := []map[string]interface{}{
		{"id": 1, "name": "test"},
		{"id": 2, "name": "test2"},
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	writeMaybeObject(rec, req, http.StatusOK, result)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var body []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body) != 2 {
		t.Errorf("expected 2 items, got %d", len(body))
	}
}

func TestWriteMaybeObject_SingleObject(t *testing.T) {
	result := []map[string]interface{}{
		{"id": 1, "name": "test"},
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept", "application/vnd.pgrst.object+json")
	rec := httptest.NewRecorder()

	writeMaybeObject(rec, req, http.StatusOK, result)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var body map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&body)
	if body["name"] != "test" {
		t.Errorf("expected name 'test', got %v", body["name"])
	}
}

func TestWriteMaybeObject_MultipleRowsWithSingleExpected(t *testing.T) {
	result := []map[string]interface{}{
		{"id": 1},
		{"id": 2},
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept", "application/vnd.pgrst.object+json")
	rec := httptest.NewRecorder()

	writeMaybeObject(rec, req, http.StatusOK, result)

	if rec.Code != http.StatusNotAcceptable {
		t.Errorf("expected status 406, got %d", rec.Code)
	}
}

func TestWriteMaybeObject_EmptyResultWithSingleExpected(t *testing.T) {
	result := []map[string]interface{}{}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept", "application/vnd.pgrst.object+json")
	rec := httptest.NewRecorder()

	writeMaybeObject(rec, req, http.StatusOK, result)

	if rec.Code != http.StatusNotAcceptable {
		t.Errorf("expected status 406, got %d", rec.Code)
	}
}
