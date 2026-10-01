package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sanitizeValue converts pgx native types (e.g. [16]byte UUIDs, net.IPNet) into
// JSON-friendly representations so encoding/json doesn't produce byte arrays.
func sanitizeValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case [16]byte:
		// UUID bytes → formatted UUID string
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			val[0:4], val[4:6], val[6:8], val[8:10], val[10:16])
	case time.Time:
		return val.Format(time.RFC3339Nano)
	case []byte:
		// Try to return as string if it's valid UTF-8-ish, else hex
		return string(val)
	case json.RawMessage:
		var parsed interface{}
		if err := json.Unmarshal(val, &parsed); err == nil {
			return parsed
		}
		return string(val)
	case map[string]interface{}:
		return val
	default:
		// Let fmt.Stringer types render as strings
		if s, ok := v.(fmt.Stringer); ok {
			return s.String()
		}
		return v
	}
}

// safeIdentRegex validates SQL identifiers to prevent injection.
// Allows letters, digits, and underscores; must start with a letter or underscore; max 63 chars.
var safeIdentRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// isSafeIdentifier returns true if s is a safe SQL identifier.
func isSafeIdentifier(s string) bool {
	return safeIdentRegex.MatchString(s)
}

// quoteIdent double-quotes a SQL identifier, escaping embedded double quotes.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// TableService provides table browsing operations for project databases.
type TableService struct {
	db          *pgxpool.Pool
	poolManager *database.PoolManager
}

// NewTableService creates a new TableService.
func NewTableService(db *pgxpool.Pool, pm *database.PoolManager) *TableService {
	return &TableService{db: db, poolManager: pm}
}

// --- Types ---

// TableInfo represents a database table with basic metadata.
type TableInfo struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	ColumnCount int    `json:"column_count"`
}

// ColumnInfo represents a column in a database table.
type ColumnInfo struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Nullable   bool    `json:"nullable"`
	Default    *string `json:"default"`
	MaxLength  *int    `json:"max_length,omitempty"`
	Precision  *int    `json:"precision,omitempty"`
	PrimaryKey bool    `json:"primary_key"`
	Unique     bool    `json:"unique"`
}

// TableRowsResponse contains paginated row data for a table.
type TableRowsResponse struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	Total   int64           `json:"total"`
	Page    int             `json:"page"`
	PerPage int             `json:"per_page"`
}

// --- Helpers ---

// getProjectPool gets a connection pool for the project via pool manager.
func (s *TableService) getProjectPool(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	pool, err := s.poolManager.GetPool(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get project pool: %w", err)
	}
	return pool, nil
}

// validateSchemaTable validates schema and table name identifiers.
func validateSchemaTable(schema, table string) error {
	if strings.HasPrefix(strings.ToLower(schema), "pg_") || schema == "auth" || schema == "platform" || schema == "information_schema" || schema == "extensions" {
		return fmt.Errorf("access to this schema is not allowed")
	}
	if !isSafeIdentifier(schema) {
		return fmt.Errorf("invalid schema name")
	}
	if !isSafeIdentifier(table) {
		return fmt.Errorf("invalid table name")
	}
	return nil
}

// --- Methods ---

// ListTables returns all user tables for a project database.
func (s *TableService) ListTables(ctx context.Context, projectID string) ([]TableInfo, int, error) {
	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	rows, err := pool.Query(ctx, `
		SELECT table_schema, table_name,
			(SELECT COUNT(*) FROM information_schema.columns c
			 WHERE c.table_schema = t.table_schema AND c.table_name = t.table_name) as column_count
		FROM information_schema.tables t
		WHERE table_schema NOT IN ('pg_catalog', 'information_schema', 'platform', 'auth', 'extensions')
			AND table_schema NOT LIKE 'pg_%'
			AND table_type = 'BASE TABLE'
		ORDER BY table_schema, table_name
	`)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Schema, &t.Name, &t.ColumnCount); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan table: %w", err)
		}
		tables = append(tables, t)
	}

	if tables == nil {
		tables = []TableInfo{}
	}

	return tables, http.StatusOK, nil
}

