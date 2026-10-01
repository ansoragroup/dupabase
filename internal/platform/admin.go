package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolCloser is an interface for closing project connection pools.
type PoolCloser interface {
	ClosePool(projectID string)
}

type AdminService struct {
	db          *pgxpool.Pool
	poolManager PoolCloser
}

func NewAdminService(db *pgxpool.Pool, poolManager PoolCloser) *AdminService {
	return &AdminService{db: db, poolManager: poolManager}
}

// AdminUser is the admin-facing view of a user.
type AdminUser struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  *string   `json:"display_name"`
	PgUsername   string    `json:"pg_username"`
	IsAdmin      bool      `json:"is_admin"`
	ProjectCount int       `json:"project_count"`
	CreatedAt    time.Time `json:"created_at"`
}

// PaginatedUsers holds a page of users plus total count.
type PaginatedUsers struct {
	Users   []AdminUser `json:"users"`
	Total   int         `json:"total"`
	Page    int         `json:"page"`
	PerPage int         `json:"per_page"`
}

// ListUsers returns a paginated list of platform users with their project counts.
func (s *AdminService) ListUsers(ctx context.Context, page, perPage int) (*PaginatedUsers, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	offset := (page - 1) * perPage

	// Get total count
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM platform.users`).Scan(&total); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("count users: %w", err)
	}

	rows, err := s.db.Query(ctx, `
		SELECT u.id, u.email, u.display_name, COALESCE(p.pg_username, ''), u.is_admin, u.created_at,
			(SELECT COUNT(*) FROM platform.projects WHERE user_id = u.id AND status != 'deleted')
		FROM platform.users u
		LEFT JOIN platform.pg_users p ON p.user_id = u.id
		ORDER BY u.created_at ASC
		LIMIT $1 OFFSET $2
	`, perPage, offset)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	var users []AdminUser
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PgUsername, &u.IsAdmin, &u.CreatedAt, &u.ProjectCount); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	if users == nil {
		users = []AdminUser{}
	}
	return &PaginatedUsers{Users: users, Total: total, Page: page, PerPage: perPage}, http.StatusOK, nil
}

// DeleteUser deletes a platform user. Cannot delete yourself or other admins.
func (s *AdminService) DeleteUser(ctx context.Context, callerID, targetID string) (int, error) {
	if callerID == targetID {
		return http.StatusBadRequest, fmt.Errorf("cannot delete yourself")
	}

	var isAdmin bool
	err := s.db.QueryRow(ctx, `SELECT is_admin FROM platform.users WHERE id = $1`, targetID).Scan(&isAdmin)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("user not found")
	}
	if isAdmin {
		return http.StatusForbidden, fmt.Errorf("cannot delete an admin user")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("begin user deletion: %w", err)
	}
	defer tx.Rollback(ctx)
	// Prevent new owned organizations/memberships while deletion is checked.
	if err := tx.QueryRow(ctx, `SELECT is_admin FROM platform.users WHERE id=$1 FOR UPDATE`, targetID).Scan(&isAdmin); err != nil {
		return http.StatusNotFound, fmt.Errorf("user not found")
	}
	if isAdmin {
		return http.StatusForbidden, fmt.Errorf("cannot delete an admin user")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM platform.organizations WHERE created_by=$1 FOR UPDATE`, targetID); err != nil {
		return http.StatusInternalServerError, err
	}
	var shared bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.organizations o WHERE o.created_by=$1 AND (EXISTS(SELECT 1 FROM platform.org_members m WHERE m.org_id=o.id AND m.user_id<>$1) OR EXISTS(SELECT 1 FROM platform.projects p WHERE p.org_id=o.id AND p.user_id<>$1)))`, targetID).Scan(&shared); err != nil {
		return http.StatusInternalServerError, err
	}
	if shared {
		return http.StatusBadRequest, fmt.Errorf("cannot delete the owner of an organization with other members")
	}

	// Clean up project databases before deleting the user
	rows, err := s.db.Query(ctx, `SELECT id, db_name FROM platform.projects WHERE user_id = $1`, targetID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("list account projects: %w", err)
	}
	type accountProject struct{ id, dbName string }
	var projects []accountProject
	for rows.Next() {
		var project accountProject
		if err := rows.Scan(&project.id, &project.dbName); err != nil {
			rows.Close()
			return http.StatusInternalServerError, err
		}
		projects = append(projects, project)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return http.StatusInternalServerError, err
	}
	for _, project := range projects {
		if s.poolManager != nil {
			s.poolManager.ClosePool(project.id)
		}
		if _, err := s.db.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, adminQuoteIdent(project.dbName))); err != nil {
			return http.StatusInternalServerError, fmt.Errorf("drop account project database: %w", err)
		}
		if cleaner, ok := s.poolManager.(interface {
			DropProjectLogin(context.Context, string) error
		}); ok {
			if err := cleaner.DropProjectLogin(ctx, project.id); err != nil {
				return http.StatusInternalServerError, fmt.Errorf("drop account project login: %w", err)
			}
		}
	}

	// Clean up PG role
	var pgUsername string
	_ = s.db.QueryRow(ctx, `SELECT pg_username FROM platform.pg_users WHERE user_id = $1`, targetID).Scan(&pgUsername)
	if pgUsername != "" {
		if _, err := s.db.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, adminQuoteIdent(pgUsername))); err != nil {
			return http.StatusInternalServerError, fmt.Errorf("drop account PostgreSQL role: %w", err)
		} else {
			slog.Info("dropped PG role during user deletion", "role", pgUsername, "user_id", targetID)
		}
	}

	// Delete user (CASCADE will clean up pg_users, projects, etc.)
	// Keep the invitation's consumption timestamp while releasing its user FK.
	// Expire it in the same transaction so deleting a user never reopens a code.
	_, err = tx.Exec(ctx, `UPDATE platform.invites SET used_by=NULL, expires_at=LEAST(expires_at,NOW()) WHERE used_by=$1`, targetID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("retire user invitations: %w", err)
	}
	// Account deletion also removes its private workspaces and invitations it
	// issued. Shared workspaces were rejected above before any database cleanup.
	if _, err := tx.Exec(ctx, `DELETE FROM platform.org_invites WHERE invited_by=$1`, targetID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("retire organization invitations: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.projects WHERE user_id=$1`, targetID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("delete account project records: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.organizations WHERE created_by=$1`, targetID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("delete private organizations: %w", err)
	}
	_, err = tx.Exec(ctx, `DELETE FROM platform.users WHERE id = $1`, targetID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("delete user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("commit user deletion: %w", err)
	}
	return http.StatusOK, nil
}

