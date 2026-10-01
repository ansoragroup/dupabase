package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthUserService provides auth user management for project databases.
type AuthUserService struct {
	db          *pgxpool.Pool
	poolManager *database.PoolManager
}

// NewAuthUserService creates a new AuthUserService.
func NewAuthUserService(db *pgxpool.Pool, pm *database.PoolManager) *AuthUserService {
	return &AuthUserService{db: db, poolManager: pm}
}

// --- Types ---

// AuthUserInfo represents a user from the project's auth.users table.
type AuthUserInfo struct {
	ID               string     `json:"id"`
	Email            *string    `json:"email"`
	Phone            *string    `json:"phone"`
	EmailConfirmedAt *time.Time `json:"email_confirmed_at"`
	PhoneConfirmedAt *time.Time `json:"phone_confirmed_at"`
	LastSignInAt     *time.Time `json:"last_sign_in_at"`
	IsAnonymous      bool       `json:"is_anonymous"`
	BannedUntil      *time.Time `json:"banned_until"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// AuthUserListResponse contains a paginated list of auth users.
type AuthUserListResponse struct {
	Users   []AuthUserInfo `json:"users"`
	Total   int64          `json:"total"`
	Page    int            `json:"page"`
	PerPage int            `json:"per_page"`
}

// AuthUserDetail extends AuthUserInfo with metadata and sessions.
type AuthUserDetail struct {
	AuthUserInfo
	AppMetadata  interface{}       `json:"app_metadata"`
	UserMetadata interface{}       `json:"user_metadata"`
	Sessions     []AuthSessionInfo `json:"sessions"`
}

// AuthSessionInfo represents a session from the project's auth.sessions table.
type AuthSessionInfo struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	UserAgent *string   `json:"user_agent"`
	IP        *string   `json:"ip"`
}

// --- Helpers ---

// getProjectPool gets a connection pool for the project via pool manager.
func (s *AuthUserService) getProjectPool(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	pool, err := s.poolManager.GetPool(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get project pool: %w", err)
	}
	return pool, nil
}

// --- Methods ---

// ListAuthUsers returns a paginated list of auth users for a project, with optional search.
func (s *AuthUserService) ListAuthUsers(ctx context.Context, projectID string, page, perPage int, search string) (*AuthUserListResponse, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 50
	}
	if perPage > 100 {
		perPage = 100
	}

	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	offset := (page - 1) * perPage

	// Count total matching users
	var total int64
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM auth.users
		WHERE ($1 = '' OR email ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%')
	`, search).Scan(&total)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("count users: %w", err)
	}

	// Query users
	rows, err := pool.Query(ctx, `
		SELECT id, email, phone, email_confirmed_at, phone_confirmed_at,
			last_sign_in_at, is_anonymous, CASE WHEN banned_until='infinity'::timestamptz THEN '9999-12-31 23:59:59+00'::timestamptz ELSE banned_until END, created_at, updated_at
		FROM auth.users
		WHERE ($1 = '' OR email ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%')
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, search, perPage, offset)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	var users []AuthUserInfo
	for rows.Next() {
		var u AuthUserInfo
		if err := rows.Scan(&u.ID, &u.Email, &u.Phone, &u.EmailConfirmedAt, &u.PhoneConfirmedAt,
			&u.LastSignInAt, &u.IsAnonymous, &u.BannedUntil, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}

	if users == nil {
		users = []AuthUserInfo{}
	}

	return &AuthUserListResponse{
		Users:   users,
		Total:   total,
		Page:    page,
		PerPage: perPage,
	}, http.StatusOK, nil
}

// GetAuthUser returns detailed information about a specific auth user, including sessions.
func (s *AuthUserService) GetAuthUser(ctx context.Context, projectID, userID string) (*AuthUserDetail, int, error) {
	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("project not found")
	}

	var detail AuthUserDetail
	err = pool.QueryRow(ctx, `
		SELECT id, email, phone, email_confirmed_at, phone_confirmed_at,
			last_sign_in_at, is_anonymous, CASE WHEN banned_until='infinity'::timestamptz THEN '9999-12-31 23:59:59+00'::timestamptz ELSE banned_until END,
			raw_app_meta_data, raw_user_meta_data, created_at, updated_at
		FROM auth.users WHERE id = $1
	`, userID).Scan(
		&detail.ID, &detail.Email, &detail.Phone,
		&detail.EmailConfirmedAt, &detail.PhoneConfirmedAt,
		&detail.LastSignInAt, &detail.IsAnonymous, &detail.BannedUntil,
		&detail.AppMetadata, &detail.UserMetadata,
		&detail.CreatedAt, &detail.UpdatedAt,
	)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("user not found")
	}

	// Query user sessions
	sessionRows, err := pool.Query(ctx, `
		SELECT id, created_at, updated_at, user_agent, ip
		FROM auth.sessions WHERE user_id = $1
		ORDER BY created_at DESC LIMIT 20
	`, userID)
	if err != nil {
		slog.Warn("failed to query sessions for user", "user_id", userID, "error", err)
		detail.Sessions = []AuthSessionInfo{}
		return &detail, http.StatusOK, nil
	}
	defer sessionRows.Close()

	var sessions []AuthSessionInfo
	for sessionRows.Next() {
		var sess AuthSessionInfo
		if err := sessionRows.Scan(&sess.ID, &sess.CreatedAt, &sess.UpdatedAt, &sess.UserAgent, &sess.IP); err != nil {
			slog.Warn("failed to scan session", "user_id", userID, "error", err)
			continue
		}
		sessions = append(sessions, sess)
	}

	if sessions == nil {
		sessions = []AuthSessionInfo{}
	}
	detail.Sessions = sessions

	return &detail, http.StatusOK, nil
}

// DeleteAuthUser deletes an auth user by ID. CASCADE handles sessions, refresh_tokens, and identities.
func (s *AuthUserService) DeleteAuthUser(ctx context.Context, projectID, userID string) (int, error) {
	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("project not found")
	}

	tag, err := pool.Exec(ctx, `DELETE FROM auth.users WHERE id = $1`, userID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("delete user: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return http.StatusNotFound, fmt.Errorf("user not found")
	}

	slog.Info("auth user deleted", "project_id", projectID, "user_id", userID)
	return http.StatusOK, nil
}

// BanAuthUser bans an auth user indefinitely by setting banned_until to infinity.
func (s *AuthUserService) BanAuthUser(ctx context.Context, projectID, userID string) (int, error) {
	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("project not found")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE auth.users SET banned_until = 'infinity', updated_at = NOW() WHERE id = $1
	`, userID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("ban user: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return http.StatusNotFound, fmt.Errorf("user not found")
	}

	slog.Info("auth user banned", "project_id", projectID, "user_id", userID)
	if _, err := tx.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked=true,updated_at=NOW() WHERE user_id=$1`, userID); err != nil {
		return http.StatusInternalServerError, err
	}
	if err := tx.Commit(ctx); err != nil {
		return http.StatusInternalServerError, err
	}
	return http.StatusOK, nil
}

// UnbanAuthUser removes a ban from an auth user by setting banned_until to NULL.
func (s *AuthUserService) UnbanAuthUser(ctx context.Context, projectID, userID string) (int, error) {
	pool, err := s.getProjectPool(ctx, projectID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("project not found")
	}

	tag, err := pool.Exec(ctx, `
		UPDATE auth.users SET banned_until = NULL, updated_at = NOW() WHERE id = $1
	`, userID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("unban user: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return http.StatusNotFound, fmt.Errorf("user not found")
	}

	slog.Info("auth user unbanned", "project_id", projectID, "user_id", userID)
	return http.StatusOK, nil
}
