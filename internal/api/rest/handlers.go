package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/ansoraGROUP/dupabase/internal/httputil"
	"github.com/ansoraGROUP/dupabase/internal/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

// HandleTable handles all CRUD operations on /rest/v1/{table}
func (h *Handler) HandleTable(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "PGRST000", "missing project context")
		return
	}

	// Extract table name from URL path
	table := extractTableName(r.URL.Path)
	if table == "" {
		writeError(w, http.StatusBadRequest, "PGRST100", "missing table name")
		return
	}

	// Determine the effective role for RLS
	role, claims := resolveRoleAndClaims(r, project)
	if !authorizeUserSession(r, project, pool, role, claims) {
		writeError(w, http.StatusUnauthorized, "PGRST301", "invalid or inactive user session")
		return
	}

	// Get schema from Accept-Profile / Content-Profile headers
	schema := "public"
	if s := r.Header.Get("Accept-Profile"); s != "" && r.Method == http.MethodGet {
		schema = s
	}
	if s := r.Header.Get("Content-Profile"); s != "" && r.Method != http.MethodGet {
		schema = s
	}

	// Validate schema name — only allow public and user-created schemas
	if !isAllowedSchema(schema) {
		writeError(w, http.StatusBadRequest, "PGRST106", "access to this schema is not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.handleSelect(ctx, w, r, pool, role, claims, schema, table)
	case http.MethodPost:
		h.handleInsert(ctx, w, r, pool, role, claims, schema, table)
	case http.MethodPatch:
		h.handleUpdate(ctx, w, r, pool, role, claims, schema, table)
	case http.MethodDelete:
		h.handleDelete(ctx, w, r, pool, role, claims, schema, table)
	default:
		writeError(w, http.StatusMethodNotAllowed, "PGRST105", "method not allowed")
	}
}

// HandleRPC handles POST /rest/v1/rpc/{function}
func (h *Handler) HandleRPC(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "PGRST000", "missing project context")
		return
	}

	// Extract function name
	fnName := extractRPCName(r.URL.Path)
	if fnName == "" {
		writeError(w, http.StatusBadRequest, "PGRST100", "missing function name")
		return
	}

	role, claims := resolveRoleAndClaims(r, project)
	if !authorizeUserSession(r, project, pool, role, claims) {
		writeError(w, http.StatusUnauthorized, "PGRST301", "invalid or inactive user session")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Parse function arguments
	var args map[string]interface{}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "PGRST100", "request body is not valid JSON")
			return
		}
	}

	// Build SELECT statement for RPC
	// PostgREST calls functions as: SELECT * FROM schema.function(args)
	argParts := []string{}
	argValues := []interface{}{}
	i := 1
	for k, v := range args {
		argParts = append(argParts, fmt.Sprintf("%s := $%d", quoteIdent(k), i))
		argValues = append(argValues, v)
		i++
	}

	schema := "public"
	if s := r.Header.Get("Content-Profile"); s != "" {
		schema = s
	}
	if !isAllowedSchema(schema) {
		writeError(w, http.StatusBadRequest, "PGRST106", "access to this schema is not allowed")
		return
	}

	query := fmt.Sprintf(`SELECT * FROM %s.%s(%s)`,
		quoteIdent(schema), quoteIdent(fnName), strings.Join(argParts, ", "))

	result, err := database.ExecuteWithRLS(ctx, pool, role, database.JWTClaims(claims), func(tx pgx.Tx) (interface{}, error) {
		rows, err := tx.Query(ctx, query, argValues...)
		if err != nil {
			return nil, err
		}
		return collectRows(rows)
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "PGRST202", sanitizeDBError(err))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, result)
}

// hasFilterParams returns true if the query string contains at least one filter parameter
// (i.e., a parameter that is not a reserved non-filter keyword like select, order, limit, etc.).
func hasFilterParams(q url.Values) bool {
	for k := range q {
		switch k {
		case "select", "order", "limit", "offset", "on_conflict", "columns":
			continue
		default:
			return true
		}
	}
	return false
}

// ---------- CRUD handlers ----------