// PlatformSettings holds platform-wide configuration.
type PlatformSettings struct {
	RegistrationMode string `json:"registration_mode"`
}

// GetSettings returns the platform settings.
func (s *AdminService) GetSettings(ctx context.Context) (*PlatformSettings, int, error) {
	var mode string
	err := s.db.QueryRow(ctx, `SELECT value FROM platform.settings WHERE key = 'registration_mode'`).Scan(&mode)
	if err != nil {
		mode = "open"
	}
	return &PlatformSettings{RegistrationMode: mode}, http.StatusOK, nil
}

// UpdateSettings updates the platform settings.
func (s *AdminService) UpdateSettings(ctx context.Context, settings PlatformSettings) (int, error) {
	valid := map[string]bool{"open": true, "invite": true, "disabled": true}
	if !valid[settings.RegistrationMode] {
		return http.StatusBadRequest, fmt.Errorf("registration_mode must be 'open', 'invite', or 'disabled'")
	}

	_, err := s.db.Exec(ctx, `
		INSERT INTO platform.settings (key, value, updated_at) VALUES ('registration_mode', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = NOW()
	`, settings.RegistrationMode)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("update settings: %w", err)
	}
	return http.StatusOK, nil
}

// Invite represents an invitation code.
type Invite struct {
	ID        string     `json:"id"`
	Code      string     `json:"code"`
	Email     *string    `json:"email"`
	CreatedBy string     `json:"created_by"`
	UsedBy    *string    `json:"used_by"`
	UsedAt    *time.Time `json:"used_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type CreateInviteRequest struct {
	Email     string `json:"email,omitempty"`
	ExpiresIn int    `json:"expires_in_hours,omitempty"` // default 72 hours
}

// CreateInvite generates a new invite code.
func (s *AdminService) CreateInvite(ctx context.Context, createdBy string, req CreateInviteRequest) (*Invite, int, error) {
	// Validate email if provided
	if req.Email != "" {
		if _, err := mail.ParseAddress(req.Email); err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid email format")
		}
	}

	// Generate 16-byte hex code
	codeBytes := make([]byte, 16)
	if _, err := rand.Read(codeBytes); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate code: %w", err)
	}
	code := hex.EncodeToString(codeBytes)

	expiresHours := req.ExpiresIn
	if expiresHours <= 0 {
		expiresHours = 72
	}
	expiresAt := time.Now().Add(time.Duration(expiresHours) * time.Hour)

	var email *string
	if req.Email != "" {
		email = &req.Email
	}

	var invite Invite
	err := s.db.QueryRow(ctx, `
		INSERT INTO platform.invites (code, email, created_by, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, code, email, created_by, used_by, used_at, expires_at, created_at
	`, code, email, createdBy, expiresAt).Scan(
		&invite.ID, &invite.Code, &invite.Email, &invite.CreatedBy,
		&invite.UsedBy, &invite.UsedAt, &invite.ExpiresAt, &invite.CreatedAt,
	)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("create invite: %w", err)
	}

	return &invite, http.StatusCreated, nil
}

// ListInvites returns all invites.
func (s *AdminService) ListInvites(ctx context.Context) ([]Invite, int, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, code, email, created_by, used_by, used_at, expires_at, created_at
		FROM platform.invites
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("query invites: %w", err)
	}
	defer rows.Close()

	var invites []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.Code, &inv.Email, &inv.CreatedBy, &inv.UsedBy, &inv.UsedAt, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("scan invite: %w", err)
		}
		invites = append(invites, inv)
	}
	if invites == nil {
		invites = []Invite{}
	}
	return invites, http.StatusOK, nil
}

// DeleteInvite revokes an invite by ID.
func (s *AdminService) DeleteInvite(ctx context.Context, inviteID string) (int, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM platform.invites WHERE id = $1`, inviteID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("delete invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return http.StatusNotFound, fmt.Errorf("invite not found")
	}
	return http.StatusOK, nil
}

// adminQuoteIdent quotes a SQL identifier to prevent injection.
func adminQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
