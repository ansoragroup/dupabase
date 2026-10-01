package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JWTClaims holds the claims from a JWT token.
type JWTClaims map[string]interface{}

// validRoleName ensures role names only contain safe characters (prevents SQL injection).
var validRoleName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ExecuteWithRLS runs a callback within a transaction that has PostgreSQL RLS
// context set (role + JWT claims). service_role bypasses RLS entirely.
func ExecuteWithRLS[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	role string,
	claims JWTClaims,
	fn func(tx pgx.Tx) (T, error),
) (T, error) {
	var zero T

	// Validate role name to prevent SQL injection (SET LOCAL ROLE doesn't support $1)
	if !validRoleName.MatchString(role) {
		return zero, fmt.Errorf("invalid role name: %s", role)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Set the PostgreSQL role for this transaction
	// Role names cannot be parameterized in SET LOCAL ROLE, so we validate via regex above
	_, err = tx.Exec(ctx, fmt.Sprintf(`SET LOCAL ROLE "%s"`, role))
	if err != nil {
		return zero, fmt.Errorf("set role %s: %w", role, err)
	}

	// Set full claims JSON using parameterized set_config() — safe from injection
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return zero, fmt.Errorf("marshal JWT claims: %w", err)
	}
	_, err = tx.Exec(ctx, `SELECT set_config('request.jwt.claims', $1, true)`, string(claimsJSON))
	if err != nil {
		return zero, fmt.Errorf("set jwt claims: %w", err)
	}

	// Set individual claims for convenience using parameterized set_config()
	if sub, ok := claims["sub"].(string); ok && sub != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('request.jwt.claim.sub', $1, true)`, sub); err != nil {
			slog.Warn("failed to set JWT claim", "claim", "sub", "error", err)
		}
	}
	if r, ok := claims["role"].(string); ok && r != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('request.jwt.claim.role', $1, true)`, r); err != nil {
			slog.Warn("failed to set JWT claim", "claim", "role", "error", err)
		}
	}
	if email, ok := claims["email"].(string); ok && email != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('request.jwt.claim.email', $1, true)`, email); err != nil {
			slog.Warn("failed to set JWT claim", "claim", "email", "error", err)
		}
	}

	result, err := fn(tx)
	if err != nil {
		return zero, err
	}

	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit tx: %w", err)
	}

	return result, nil
}