func (h *Handler) handleSelect(ctx context.Context, w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, role string, claims map[string]interface{}, schema, table string) {
	q := r.URL.Query()

	// Parse select parameter for columns and embedded resources
	selectParam := q.Get("select")

	cols, embeds := parseSelectWithEmbedding(selectParam)

	// Build WHERE clause from filters
	where, whereArgs, whereErr := buildWhereClause(q, schema, table)
	if whereErr != nil {
		writeError(w, http.StatusBadRequest, "PGRST100", whereErr.Error())
		return
	}

	// Build ORDER BY
	orderBy := ""
	if o := q.Get("order"); o != "" {
		orderBy = " ORDER BY " + buildOrderClause(o)
	}

	// Build LIMIT/OFFSET
	limitOffset := ""
	limitVal := 0
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limitOffset += fmt.Sprintf(" LIMIT %d", n)
			limitVal = n
		}
	}
	if o := q.Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil {
			limitOffset += fmt.Sprintf(" OFFSET %d", n)
		}
	}

	// Check for Range header
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && limitVal == 0 {
		start, end := parseRange(rangeHeader)
		if start >= 0 && end >= start {
			limitOffset = fmt.Sprintf(" LIMIT %d OFFSET %d", end-start+1, start)
		}
	}

	// Check for count preference
	prefer := parsePrefer(r.Header.Get("Prefer"))
	wantCount := prefer["count"] != ""

	var result interface{}
	var err error

	if len(embeds) == 0 {
		// No embeddings — simple query (original fast path)
		selectCols := strings.Join(cols, ", ")
		query := fmt.Sprintf(`SELECT %s FROM %s.%s%s%s%s`,
			selectCols, quoteIdent(schema), quoteIdent(table), where, orderBy, limitOffset)

		result, err = database.ExecuteWithRLS(ctx, pool, role, database.JWTClaims(claims), func(tx pgx.Tx) (interface{}, error) {
			rows, err := tx.Query(ctx, query, whereArgs...)
			if err != nil {
				return nil, err
			}
			data, err := collectRows(rows)
			if err != nil {
				return nil, err
			}
			if wantCount {
				countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s.%s%s`,
					quoteIdent(schema), quoteIdent(table), where)
				var total int
				if err := tx.QueryRow(ctx, countQuery, whereArgs...).Scan(&total); err != nil {
					slog.Warn("count query scan failed", "error", err)
					total = 0
				}
				if dataSlice := data.([]map[string]interface{}); len(dataSlice) > 0 {
					w.Header().Set("Content-Range", fmt.Sprintf("0-%d/%d", len(dataSlice)-1, total))
				} else {
					w.Header().Set("Content-Range", fmt.Sprintf("*/%d", total))
				}
			}
			return data, nil
		})
	} else {
		// Has embeddings — look up FKs BEFORE entering RLS transaction
		// (information_schema may not be visible to anon/authenticated roles)
		selectParts := make([]string, len(cols))
		copy(selectParts, cols)
		var joinClauses []string
		var fkErr error

		for _, embed := range embeds {
			// Try forward FK: main table has FK to embedded table
			fk, err2 := lookupFK(ctx, pool, schema, table, embed.table)
			if err2 == nil {

				// M:1 embedding — use correlated subquery
				embCols := make([]string, len(embed.columns))
				for i, c := range embed.columns {
					embCols[i] = quoteIdent(c)
				}
				subquery := fmt.Sprintf(
					`(SELECT row_to_json(x) FROM (SELECT %s FROM %s.%s WHERE %s = %s.%s LIMIT 1) x) AS %s`,
					strings.Join(embCols, ", "),
					quoteIdent(schema), quoteIdent(embed.table),
					quoteIdent(fk.toCol),
					quoteIdent(table), quoteIdent(fk.fromCol),
					quoteIdent(embed.alias),
				)
				selectParts = append(selectParts, subquery)
			} else {
				// Try reverse FK: embedded table has FK to main table
				fk, err2 = lookupFK(ctx, pool, schema, embed.table, table)
				if err2 != nil {
					fkErr = fmt.Errorf("no foreign key between %s and %s", table, embed.table)
					break
				}
				if embed.isInner {
					joinClauses = append(joinClauses, fmt.Sprintf(
						` INNER JOIN %s.%s ON %s.%s = %s.%s`,
						quoteIdent(schema), quoteIdent(embed.table),
						quoteIdent(embed.table), quoteIdent(fk.fromCol),
						quoteIdent(table), quoteIdent(fk.toCol),
					))
				} else {
					joinClauses = append(joinClauses, fmt.Sprintf(
						` LEFT JOIN %s.%s ON %s.%s = %s.%s`,
						quoteIdent(schema), quoteIdent(embed.table),
						quoteIdent(embed.table), quoteIdent(fk.fromCol),
						quoteIdent(table), quoteIdent(fk.toCol),
					))
				}
			}
		}

		if fkErr != nil {
			writeError(w, http.StatusBadRequest, "PGRST102", fkErr.Error())
			return
		}

		query := fmt.Sprintf(`SELECT %s FROM %s.%s%s%s%s%s`,
			strings.Join(selectParts, ", "),
			quoteIdent(schema), quoteIdent(table),
			strings.Join(joinClauses, ""),
			where, orderBy, limitOffset,
		)
		result, err = database.ExecuteWithRLS(ctx, pool, role, database.JWTClaims(claims), func(tx pgx.Tx) (interface{}, error) {
			rows, qErr := tx.Query(ctx, query, whereArgs...)
			if qErr != nil {
				return nil, qErr
			}
			data, cErr := collectRows(rows)
			if cErr != nil {
				return nil, cErr
			}

			if wantCount {
				countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s.%s%s%s`,
					quoteIdent(schema), quoteIdent(table),
					strings.Join(joinClauses, ""), where)
				var total int
				if err := tx.QueryRow(ctx, countQuery, whereArgs...).Scan(&total); err != nil {
					slog.Warn("count query scan failed", "error", err)
					total = 0
				}
				if dataSlice := data.([]map[string]interface{}); len(dataSlice) > 0 {
					w.Header().Set("Content-Range", fmt.Sprintf("0-%d/%d", len(dataSlice)-1, total))
				} else {
					w.Header().Set("Content-Range", fmt.Sprintf("*/%d", total))
				}
			}
			return data, nil
		})
	}

	if err != nil {
		writeError(w, http.StatusBadRequest, "PGRST102", sanitizeDBError(err))
		return
	}

	// Handle single object response
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/vnd.pgrst.object+json") {
		rows, ok := result.([]map[string]interface{})
		if !ok || len(rows) != 1 {
			writeError(w, http.StatusNotAcceptable, "PGRST116", "JSON object requested, multiple (or no) rows returned")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, rows[0])
		return
	}

	httputil.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) handleInsert(ctx context.Context, w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, role string, claims map[string]interface{}, schema, table string) {
	var body interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "PGRST100", "invalid JSON body")
		return
	}

	prefer := parsePrefer(r.Header.Get("Prefer"))
	isUpsert := prefer["resolution"] == "merge-duplicates" || prefer["resolution"] == "ignore-duplicates"
	returnRepr := prefer["return"] == "representation"

	// Handle both single object and array
	var records []map[string]interface{}
	switch v := body.(type) {
	case map[string]interface{}:
		records = []map[string]interface{}{v}
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				records = append(records, m)
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "PGRST100", "body must be an object or array")
		return
	}

	if len(records) == 0 {
		writeError(w, http.StatusBadRequest, "PGRST100", "empty body")
		return
	}

	// Get column names from first record
	columns := make([]string, 0)
	for k := range records[0] {
		columns = append(columns, k)
	}

	// Support PostgREST ?columns= parameter to restrict which columns are used
	if colsParam := r.URL.Query().Get("columns"); colsParam != "" {
		allowedCols := make(map[string]bool)
		for _, c := range strings.Split(colsParam, ",") {
			c = strings.Trim(c, `"' `)
			if c != "" {
				allowedCols[c] = true
			}
		}
		var filteredCols []string
		for _, c := range columns {
			if allowedCols[c] {
				filteredCols = append(filteredCols, c)
			}
		}
		if len(filteredCols) > 0 {
			columns = filteredCols
		}
	}

	// Build VALUES placeholders
	var valueSets []string
	var allArgs []interface{}
	argIdx := 1
	for _, rec := range records {
		var placeholders []string
		for _, col := range columns {
			placeholders = append(placeholders, fmt.Sprintf("$%d", argIdx))
			allArgs = append(allArgs, rec[col])
			argIdx++
		}
		valueSets = append(valueSets, "("+strings.Join(placeholders, ", ")+")")
	}

	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = quoteIdent(c)
	}

	query := fmt.Sprintf(`INSERT INTO %s.%s (%s) VALUES %s`,
		quoteIdent(schema), quoteIdent(table),
		strings.Join(quotedCols, ", "),
		strings.Join(valueSets, ", "),
	)

	if isUpsert {
		onConflict := r.URL.Query().Get("on_conflict")
		// If no on_conflict specified, auto-detect primary key (like PostgREST)
		if onConflict == "" {
			pkCols := getPrimaryKeyCols(ctx, pool, role, database.JWTClaims(claims), schema, table)
			if len(pkCols) > 0 {
				onConflict = strings.Join(pkCols, ",")
			}
		}
		if onConflict != "" {
			if prefer["resolution"] == "ignore-duplicates" {
				conflictCols := strings.Split(onConflict, ",")
				quotedConflict := make([]string, len(conflictCols))
				for i, c := range conflictCols {
					quotedConflict[i] = quoteIdent(strings.TrimSpace(c))
				}
				query += fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", strings.Join(quotedConflict, ", "))
			} else {
				conflictCols := strings.Split(onConflict, ",")
				conflictSet := make(map[string]bool)
				quotedConflict := make([]string, len(conflictCols))
				for i, c := range conflictCols {
					c = strings.TrimSpace(c)
					conflictSet[c] = true
					quotedConflict[i] = quoteIdent(c)
				}
				setClauses := make([]string, 0)
				for _, col := range columns {
					if !conflictSet[col] {
						setClauses = append(setClauses, fmt.Sprintf("%s = EXCLUDED.%s", quoteIdent(col), quoteIdent(col)))
					}
				}
				if len(setClauses) > 0 {
					query += fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s", strings.Join(quotedConflict, ", "), strings.Join(setClauses, ", "))
				} else {
					// All columns are conflict columns — nothing to update, just do nothing
					query += fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", strings.Join(quotedConflict, ", "))
				}
			}
		}
	}

	if returnRepr {
		selectCols := "*"
		if s := r.URL.Query().Get("select"); s != "" {
			selectCols = buildSelectClause(s)
		}
		query += " RETURNING " + selectCols
	}

	result, err := database.ExecuteWithRLS(ctx, pool, role, database.JWTClaims(claims), func(tx pgx.Tx) (interface{}, error) {
		if returnRepr {
			rows, err := tx.Query(ctx, query, allArgs...)
			if err != nil {
				return nil, err
			}
			return collectRows(rows)
		}
		_, err := tx.Exec(ctx, query, allArgs...)
		return nil, err
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "PGRST204", sanitizeDBError(err))
		return
	}

	if returnRepr {
		writeMaybeObject(w, r, http.StatusCreated, result)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (h *Handler) handleUpdate(ctx context.Context, w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, role string, claims map[string]interface{}, schema, table string) {
	if !hasFilterParams(r.URL.Query()) {
		writeError(w, http.StatusBadRequest, "PGRST116", "Bulk update requires at least one filter parameter")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "PGRST100", "invalid JSON body")
		return
	}

	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "PGRST100", "empty update body")
		return
	}

	where, whereArgs, whereErr := buildWhereClause(r.URL.Query(), schema, table)
	if whereErr != nil {
		writeError(w, http.StatusBadRequest, "PGRST100", whereErr.Error())
		return
	}

	setClauses := make([]string, 0)
	setArgs := make([]interface{}, 0)
	argOffset := len(whereArgs)
	for k, v := range body {
		argOffset++
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdent(k), argOffset))
		setArgs = append(setArgs, v)
	}

	allArgs := append(whereArgs, setArgs...)

	prefer := parsePrefer(r.Header.Get("Prefer"))
	returnRepr := prefer["return"] == "representation"

	query := fmt.Sprintf(`UPDATE %s.%s SET %s%s`,
		quoteIdent(schema), quoteIdent(table),
		strings.Join(setClauses, ", "), where)

	if returnRepr {
		selectCols := "*"
		if s := r.URL.Query().Get("select"); s != "" {
			selectCols = buildSelectClause(s)
		}
		query += " RETURNING " + selectCols
	}

	result, err := database.ExecuteWithRLS(ctx, pool, role, database.JWTClaims(claims), func(tx pgx.Tx) (interface{}, error) {
		if returnRepr {
			rows, err := tx.Query(ctx, query, allArgs...)
			if err != nil {
				return nil, err
			}
			return collectRows(rows)
		}
		_, err := tx.Exec(ctx, query, allArgs...)
		return nil, err
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "PGRST204", sanitizeDBError(err))
		return
	}

	if returnRepr {
		writeMaybeObject(w, r, http.StatusOK, result)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) handleDelete(ctx context.Context, w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, role string, claims map[string]interface{}, schema, table string) {
	if !hasFilterParams(r.URL.Query()) {
		writeError(w, http.StatusBadRequest, "PGRST116", "Bulk delete requires at least one filter parameter")
		return
	}

	where, whereArgs, whereErr := buildWhereClause(r.URL.Query(), schema, table)
	if whereErr != nil {
		writeError(w, http.StatusBadRequest, "PGRST100", whereErr.Error())
		return
	}

	prefer := parsePrefer(r.Header.Get("Prefer"))
	returnRepr := prefer["return"] == "representation"

	query := fmt.Sprintf(`DELETE FROM %s.%s%s`,
		quoteIdent(schema), quoteIdent(table), where)

	if returnRepr {
		selectCols := "*"
		if s := r.URL.Query().Get("select"); s != "" {
			selectCols = buildSelectClause(s)
		}
		query += " RETURNING " + selectCols
	}

	result, err := database.ExecuteWithRLS(ctx, pool, role, database.JWTClaims(claims), func(tx pgx.Tx) (interface{}, error) {
		if returnRepr {
			rows, err := tx.Query(ctx, query, whereArgs...)
			if err != nil {
				return nil, err
			}
			return collectRows(rows)
		}
		_, err := tx.Exec(ctx, query, whereArgs...)
		return nil, err
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "PGRST204", sanitizeDBError(err))
		return
	}

	if returnRepr {
		writeMaybeObject(w, r, http.StatusOK, result)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---------- Query building helpers ----------

// embeddedResource represents a PostgREST resource embedding in the select clause
type embeddedResource struct {
	alias   string   // display name (e.g., "category")
	table   string   // related table (e.g., "categories")
	columns []string // columns to select from related table
	isInner bool     // true for !inner modifier
}

// fkRelation describes a foreign key relationship between two tables
type fkRelation struct {
	fromCol   string
	toCol     string
	fromTable string
	toTable   string
}

// splitRespectingParens splits by commas but not inside parentheses
func splitRespectingParens(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if start <= len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

// parseSelectWithEmbedding parses the select parameter extracting columns and embedded resources
func parseSelectWithEmbedding(selectParam string) ([]string, []embeddedResource) {
	selectParam = strings.TrimSpace(selectParam)
	if selectParam == "" {
		return []string{"*"}, nil
	}

	parts := splitRespectingParens(selectParam)
	var cols []string
	var embeds []embeddedResource

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "*" {
			cols = append(cols, "*")
			continue
		}

		// Check for embedding: contains ( and ends with )
		parenIdx := strings.Index(p, "(")
		if parenIdx > 0 && strings.HasSuffix(p, ")") {
			inner := p[parenIdx+1 : len(p)-1]
			prefix := p[:parenIdx]

			embed := embeddedResource{}

			// Check for !inner modifier
			if bangIdx := strings.Index(prefix, "!"); bangIdx > 0 {
				modifier := prefix[bangIdx+1:]
				prefix = prefix[:bangIdx]
				if modifier == "inner" {
					embed.isInner = true
				}
			}

			// Check for alias:table
			if colonIdx := strings.Index(prefix, ":"); colonIdx > 0 {
				embed.alias = prefix[:colonIdx]
				embed.table = prefix[colonIdx+1:]
			} else {
				embed.table = prefix
				embed.alias = prefix
			}

			// Parse inner columns
			for _, c := range strings.Split(inner, ",") {
				c = strings.TrimSpace(c)
				if c != "" {
					embed.columns = append(embed.columns, c)
				}
			}

			embeds = append(embeds, embed)
			continue
		}

		// Handle alias: alias:column
		if idx := strings.Index(p, ":"); idx > 0 {
			alias := strings.TrimSpace(p[:idx])
			col := strings.TrimSpace(p[idx+1:])
			cols = append(cols, fmt.Sprintf("%s AS %s", quoteIdent(col), quoteIdent(alias)))
			continue
		}

		cols = append(cols, quoteIdent(p))
	}

	if len(cols) == 0 {
		cols = []string{"*"}
	}

	return cols, embeds
}

// buildSelectClause builds a simple SQL SELECT clause (no embeddings)
func buildSelectClause(selectParam string) string {
	cols, _ := parseSelectWithEmbedding(selectParam)
	return strings.Join(cols, ", ")
}

// lookupFK finds a foreign key from fromTable to toTable in the given schema.
// Uses the pool directly (not an RLS transaction) because information_schema
// may not be visible to restricted roles like anon/authenticated.
func lookupFK(ctx context.Context, pool *pgxpool.Pool, schema, fromTable, toTable string) (*fkRelation, error) {
	query := `
		SELECT kcu.column_name, ccu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON ccu.constraint_name = tc.constraint_name AND ccu.table_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
			AND tc.table_schema = $1
			AND tc.table_name = $2
			AND ccu.table_name = $3
		LIMIT 1
	`
	var fromCol, toCol string
	err := pool.QueryRow(ctx, query, schema, fromTable, toTable).Scan(&fromCol, &toCol)
	if err != nil {
		return nil, err
	}
	return &fkRelation{fromCol: fromCol, toCol: toCol, fromTable: fromTable, toTable: toTable}, nil
}

func buildWhereClause(q map[string][]string, schema, table string) (string, []interface{}, error) {
	// Reserved params that are not filters
	reserved := map[string]bool{"select": true, "order": true, "limit": true, "offset": true, "on_conflict": true, "columns": true}

	var conditions []string
	var args []interface{}
	argIdx := 1

	for key, values := range q {
		if reserved[key] {
			continue
		}
		// Logical operators are not yet supported
		if key == "and" || key == "or" || key == "not.and" || key == "not.or" {
			return "", nil, fmt.Errorf("unsupported logical operator: %s", key)
		}

		for _, val := range values {
			cond, condArgs, newIdx := parseFilter(key, val, argIdx)
			if cond == "" {
				return "", nil, fmt.Errorf("invalid filter for %s", key)
			}
			if cond != "" {
				conditions = append(conditions, cond)
				args = append(args, condArgs...)
				argIdx = newIdx
			}
		}
	}

	if len(conditions) == 0 {
		return "", nil, nil
	}

	return " WHERE " + strings.Join(conditions, " AND "), args, nil
}

// quoteIdentDotted handles table.column notation (e.g., "user_categories.user_id")
func quoteIdentDotted(s string) string {
	if dotIdx := strings.Index(s, "."); dotIdx > 0 {
		return quoteIdent(s[:dotIdx]) + "." + quoteIdent(s[dotIdx+1:])
	}
	return quoteIdent(s)
}

func parseFilter(column, value string, argIdx int) (string, []interface{}, int) {
	col := quoteIdentDotted(column)

	// Handle negation
	negate := false
	if strings.HasPrefix(value, "not.") {
		negate = true
		value = value[4:]
	}

	// Parse operator.value
	dotIdx := strings.Index(value, ".")
	if dotIdx < 0 {
		return "", nil, argIdx
	}

	op := value[:dotIdx]
	val := value[dotIdx+1:]

	var condition string
	var args []interface{}

	switch op {
	case "eq":
		condition = fmt.Sprintf("%s = $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "neq":
		condition = fmt.Sprintf("%s != $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "gt":
		condition = fmt.Sprintf("%s > $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "gte":
		condition = fmt.Sprintf("%s >= $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "lt":
		condition = fmt.Sprintf("%s < $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "lte":
		condition = fmt.Sprintf("%s <= $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "like":
		condition = fmt.Sprintf("%s LIKE $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "ilike":
		condition = fmt.Sprintf("%s ILIKE $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "is":
		switch strings.ToLower(val) {
		case "null":
			condition = fmt.Sprintf("%s IS NULL", col)
		case "true":
			condition = fmt.Sprintf("%s IS TRUE", col)
		case "false":
			condition = fmt.Sprintf("%s IS FALSE", col)
		default:
			return "", nil, argIdx
		}
	case "in":
		// Parse (val1,val2,val3)
		val = strings.TrimPrefix(val, "(")
		val = strings.TrimSuffix(val, ")")
		items := strings.Split(val, ",")
		placeholders := make([]string, len(items))
		for i, item := range items {
			item = strings.Trim(item, `"' `)
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, item)
			argIdx++
		}
		condition = fmt.Sprintf("%s IN (%s)", col, strings.Join(placeholders, ", "))
	case "cs":
		condition = fmt.Sprintf("%s @> $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "cd":
		condition = fmt.Sprintf("%s <@ $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "ov":
		condition = fmt.Sprintf("%s && $%d", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "fts":
		condition = fmt.Sprintf("%s @@ to_tsquery($%d)", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "plfts":
		condition = fmt.Sprintf("%s @@ plainto_tsquery($%d)", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "phfts":
		condition = fmt.Sprintf("%s @@ phraseto_tsquery($%d)", col, argIdx)
		args = []interface{}{val}
		argIdx++
	case "wfts":
		condition = fmt.Sprintf("%s @@ websearch_to_tsquery($%d)", col, argIdx)
		args = []interface{}{val}
		argIdx++
	default:
		return "", nil, argIdx
	}

	if negate && condition != "" {
		condition = "NOT (" + condition + ")"
	}

	return condition, args, argIdx
}

func buildOrderClause(orderParam string) string {
	parts := strings.Split(orderParam, ",")
	var clauses []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		subParts := strings.Split(p, ".")
		col := quoteIdent(subParts[0])
		dir := "ASC"
		nulls := ""
		for _, sub := range subParts[1:] {
			switch strings.ToLower(sub) {
			case "asc":
				dir = "ASC"
			case "desc":
				dir = "DESC"
			case "nullsfirst":
				nulls = " NULLS FIRST"
			case "nullslast":
				nulls = " NULLS LAST"
			}
		}
		clauses = append(clauses, col+" "+dir+nulls)
	}
	return strings.Join(clauses, ", ")
}

func parseRange(header string) (int, int) {
	parts := strings.Split(header, "-")
	if len(parts) != 2 {
		return -1, -1
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return -1, -1
	}
	return start, end
}

func parsePrefer(header string) map[string]string {
	prefs := make(map[string]string)
	parts := strings.Split(header, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if idx := strings.Index(p, "="); idx > 0 {
			prefs[strings.TrimSpace(p[:idx])] = strings.TrimSpace(p[idx+1:])
		}
	}
	return prefs
}

func resolveRoleAndClaims(r *http.Request, project *database.ProjectRecord) (string, map[string]interface{}) {
	// Default to apikey role
	role := middleware.GetAPIKeyRole(r)
	claims := map[string]interface{}(middleware.GetAPIKeyClaims(r))

	// Check if Authorization header has a different token (user session)
	auth := r.Header.Get("Authorization")
	apikey := r.Header.Get("apikey")
	if auth != "" {
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		if tokenStr != apikey {
			// This is a user JWT, verify it (enforce HMAC algorithm)
			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(project.JWTSecret), nil
			})
			if err == nil && token.Valid {
				if userClaims, ok := token.Claims.(jwt.MapClaims); ok {
					claims = map[string]interface{}(userClaims)
					if r, ok := userClaims["role"].(string); ok {
						role = r
					}
				}
			} else {
				return "", nil
			}
		}
	}

	return role, claims
}

func authorizeUserSession(r *http.Request, project *database.ProjectRecord, pool *pgxpool.Pool, role string, claims map[string]interface{}) bool {
	if role == "" || claims == nil {
		return false
	}
	if role == "authenticated" {
		if sub, ok := claims["sub"].(string); ok && sub != "" {
			return database.UserSessionEligible(r.Context(), pool, sub, project.Autoconfirm)
		}
	}
	return true
}

// allowedSchemas are schemas that can be accessed via the REST API.
// Only public and explicitly user-created schemas are allowed.
// Additional schemas can be added via the ALLOWED_SCHEMAS env var.
var allowedSchemas map[string]bool

func init() {
	allowedSchemas = map[string]bool{"public": true}
	if schemas := os.Getenv("ALLOWED_SCHEMAS"); schemas != "" {
		for _, s := range strings.Split(schemas, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				allowedSchemas[s] = true
			}
		}
	}
}

// isAllowedSchema checks if a schema name is safe to query.
func isAllowedSchema(schema string) bool {
	if schema == "auth" || schema == "platform" || schema == "information_schema" || strings.HasPrefix(schema, "pg_") {
		return false
	}
	if allowedSchemas[schema] {
		return true
	}
	for _, s := range strings.Split(os.Getenv("ALLOWED_SCHEMAS"), ",") {
		if strings.TrimSpace(s) == schema && schema != "" {
			return true
		}
	}
	return false
}

func extractTableName(path string) string {
	// /rest/v1/table_name -> table_name
	path = strings.TrimPrefix(path, "/rest/v1/")
	path = strings.TrimSuffix(path, "/")
	if strings.Contains(path, "/") {
		return ""
	}
	return path
}

func extractRPCName(path string) string {
	// /rest/v1/rpc/function_name -> function_name
	path = strings.TrimPrefix(path, "/rest/v1/rpc/")
	path = strings.TrimSuffix(path, "/")
	if strings.Contains(path, "/") {
		return ""
	}
	return path
}

func quoteIdent(s string) string {
	// Simple identifier quoting — prevents SQL injection
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

var errResponseTooLarge = errors.New("response is too large; request a smaller page")

func collectRows(rows pgx.Rows) (interface{}, error) {
	defer rows.Close()

	descs := rows.FieldDescriptions()
	var result []map[string]interface{}
	maxBytes := 32 * 1024 * 1024
	if value, err := strconv.Atoi(os.Getenv("REST_MAX_RESPONSE_BYTES")); err == nil && value > 0 {
		maxBytes = value
	}
	responseBytes := 0

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, desc := range descs {
			row[string(desc.Name)] = convertPgValue(values[i])
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		responseBytes += len(encoded) + 1
		if responseBytes > maxBytes {
			return nil, errResponseTooLarge
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if result == nil {
		result = []map[string]interface{}{}
	}

	return result, nil
}

// convertPgValue converts pgx-specific types to JSON-friendly representations.
func convertPgValue(v interface{}) interface{} {
	switch val := v.(type) {
	case [16]byte:
		// UUID as [16]byte → string
		u := pgtype.UUID{Bytes: val, Valid: true}
		s, _ := u.Value()
		return s
	case pgtype.UUID:
		if !val.Valid {
			return nil
		}
		s, _ := val.Value()
		return s
	case time.Time:
		return val.Format(time.RFC3339Nano)
	case []byte:
		// JSON/JSONB data from PostgreSQL (e.g., row_to_json results)
		if len(val) > 0 && (val[0] == '{' || val[0] == '[' || val[0] == '"') {
			var parsed interface{}
			if json.Unmarshal(val, &parsed) == nil {
				return parsed
			}
		}
		return string(val)
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = convertPgValue(item)
		}
		return result
	default:
		return v
	}
}

// sanitizeDBError removes internal database details from error messages.
func sanitizeDBError(err error) string {
	if errors.Is(err, errResponseTooLarge) {
		return errResponseTooLarge.Error()
	}
	msg := err.Error()
	// Only expose safe PostgreSQL error patterns to clients
	if strings.Contains(msg, "violates row-level security") {
		return "permission denied for this resource"
	}
	if strings.Contains(msg, "violates unique constraint") {
		return "duplicate key value violates unique constraint"
	}
	if strings.Contains(msg, "violates not-null constraint") {
		return "null value in column violates not-null constraint"
	}
	if strings.Contains(msg, "violates foreign key constraint") {
		return "foreign key constraint violation"
	}
	if strings.Contains(msg, "violates check constraint") {
		return "check constraint violation"
	}
	if strings.Contains(msg, "relation") && strings.Contains(msg, "does not exist") {
		return "Requested resource does not exist"
	}
	if strings.Contains(msg, "permission denied") {
		return "permission denied"
	}
	// Generic fallback — never expose raw DB errors
	return "database operation failed"
}

// writeMaybeObject returns a single object if Accept: application/vnd.pgrst.object+json,
// otherwise returns the array. This matches PostgREST's .single() behavior.
func writeMaybeObject(w http.ResponseWriter, r *http.Request, status int, result interface{}) {
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/vnd.pgrst.object+json") {
		rows, ok := result.([]map[string]interface{})
		if !ok || len(rows) != 1 {
			writeError(w, http.StatusNotAcceptable, "PGRST116", "JSON object requested, multiple (or no) rows returned")
			return
		}
		httputil.WriteJSON(w, status, rows[0])
		return
	}
	httputil.WriteJSON(w, status, result)
}

// getPrimaryKeyCols returns the primary key column names for a table.
func getPrimaryKeyCols(ctx context.Context, pool *pgxpool.Pool, role string, claims database.JWTClaims, schema, table string) []string {
	query := `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = ($1 || '.' || $2)::regclass
		  AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)
	`
	result, err := database.ExecuteWithRLS(ctx, pool, role, claims, func(tx pgx.Tx) (interface{}, error) {
		rows, err := tx.Query(ctx, query, schema, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				return nil, err
			}
			cols = append(cols, col)
		}
		return cols, rows.Err()
	})
	if err != nil {
		return nil
	}
	if cols, ok := result.([]string); ok {
		return cols
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	httputil.WriteJSON(w, status, map[string]interface{}{
		"code":    code,
		"message": message,
		"details": nil,
		"hint":    nil,
	})
}
