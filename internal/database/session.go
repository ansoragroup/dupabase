package database

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserSessionEligible checks live account state, including PostgreSQL infinity bans.
func UserSessionEligible(ctx context.Context, pool *pgxpool.Pool, userID string, autoconfirm bool) bool {
	var eligible bool
	err := pool.QueryRow(ctx, `SELECT deleted_at IS NULL AND NOT COALESCE(banned_until>NOW(),false)
		AND (is_anonymous OR $2 OR email_confirmed_at IS NOT NULL) FROM auth.users WHERE id=$1`, userID, autoconfirm).Scan(&eligible)
	return err == nil && eligible
}