// GetTableColumns returns column metadata for a specific table.
func (s *TableService) GetTableColumns(ctx context.Context, projectID, schema, tableName string) ([]ColumnInfo, int, error) {
	if err := validateSchemaTable(schema, tableName); err != nil {
		return nil, http.StatusBadRequest, err
	}

	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default,
			character_maximum_length, numeric_precision,
			EXISTS(SELECT 1 FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_attribute a ON a.attrelid=t.oid AND a.attname=c.column_name WHERE n.nspname=$1 AND t.relname=$2 AND i.indisprimary AND a.attnum=ANY(i.indkey)) AS primary_key,
			EXISTS(SELECT 1 FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_attribute a ON a.attrelid=t.oid AND a.attname=c.column_name WHERE n.nspname=$1 AND t.relname=$2 AND i.indisunique AND i.indisvalid AND i.indnkeyatts=1 AND i.indpred IS NULL AND i.indexprs IS NULL AND i.indkey[0]=a.attnum) AS unique
		FROM information_schema.columns c
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position
	`, schema, tableName)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query columns: %w", err)
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var c ColumnInfo
		var isNullable string
		if err := rows.Scan(&c.Name, &c.Type, &isNullable, &c.Default, &c.MaxLength, &c.Precision, &c.PrimaryKey, &c.Unique); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan column: %w", err)
		}
		c.Nullable = isNullable == "YES"
		columns = append(columns, c)
	}

	if columns == nil {
		columns = []ColumnInfo{}
	}

	return columns, http.StatusOK, nil
}

// GetTableRows returns paginated row data for a specific table.
func (s *TableService) GetTableRows(ctx context.Context, projectID, schema, tableName string, page, perPage int, orderBy, orderDir string) (*TableRowsResponse, int, error) {
	if err := validateSchemaTable(schema, tableName); err != nil {
		return nil, http.StatusBadRequest, err
	}

	// Defaults and limits
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 50
	}
	if perPage > 100 {
		perPage = 100
	}

	// Validate orderDir
	orderDir = strings.ToUpper(strings.TrimSpace(orderDir))
	if orderDir != "DESC" {
		orderDir = "ASC"
	}

	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	qualifiedTable := quoteIdent(schema) + "." + quoteIdent(tableName)

	// Count total rows
	var total int64
	err = pool.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, qualifiedTable)).Scan(&total)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("count rows: %w", err)
	}

	// Build ORDER BY clause
	orderClause := ""
	if orderBy != "" {
		if !isSafeIdentifier(orderBy) {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid order_by column name")
		}
		orderClause = fmt.Sprintf(" ORDER BY %s %s", quoteIdent(orderBy), orderDir)
	}

	offset := (page - 1) * perPage
	query := fmt.Sprintf(`SELECT * FROM %s%s LIMIT $1 OFFSET $2`, qualifiedTable, orderClause)

	rows, err := pool.Query(ctx, query, perPage, offset)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()

	// Get column names from field descriptions
	fields := rows.FieldDescriptions()
	columnNames := make([]string, len(fields))
	for i, f := range fields {
		columnNames[i] = string(f.Name)
	}

	// Collect rows
	var resultRows [][]interface{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan row: %w", err)
		}
		// Sanitize values for JSON serialization (e.g. UUID [16]byte → string)
		for i, v := range values {
			values[i] = sanitizeValue(v)
		}
		resultRows = append(resultRows, values)
	}

	if resultRows == nil {
		resultRows = [][]interface{}{}
	}

	return &TableRowsResponse{
		Columns: columnNames,
		Rows:    resultRows,
		Total:   total,
		Page:    page,
		PerPage: perPage,
	}, http.StatusOK, nil
}

// InsertRow inserts a new row into a table and returns the inserted row.
func (s *TableService) InsertRow(ctx context.Context, projectID, schema, tableName string, data map[string]interface{}) (map[string]interface{}, int, error) {
	if err := validateSchemaTable(schema, tableName); err != nil {
		return nil, http.StatusBadRequest, err
	}
	if len(data) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("no data provided")
	}

	// Validate all column names
	for col := range data {
		if !isSafeIdentifier(col) {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid column name: %s", col)
		}
	}

	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	qualifiedTable := quoteIdent(schema) + "." + quoteIdent(tableName)

	// Build INSERT query
	columns := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	i := 1
	for col, val := range data {
		columns = append(columns, quoteIdent(col))
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		values = append(values, val)
		i++
	}

	query := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING *`,
		qualifiedTable,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	rows, err := pool.Query(ctx, query, values...)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("insert row: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, http.StatusInternalServerError, fmt.Errorf("no row returned after insert")
	}

	// Build result map from field descriptions and values
	fields := rows.FieldDescriptions()
	rowValues, err := rows.Values()
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("scan inserted row: %w", err)
	}

	result := make(map[string]interface{}, len(fields))
	for idx, f := range fields {
		result[string(f.Name)] = rowValues[idx]
	}

	return result, http.StatusCreated, nil
}

