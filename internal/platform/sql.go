package platform

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLService provides SQL query execution for project databases.
type SQLService struct {
	db          *pgxpool.Pool
	poolManager *database.PoolManager
}

// NewSQLService creates a new SQLService.
func NewSQLService(db *pgxpool.Pool, pm *database.PoolManager) *SQLService {
	return &SQLService{db: db, poolManager: pm}
}

// --- Types ---

// SQLRequest is the request body for executing a SQL query.
type SQLRequest struct {
	Query    string `json:"query"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// SQLResponse contains the results of a SQL query execution.
type SQLResponse struct {
	Columns       []string        `json:"columns"`
	Rows          [][]interface{} `json:"rows"`
	RowCount      int             `json:"row_count"`
	ExecutionTime float64         `json:"execution_time_ms"`
}

// --- Methods ---

// ExecuteSQL executes a SQL query against a project database.
// It enforces a 30-second statement timeout and limits results to 1000 rows.
func (s *SQLService) ExecuteSQL(ctx context.Context, projectID string, req SQLRequest) (*SQLResponse, int, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("query is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := s.poolManager.GetPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	// Acquire dedicated connection for statement_timeout
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("acquire connection: %w", err)
	}
	// Arbitrary SQL can alter session state. Destroy this connection rather than
	// returning changed roles/settings/prepared statements to API traffic.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(closeCtx)
	}()

	// Set timeout to prevent runaway queries
	_, err = conn.Exec(ctx, "SET statement_timeout = '30s'")
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("set timeout: %w", err)
	}

	start := time.Now()

	if req.ReadOnly {
		// Execute in a read-only transaction
		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		_, err = tx.Exec(ctx, "SET TRANSACTION READ ONLY")
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("set read only: %w", err)
		}

		rows, err := tx.Query(ctx, query)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("query error: %w", err)
		}
		defer rows.Close()

		result, statusCode, resultErr := scanQueryResults(rows)
		rows.Close()
		if resultErr != nil {
			return nil, statusCode, resultErr
		}

		result.ExecutionTime = float64(time.Since(start).Microseconds()) / 1000.0

		// Commit read-only transaction (no data changes)
		if err := tx.Commit(ctx); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("commit: %w", err)
		}

		return result, http.StatusOK, nil
	}

	// Execute directly (read-write)
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	result, statusCode, resultErr := scanQueryResults(rows)
	if resultErr != nil {
		return nil, statusCode, resultErr
	}

	result.ExecutionTime = float64(time.Since(start).Microseconds()) / 1000.0

	return result, http.StatusOK, nil
}

// scanQueryResults extracts columns and rows from a pgx query result.
// It limits output to 1000 rows.
func scanQueryResults(rows pgx.Rows) (*SQLResponse, int, error) {
	fds := rows.FieldDescriptions()
	columns := make([]string, len(fds))
	for i, fd := range fds {
		columns[i] = fd.Name
	}

	const maxRows = 1000
	var resultRows [][]interface{}
	for rows.Next() {
		if len(resultRows) >= maxRows {
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan values: %w", err)
		}
		// Sanitize values for JSON serialization (e.g. UUID [16]byte → string)
		for i, v := range vals {
			vals[i] = sanitizeValue(v)
		}
		resultRows = append(resultRows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("rows error: %w", err)
	}

	if resultRows == nil {
		resultRows = [][]interface{}{}
	}

	return &SQLResponse{
		Columns:  columns,
		Rows:     resultRows,
		RowCount: len(resultRows),
	}, http.StatusOK, nil
}