// UpdateRow updates a row identified by primary key column and value.
func (s *TableService) UpdateRow(ctx context.Context, projectID, schema, tableName string, pkColumn, pkValue string, data map[string]interface{}) (int, error) {
	if err := validateSchemaTable(schema, tableName); err != nil {
		return http.StatusBadRequest, err
	}
	if !isSafeIdentifier(pkColumn) {
		return http.StatusBadRequest, fmt.Errorf("invalid primary key column name")
	}
	if len(data) == 0 {
		return http.StatusBadRequest, fmt.Errorf("no data provided")
	}

	// Validate all column names
	for col := range data {
		if !isSafeIdentifier(col) {
			return http.StatusBadRequest, fmt.Errorf("invalid column name: %s", col)
		}
	}

	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("project not found")
	}

	qualifiedTable := quoteIdent(schema) + "." + quoteIdent(tableName)

	// Build UPDATE SET clause
	setClauses := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data)+1)
	i := 1
	for col, val := range data {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdent(col), i))
		values = append(values, val)
		i++
	}
	// PK value is the last parameter
	values = append(values, pkValue)

	query := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $%d`,
		qualifiedTable,
		strings.Join(setClauses, ", "),
		quoteIdent(pkColumn),
		i,
	)

	return executeSingleRowMutation(ctx, pool, schema, tableName, pkColumn, query, values...)
}

func executeSingleRowMutation(ctx context.Context, pool *pgxpool.Pool, schema, table, pkColumn, query string, values ...interface{}) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer tx.Rollback(ctx)
	var unique bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_attribute a ON a.attrelid=t.oid AND a.attname=$3
		WHERE n.nspname=$1 AND t.relname=$2 AND i.indisunique AND i.indisvalid AND i.indnkeyatts=1 AND i.indpred IS NULL AND i.indexprs IS NULL AND i.indkey[0]=a.attnum)`, schema, table, pkColumn).Scan(&unique)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !unique {
		return http.StatusBadRequest, fmt.Errorf("row identity must be a single-column primary or unique key")
	}
	tag, err := tx.Exec(ctx, query, values...)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("update row: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return http.StatusNotFound, fmt.Errorf("row not found")
	}

	if tag.RowsAffected() != 1 {
		return http.StatusConflict, fmt.Errorf("row mutation would affect multiple rows")
	}
	if err := tx.Commit(ctx); err != nil {
		return http.StatusInternalServerError, err
	}
	return http.StatusOK, nil
}

// DeleteRow deletes a row identified by primary key column and value.
func (s *TableService) DeleteRow(ctx context.Context, projectID, schema, tableName, pkColumn, pkValue string) (int, error) {
	if err := validateSchemaTable(schema, tableName); err != nil {
		return http.StatusBadRequest, err
	}
	if !isSafeIdentifier(pkColumn) {
		return http.StatusBadRequest, fmt.Errorf("invalid primary key column name")
	}

	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("project not found")
	}

	qualifiedTable := quoteIdent(schema) + "." + quoteIdent(tableName)
	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`, qualifiedTable, quoteIdent(pkColumn))

	return executeSingleRowMutation(ctx, pool, schema, tableName, pkColumn, query, pkValue)
}
