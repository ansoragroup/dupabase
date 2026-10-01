package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/cryptocompat/bcrypt"
	"github.com/ansoraGROUP/dupabase/internal/database"
	"github.com/ansoraGROUP/dupabase/internal/httputil"
	"github.com/ansoraGROUP/dupabase/internal/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dummyProjectHash is used for timing-safe login — prevents user enumeration via timing.
var dummyProjectHash []byte

// gotrueCleanupStop signals the cleanup goroutine to shut down.
var gotrueCleanupStop chan struct{}

func init() {
	var err error
	dummyProjectHash, err = bcrypt.GenerateFromPassword([]byte("timing-safe-dummy-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic("failed to generate dummy bcrypt hash: " + err.Error())
	}

	gotrueCleanupStop = make(chan struct{})

	// Periodically clean up stale login attempt entries to prevent unbounded map growth.
	go func() {
		ticker := time.NewTicker(lockoutDuration)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				loginAttemptsMu.Lock()
				now := time.Now()
				for email, a := range loginAttempts {
					if now.Sub(a.lockedAt) >= lockoutDuration {
						delete(loginAttempts, email)
					}
				}
				loginAttemptsMu.Unlock()
			case <-gotrueCleanupStop:
				return
			}
		}
	}()
}

// StopGoTrueCleanup signals the login attempt cleanup goroutine to stop.
func StopGoTrueCleanup() {
	close(gotrueCleanupStop)
}

// ---------- Per-email brute-force protection ----------

type loginAttempt struct {
	count    int
	lockedAt time.Time
}

var (
	loginAttempts   = make(map[string]*loginAttempt)
	loginAttemptsMu sync.Mutex
)

const (
	maxLoginAttempts = 5
	lockoutDuration  = 15 * time.Minute
)

func isEmailLocked(email string) bool {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	a, ok := loginAttempts[email]
	if !ok {
		return false
	}
	if a.count >= maxLoginAttempts && time.Since(a.lockedAt) < lockoutDuration {
		return true
	}
	if a.count >= maxLoginAttempts && time.Since(a.lockedAt) >= lockoutDuration {
		delete(loginAttempts, email)
		return false
	}
	return false
}

func recordFailedLogin(email string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	a, ok := loginAttempts[email]
	if !ok {
		a = &loginAttempt{}
		loginAttempts[email] = a
	}
	a.count++
	if a.count >= maxLoginAttempts {
		a.lockedAt = time.Now()
	}
}

func clearLoginAttempts(email string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, email)
}

// isValidEmail checks that the email address is well-formed per RFC 5322.
func isValidEmail(email string) bool {
	_, err := mail.ParseAddress(email)
	return err == nil
}

// Handler implements GoTrue-compatible auth endpoints.
type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

// ---------- Request/Response types ----------

type signupRequest struct {
	Email    string                 `json:"email"`
	Password string                 `json:"password"`
	Data     map[string]interface{} `json:"data,omitempty"`
	Phone    string                 `json:"phone,omitempty"`
}

type tokenRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	RefreshToken string `json:"refresh_token"`
	Phone        string `json:"phone,omitempty"`
}

type updateUserRequest struct {
	Email           string                 `json:"email,omitempty"`
	Password        string                 `json:"password,omitempty"`
	CurrentPassword string                 `json:"current_password,omitempty"`
	Data            map[string]interface{} `json:"data,omitempty"`
}

type logoutRequest struct {
	Scope string `json:"scope,omitempty"` // "global" or "local" (default)
}

type userResponse struct {
	ID               string                 `json:"id"`
	Aud              string                 `json:"aud"`
	Role             string                 `json:"role"`
	Email            string                 `json:"email"`
	EmailConfirmedAt *string                `json:"email_confirmed_at"`
	Phone            string                 `json:"phone"`
	PhoneConfirmedAt *string                `json:"phone_confirmed_at,omitempty"`
	ConfirmedAt      *string                `json:"confirmed_at"`
	LastSignInAt     *string                `json:"last_sign_in_at"`
	AppMetadata      map[string]interface{} `json:"app_metadata"`
	UserMetadata     map[string]interface{} `json:"user_metadata"`
	Identities       []identityResponse     `json:"identities"`
	IsAnonymous      bool                   `json:"is_anonymous"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`
}

type identityResponse struct {
	IdentityID   string                 `json:"identity_id"`
	ID           string                 `json:"id"`
	UserID       string                 `json:"user_id"`
	IdentityData map[string]interface{} `json:"identity_data"`
	Provider     string                 `json:"provider"`
	LastSignInAt *string                `json:"last_sign_in_at"`
	CreatedAt    string                 `json:"created_at"`
	UpdatedAt    string                 `json:"updated_at"`
}

type sessionResponse struct {
	AccessToken  string       `json:"access_token"`
	TokenType    string       `json:"token_type"`
	ExpiresIn    int          `json:"expires_in"`
	ExpiresAt    int64        `json:"expires_at"`
	RefreshToken string       `json:"refresh_token"`
	User         userResponse `json:"user"`
}

// ---------- Handlers ----------

// Signup handles POST /auth/v1/signup
// Supports both email+password signup and anonymous sign-in (empty body).
func (h *Handler) Signup(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "missing project context")
		return
	}

	if !project.EnableSignup {
		writeError(w, http.StatusForbidden, "signups are disabled for this project")
		return
	}

	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		req = signupRequest{}
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()
	now := time.Now()

	// Anonymous sign-in: no email and no password provided
	if email == "" && req.Password == "" {
		h.signupAnonymous(ctx, w, r, project, pool, req.Data, now)
		return
	}

	// Email+password signup
	if email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if !isValidEmail(email) {
		writeError(w, http.StatusUnprocessableEntity, "invalid email format")
		return
	}
	if len(req.Password) < project.PasswordMinLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", project.PasswordMinLen))
		return
	}
	if len(req.Password) > 72 {
		writeError(w, http.StatusBadRequest, "password must not exceed 72 characters")
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	userMetadata := req.Data
	if userMetadata == nil {
		userMetadata = map[string]interface{}{}
	}
	appMetadata := map[string]interface{}{"provider": "email", "providers": []string{"email"}}
	userMetaJSON, err := json.Marshal(userMetadata)
	if err != nil {
		slog.Error("failed to marshal user metadata", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	appMetaJSON, err := json.Marshal(appMetadata)
	if err != nil {
		slog.Error("failed to marshal app metadata", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var emailConfirmedAt *time.Time
	if project.Autoconfirm {
		emailConfirmedAt = &now
	}

	// Insert user
	var userID string
	var createdAt, updatedAt time.Time
	err = pool.QueryRow(ctx, `
		INSERT INTO auth.users (email, encrypted_password, email_confirmed_at,
			raw_app_meta_data, raw_user_meta_data, aud, role, last_sign_in_at)
		VALUES ($1, $2, $3, $4, $5, 'authenticated', 'authenticated', $6)
		RETURNING id, created_at, updated_at
	`, email, string(hash), emailConfirmedAt, string(appMetaJSON), string(userMetaJSON), emailConfirmedAt,
	).Scan(&userID, &createdAt, &updatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusBadRequest, "User already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// Create identity
	identityData := map[string]interface{}{
		"sub":            userID,
		"email":          email,
		"email_verified": project.Autoconfirm,
		"phone_verified": false,
	}
	identityJSON, err := json.Marshal(identityData)
	if err != nil {
		slog.Error("failed to marshal identity data", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var identityID string
	err = pool.QueryRow(ctx, `
		INSERT INTO auth.identities (user_id, provider_id, identity_data, provider, last_sign_in_at)
		VALUES ($1, $2, $3, 'email', $4)
		RETURNING id
	`, userID, userID, string(identityJSON), emailConfirmedAt).Scan(&identityID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create identity")
		return
	}

	// Create session and return tokens
	session := &sessionData{}
	if project.Autoconfirm {
		session, err = createSession(ctx, pool, project, userID, email, userMetadata, appMetadata, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create session")
			return
		}
	}

	var emailConfStr *string
	if emailConfirmedAt != nil {
		s := emailConfirmedAt.Format(time.RFC3339)
		emailConfStr = &s
	}
	lastSignIn := now.Format(time.RFC3339)

	resp := sessionResponse{
		AccessToken:  session.accessToken,
		TokenType:    "bearer",
		ExpiresIn:    session.expiresIn,
		ExpiresAt:    session.expiresAt,
		RefreshToken: session.refreshToken,
		User: userResponse{
			ID:               userID,
			Aud:              "authenticated",
			Role:             "authenticated",
			Email:            email,
			EmailConfirmedAt: emailConfStr,
			Phone:            "",
			ConfirmedAt:      emailConfStr,
			LastSignInAt:     &lastSignIn,
			AppMetadata:      appMetadata,
			UserMetadata:     userMetadata,
			Identities: []identityResponse{{
				IdentityID:   identityID,
				ID:           userID,
				UserID:       userID,
				IdentityData: identityData,
				Provider:     "email",
				LastSignInAt: &lastSignIn,
				CreatedAt:    createdAt.Format(time.RFC3339),
				UpdatedAt:    updatedAt.Format(time.RFC3339),
			}},
			CreatedAt: createdAt.Format(time.RFC3339),
			UpdatedAt: updatedAt.Format(time.RFC3339),
		},
	}

	if !project.Autoconfirm {
		resp.User.LastSignInAt = nil
		resp.User.Identities[0].LastSignInAt = nil
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// signupAnonymous creates an anonymous user session (Supabase signInAnonymously).
// Matches real Supabase GoTrue: NULL email, empty app_metadata, no identity rows,
// NULL confirmed_at, amr method "anonymous", is_anonymous claim in JWT.
func (h *Handler) signupAnonymous(ctx context.Context, w http.ResponseWriter, r *http.Request, project *database.ProjectRecord, pool *pgxpool.Pool, data map[string]interface{}, now time.Time) {
	userMetadata := data
	if userMetadata == nil {
		userMetadata = map[string]interface{}{}
	}
	// Real Supabase: app_metadata is empty {} for anonymous users (no provider/providers)
	appMetadata := map[string]interface{}{}
	userMetaJSON, err := json.Marshal(userMetadata)
	if err != nil {
		slog.Error("failed to marshal user metadata", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	appMetaJSON, err := json.Marshal(appMetadata)
	if err != nil {
		slog.Error("failed to marshal app metadata", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Insert anonymous user: email=NULL (not ''), no password, is_anonymous=true
	// NULL emails don't violate unique constraints in PostgreSQL
	// confirmed_at and email_confirmed_at are NULL (anonymous users are not "confirmed")
	var userID string
	var createdAt, updatedAt time.Time
	err = pool.QueryRow(ctx, `
		INSERT INTO auth.users (encrypted_password,
			raw_app_meta_data, raw_user_meta_data, aud, role, is_anonymous, last_sign_in_at)
		VALUES ('', $1, $2, 'authenticated', 'authenticated', true, $3)
		RETURNING id, created_at, updated_at
	`, string(appMetaJSON), string(userMetaJSON), now,
	).Scan(&userID, &createdAt, &updatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create anonymous user")
		return
	}

	// Create session (pass isAnonymous=true for correct JWT claims)
	session, err := createSessionAnonymous(ctx, pool, project, userID, userMetadata, appMetadata, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	lastSignIn := now.Format(time.RFC3339)

	resp := sessionResponse{
		AccessToken:  session.accessToken,
		TokenType:    "bearer",
		ExpiresIn:    session.expiresIn,
		ExpiresAt:    session.expiresAt,
		RefreshToken: session.refreshToken,
		User: userResponse{
			ID:               userID,
			Aud:              "authenticated",
			Role:             "authenticated",
			Email:            "",
			EmailConfirmedAt: nil,
			Phone:            "",
			ConfirmedAt:      nil, // anonymous users have NULL confirmed_at
			LastSignInAt:     &lastSignIn,
			AppMetadata:      appMetadata,
			UserMetadata:     userMetadata,
			Identities:       []identityResponse{},
			IsAnonymous:      true,
			CreatedAt:        createdAt.Format(time.RFC3339),
			UpdatedAt:        updatedAt.Format(time.RFC3339),
		},
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// Token handles POST /auth/v1/token?grant_type=...
func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "missing project context")
		return
	}

	grantType := r.URL.Query().Get("grant_type")
	ctx := r.Context()

	switch grantType {
	case "password":
		h.tokenPassword(ctx, w, r, project, pool)
	case "refresh_token":
		h.tokenRefresh(ctx, w, r, project, pool)
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported grant_type: %s", grantType))
	}
}

func (h *Handler) tokenPassword(ctx contextType, w http.ResponseWriter, r *http.Request, project *database.ProjectRecord, pool *pgxpool.Pool) {
	var req tokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Per-email brute-force protection
	lockoutKey := project.ID + "\x00" + email
	if isEmailLocked(lockoutKey) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	var userID, passwordHash string
	var emailConfirmedAt *time.Time
	var rawAppMeta, rawUserMeta []byte
	var createdAt, updatedAt time.Time
	var banned bool

	err := pool.QueryRow(ctx, `
		SELECT id, encrypted_password, email_confirmed_at,
			raw_app_meta_data, raw_user_meta_data, created_at, updated_at,
			COALESCE(banned_until > NOW(),false)
		FROM auth.users WHERE email = $1 AND deleted_at IS NULL
	`, email).Scan(&userID, &passwordHash, &emailConfirmedAt, &rawAppMeta, &rawUserMeta, &createdAt, &updatedAt, &banned)
	if err != nil {
		// Perform dummy bcrypt comparison to prevent user enumeration via timing
		_ = bcrypt.CompareHashAndPassword(dummyProjectHash, []byte(req.Password)) // timing equalization — always fails
		recordFailedLogin(lockoutKey)
		writeError(w, http.StatusBadRequest, "Invalid login credentials")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		recordFailedLogin(lockoutKey)
		writeError(w, http.StatusBadRequest, "Invalid login credentials")
		return
	}

	// Successful login — clear any failed attempt tracking
	clearLoginAttempts(lockoutKey)
	if banned {
		writeError(w, http.StatusForbidden, "User is banned")
		return
	}
	if emailConfirmedAt == nil && !project.Autoconfirm {
		writeError(w, http.StatusBadRequest, "Email not confirmed")
		return
	}

	var appMetadata, userMetadata map[string]interface{}
	if err := json.Unmarshal(rawAppMeta, &appMetadata); err != nil {
		slog.Warn("failed to unmarshal app_metadata", "user_id", userID, "error", err)
	}
	if err := json.Unmarshal(rawUserMeta, &userMetadata); err != nil {
		slog.Warn("failed to unmarshal user_metadata", "user_id", userID, "error", err)
	}

	// Update last_sign_in_at
	now := time.Now()
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET last_sign_in_at = $1, updated_at = $1 WHERE id = $2`, now, userID); err != nil {
		slog.Error("failed to update last_sign_in_at", "error", err, "user_id", userID)
	}

	session, err := createSession(ctx, pool, project, userID, email, userMetadata, appMetadata, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	var emailConfStr *string
	if emailConfirmedAt != nil {
		s := emailConfirmedAt.Format(time.RFC3339)
		emailConfStr = &s
	}
	lastSignIn := now.Format(time.RFC3339)

	// Get identities
	identities := fetchIdentities(ctx, pool, userID)

	resp := sessionResponse{
		AccessToken:  session.accessToken,
		TokenType:    "bearer",
		ExpiresIn:    session.expiresIn,
		ExpiresAt:    session.expiresAt,
		RefreshToken: session.refreshToken,
		User: userResponse{
			ID:               userID,
			Aud:              "authenticated",
			Role:             "authenticated",
			Email:            email,
			EmailConfirmedAt: emailConfStr,
			Phone:            "",
			ConfirmedAt:      emailConfStr,
			LastSignInAt:     &lastSignIn,
			AppMetadata:      appMetadata,
			UserMetadata:     userMetadata,
			Identities:       identities,
			CreatedAt:        createdAt.Format(time.RFC3339),
			UpdatedAt:        updatedAt.Format(time.RFC3339),
		},
	}

	httputil.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) tokenRefresh(ctx contextType, w http.ResponseWriter, r *http.Request, project *database.ProjectRecord, pool *pgxpool.Pool) {
	var req tokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	// Look up the refresh token (enforce expiration)
	var tokenID int64
	var userID, sessionID string
	var revoked bool
	err := pool.QueryRow(ctx, `
		SELECT id, user_id, session_id, revoked
		FROM auth.refresh_tokens
		WHERE token = $1 AND (expires_at IS NULL OR expires_at > NOW())
	`, req.RefreshToken).Scan(&tokenID, &userID, &sessionID, &revoked)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid refresh token")
		return
	}
	if revoked {
		// Revoke all tokens in this family (rotation attack detection)
		if _, err := pool.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked = true WHERE session_id = $1`, sessionID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to revoke compromised tokens")
			return
		}
		writeError(w, http.StatusBadRequest, "Token has been revoked")
		return
	}

	// Get user data
	var emailPtr *string
	var emailConfirmedAt *time.Time
	var rawAppMeta, rawUserMeta []byte
	var createdAt, updatedAt time.Time
	var banned, isAnonymous bool
	err = pool.QueryRow(ctx, `
		SELECT email, email_confirmed_at, raw_app_meta_data, raw_user_meta_data, created_at, updated_at,
			COALESCE(banned_until > NOW(),false),is_anonymous
		FROM auth.users WHERE id = $1 AND deleted_at IS NULL
	`, userID).Scan(&emailPtr, &emailConfirmedAt, &rawAppMeta, &rawUserMeta, &createdAt, &updatedAt, &banned, &isAnonymous)
	if err != nil {
		writeError(w, http.StatusBadRequest, "User not found")
		return
	}
	email := ""
	if emailPtr != nil {
		email = *emailPtr
	}
	if banned {
		writeError(w, http.StatusForbidden, "User is banned")
		return
	}
	if !isAnonymous && emailConfirmedAt == nil && !project.Autoconfirm {
		writeError(w, http.StatusBadRequest, "Email not confirmed")
		return
	}

	var appMetadata, userMetadata map[string]interface{}
	if err := json.Unmarshal(rawAppMeta, &appMetadata); err != nil {
		slog.Warn("failed to unmarshal app_metadata", "user_id", userID, "error", err)
	}
	if err := json.Unmarshal(rawUserMeta, &userMetadata); err != nil {
		slog.Warn("failed to unmarshal user_metadata", "user_id", userID, "error", err)
	}

	// Generate new tokens
	now := time.Now()
	method := "password"
	if isAnonymous {
		method = "anonymous"
	}
	accessToken, expiresAt, expiresAtTime, err := generateUserJWTFull(project.JWTSecret, project.SiteURL, userID, email, userMetadata, appMetadata, sessionID, isAnonymous, method)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	newRefreshToken, err := generateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate refresh token")
		return
	}

	// Atomic token rotation: revoke old + insert new in a single transaction.
	// If either fails, the old token remains valid and the user can retry.
	tx, err := pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(ctx)

	// Revoke old token inside tx
	tag, err := tx.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked = true, updated_at = NOW()
		WHERE id = $1 AND revoked = false AND (expires_at IS NULL OR expires_at > NOW())`, tokenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke old token")
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, http.StatusBadRequest, "Token has been revoked")
		return
	}

	// Store new refresh token inside tx
	_, err = tx.Exec(ctx, `
		INSERT INTO auth.refresh_tokens (token, user_id, session_id, parent)
		VALUES ($1, $2, $3, $4)
	`, newRefreshToken, userID, sessionID, req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store refresh token")
		return
	}

	// Update session inside tx
	if _, err := tx.Exec(ctx, `UPDATE auth.sessions SET refreshed_at = $1, updated_at = $1 WHERE id = $2`, now, sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update session")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit token refresh")
		return
	}

	var emailConfStr *string
	if emailConfirmedAt != nil {
		s := emailConfirmedAt.Format(time.RFC3339)
		emailConfStr = &s
	}

	identities := fetchIdentities(ctx, pool, userID)

	resp := sessionResponse{
		AccessToken:  accessToken,
		TokenType:    "bearer",
		ExpiresIn:    int(time.Until(expiresAtTime).Seconds()),
		ExpiresAt:    expiresAt,
		RefreshToken: newRefreshToken,
		User: userResponse{
			ID:               userID,
			Aud:              "authenticated",
			Role:             "authenticated",
			Email:            email,
			EmailConfirmedAt: emailConfStr,
			Phone:            "",
			ConfirmedAt:      emailConfStr,
			AppMetadata:      appMetadata,
			UserMetadata:     userMetadata,
			Identities:       identities,
			CreatedAt:        createdAt.Format(time.RFC3339),
			UpdatedAt:        updatedAt.Format(time.RFC3339),
		},
	}

	resp.User.IsAnonymous = isAnonymous
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// GetUser handles GET /auth/v1/user
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "missing project context")
		return
	}

	// Get user from Authorization header (user JWT, not apikey)
	userID, err := extractUserFromAuth(r, project.JWTSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	ctx := r.Context()
	if !database.UserSessionEligible(ctx, pool, userID, project.Autoconfirm) {
		writeError(w, http.StatusUnauthorized, "user session is inactive")
		return
	}

	user, err := fetchUser(ctx, pool, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, user)
}

// UpdateUser handles PUT /auth/v1/user
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "missing project context")
		return
	}

	userID, err := extractUserFromAuth(r, project.JWTSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	if !database.UserSessionEligible(ctx, pool, userID, project.Autoconfirm) {
		writeError(w, http.StatusUnauthorized, "user session is inactive")
		return
	}

	// Validate password constraints before starting a transaction
	if req.Password != "" {
		if len(req.Password) < project.PasswordMinLen {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("password must be at least %d characters", project.PasswordMinLen))
			return
		}
		if len(req.Password) > 72 {
			writeError(w, http.StatusUnprocessableEntity, "password must not exceed 72 characters")
			return
		}
		if req.CurrentPassword == "" {
			writeError(w, http.StatusBadRequest, "current_password is required to change password")
			return
		}
	}

	// Begin transaction for atomicity
	tx, err := pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(ctx)

	if req.Password != "" {
		// Fetch and lock the user row to prevent TOCTOU race between verification and update
		var storedHash string
		err := tx.QueryRow(ctx, `SELECT encrypted_password FROM auth.users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&storedHash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify current password")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.CurrentPassword)); err != nil {
			writeError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE auth.users SET encrypted_password = $1, updated_at = NOW() WHERE id = $2`, string(hash), userID); err != nil {
			slog.Error("failed to update password", "error", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "failed to update password")
			return
		}
	}

	if req.Data != nil {
		metaJSON, err := json.Marshal(req.Data)
		if err != nil {
			slog.Error("failed to marshal user metadata", "error", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE auth.users SET raw_user_meta_data = $1, updated_at = NOW() WHERE id = $2`, string(metaJSON), userID); err != nil {
			slog.Error("failed to update user metadata", "error", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "failed to update user metadata")
			return
		}
	}

	if req.Email != "" {
		newEmail := strings.ToLower(strings.TrimSpace(req.Email))
		if !isValidEmail(newEmail) {
			writeError(w, http.StatusUnprocessableEntity, "invalid email format")
			return
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.users WHERE email = $1 AND id != $2)`, newEmail, userID).Scan(&exists); err != nil {
			slog.Error("failed to check email uniqueness", "error", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "failed to check email")
			return
		}
		if exists {
			writeError(w, http.StatusBadRequest, "email already in use")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE auth.users SET email = $1::text,
			email_confirmed_at = CASE WHEN email=$1::text THEN email_confirmed_at WHEN $3 THEN NOW() ELSE NULL END,
			updated_at = NOW() WHERE id = $2`, newEmail, userID, project.Autoconfirm); err != nil {
			slog.Error("failed to update email", "error", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "failed to update email")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE auth.identities SET identity_data = identity_data || jsonb_build_object('email',$1::text,'email_verified',$3::boolean),updated_at=NOW() WHERE user_id=$2 AND provider='email'`, newEmail, userID, project.Autoconfirm); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update identity")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit changes")
		return
	}

	user, err := fetchUser(ctx, pool, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch updated user")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, user)
}

// Logout handles POST /auth/v1/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "missing project context")
		return
	}

	userID, err := extractUserFromAuth(r, project.JWTSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req logoutRequest
	// Intentionally ignoring decode error: empty/malformed body defaults to local scope logout.
	json.NewDecoder(r.Body).Decode(&req)

	ctx := r.Context()

	if req.Scope == "global" {
		// Revoke all sessions
		if _, err := pool.Exec(ctx, `DELETE FROM auth.sessions WHERE user_id = $1`, userID); err != nil {
			slog.Error("failed to delete sessions on global logout", "error", err, "user_id", userID)
		}
		if _, err := pool.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked = true WHERE user_id = $1`, userID); err != nil {
			slog.Error("failed to revoke refresh tokens on global logout", "error", err, "user_id", userID)
		}
	} else {
		// Revoke current session only
		sessionID, _ := extractSessionFromAuth(r, project.JWTSecret)
		if sessionID != "" {
			if _, err := pool.Exec(ctx, `DELETE FROM auth.sessions WHERE id = $1`, sessionID); err != nil {
				slog.Error("failed to delete session on logout", "error", err, "session_id", sessionID)
			}
			if _, err := pool.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked = true WHERE session_id = $1`, sessionID); err != nil {
				slog.Error("failed to revoke refresh tokens on logout", "error", err, "session_id", sessionID)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------- Internal helpers ----------

type contextType = context.Context

type sessionData struct {
	accessToken  string
	refreshToken string
	expiresAt    int64
	expiresIn    int
}

func createSessionAnonymous(ctx contextType, pool *pgxpool.Pool, project *database.ProjectRecord, userID string, userMeta, appMeta map[string]interface{}, r *http.Request) (*sessionData, error) {
	var sessionID string
	userAgent := r.Header.Get("User-Agent")
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr // fallback if no port
	}
	err := pool.QueryRow(ctx, `
		INSERT INTO auth.sessions (user_id, user_agent, ip, aal)
		VALUES ($1, $2, $3, 'aal1')
		RETURNING id
	`, userID, userAgent, ip).Scan(&sessionID)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	accessToken, expiresAt, expiresAtTime, err := generateUserJWTFull(project.JWTSecret, project.SiteURL, userID, "", userMeta, appMeta, sessionID, true, "anonymous")
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}

	refreshToken, err := generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO auth.refresh_tokens (token, user_id, session_id)
		VALUES ($1, $2, $3)
	`, refreshToken, userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}

	return &sessionData{
		accessToken:  accessToken,
		refreshToken: refreshToken,
		expiresAt:    expiresAt,
		expiresIn:    int(time.Until(expiresAtTime).Seconds()),
	}, nil
}

func createSession(ctx contextType, pool *pgxpool.Pool, project *database.ProjectRecord, userID, email string, userMeta, appMeta map[string]interface{}, r *http.Request) (*sessionData, error) {
	// Create session record
	var sessionID string
	userAgent := r.Header.Get("User-Agent")
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr // fallback if no port
	}
	err := pool.QueryRow(ctx, `
		INSERT INTO auth.sessions (user_id, user_agent, ip, aal)
		VALUES ($1, $2, $3, 'aal1')
		RETURNING id
	`, userID, userAgent, ip).Scan(&sessionID)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Generate access token
	accessToken, expiresAt, expiresAtTime, err := generateUserJWT(project.JWTSecret, project.SiteURL, userID, email, userMeta, appMeta, sessionID)
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}

	// Generate refresh token
	refreshToken, err := generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}

	// Store refresh token
	_, err = pool.Exec(ctx, `
		INSERT INTO auth.refresh_tokens (token, user_id, session_id)
		VALUES ($1, $2, $3)
	`, refreshToken, userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}

	return &sessionData{
		accessToken:  accessToken,
		refreshToken: refreshToken,
		expiresAt:    expiresAt,
		expiresIn:    int(time.Until(expiresAtTime).Seconds()),
	}, nil
}

func generateUserJWT(jwtSecret, siteURL, userID, email string, userMeta, appMeta map[string]interface{}, sessionID string) (string, int64, time.Time, error) {
	return generateUserJWTFull(jwtSecret, siteURL, userID, email, userMeta, appMeta, sessionID, false, "password")
}

func generateUserJWTFull(jwtSecret, siteURL, userID, email string, userMeta, appMeta map[string]interface{}, sessionID string, isAnonymous bool, amrMethod string) (string, int64, time.Time, error) {
	now := time.Now()
	expiresAtTime := now.Add(1 * time.Hour)
	expiresAt := expiresAtTime.Unix()

	claims := jwt.MapClaims{
		"aud":           "authenticated",
		"exp":           expiresAt,
		"iat":           now.Unix(),
		"iss":           siteURL + "/auth/v1",
		"sub":           userID,
		"email":         email,
		"phone":         "",
		"app_metadata":  appMeta,
		"user_metadata": userMeta,
		"role":          "authenticated",
		"aal":           "aal1",
		"amr":           []map[string]interface{}{{"method": amrMethod, "timestamp": now.Unix()}},
		"session_id":    sessionID,
		"is_anonymous":  isAnonymous,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(jwtSecret))
	return signed, expiresAt, expiresAtTime, err
}

func generateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func extractUserFromAuth(r *http.Request, jwtSecret string) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing authorization")
	}
	tokenStr := strings.TrimPrefix(auth, "Bearer ")

	// Check if the token is same as apikey (unauthenticated)
	apikey := r.Header.Get("apikey")
	if tokenStr == apikey {
		return "", fmt.Errorf("not a user token")
	}

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("invalid claims")
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", fmt.Errorf("missing sub claim")
	}

	return sub, nil
}

func extractSessionFromAuth(r *http.Request, jwtSecret string) (string, error) {
	auth := r.Header.Get("Authorization")
	tokenStr := strings.TrimPrefix(auth, "Bearer ")

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return "", err
	}

	claims, _ := token.Claims.(jwt.MapClaims)
	sid, _ := claims["session_id"].(string)
	return sid, nil
}

func fetchUser(ctx contextType, pool *pgxpool.Pool, userID string) (*userResponse, error) {
	var email *string
	var emailConfirmedAt *time.Time
	var lastSignInAt *time.Time
	var rawAppMeta, rawUserMeta []byte
	var createdAt, updatedAt time.Time
	var phone *string
	var isAnonymous bool

	err := pool.QueryRow(ctx, `
		SELECT email, email_confirmed_at, last_sign_in_at,
			raw_app_meta_data, raw_user_meta_data, created_at, updated_at, phone, is_anonymous
		FROM auth.users WHERE id = $1 AND deleted_at IS NULL
	`, userID).Scan(&email, &emailConfirmedAt, &lastSignInAt, &rawAppMeta, &rawUserMeta, &createdAt, &updatedAt, &phone, &isAnonymous)
	if err != nil {
		return nil, err
	}

	var appMeta, userMeta map[string]interface{}
	if err := json.Unmarshal(rawAppMeta, &appMeta); err != nil {
		slog.Warn("failed to unmarshal app_metadata", "user_id", userID, "error", err)
	}
	if err := json.Unmarshal(rawUserMeta, &userMeta); err != nil {
		slog.Warn("failed to unmarshal user_metadata", "user_id", userID, "error", err)
	}

	var emailConfStr, lastSignStr, confirmedStr *string
	if emailConfirmedAt != nil {
		s := emailConfirmedAt.Format(time.RFC3339)
		emailConfStr = &s
		confirmedStr = &s
	}
	if lastSignInAt != nil {
		s := lastSignInAt.Format(time.RFC3339)
		lastSignStr = &s
	}

	identities := fetchIdentities(ctx, pool, userID)

	phoneStr := ""
	if phone != nil {
		phoneStr = *phone
	}
	emailStr := ""
	if email != nil {
		emailStr = *email
	}

	return &userResponse{
		ID:               userID,
		Aud:              "authenticated",
		Role:             "authenticated",
		Email:            emailStr,
		EmailConfirmedAt: emailConfStr,
		Phone:            phoneStr,
		ConfirmedAt:      confirmedStr,
		LastSignInAt:     lastSignStr,
		AppMetadata:      appMeta,
		UserMetadata:     userMeta,
		Identities:       identities,
		IsAnonymous:      isAnonymous,
		CreatedAt:        createdAt.Format(time.RFC3339),
		UpdatedAt:        updatedAt.Format(time.RFC3339),
	}, nil
}

func fetchIdentities(ctx contextType, pool *pgxpool.Pool, userID string) []identityResponse {
	rows, err := pool.Query(ctx, `
		SELECT id, provider_id, identity_data, provider, last_sign_in_at, created_at, updated_at
		FROM auth.identities WHERE user_id = $1
	`, userID)
	if err != nil {
		return []identityResponse{}
	}
	defer rows.Close()

	var identities []identityResponse
	for rows.Next() {
		var id, providerID, provider string
		var identityData []byte
		var lastSignIn *time.Time
		var createdAt, updatedAt time.Time

		if err := rows.Scan(&id, &providerID, &identityData, &provider, &lastSignIn, &createdAt, &updatedAt); err != nil {
			continue
		}

		var data map[string]interface{}
		json.Unmarshal(identityData, &data)

		var lastSignStr *string
		if lastSignIn != nil {
			s := lastSignIn.Format(time.RFC3339)
			lastSignStr = &s
		}

		identities = append(identities, identityResponse{
			IdentityID:   id,
			ID:           providerID,
			UserID:       userID,
			IdentityData: data,
			Provider:     provider,
			LastSignInAt: lastSignStr,
			CreatedAt:    createdAt.Format(time.RFC3339),
			UpdatedAt:    updatedAt.Format(time.RFC3339),
		})
	}

	if identities == nil {
		identities = []identityResponse{}
	}
	return identities
}

// AdminDeleteUser handles DELETE /auth/v1/admin/users/{id}
// Requires service_role key. Deletes the user and all associated sessions/tokens.
func (h *Handler) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	project := middleware.GetProject(r)
	pool := middleware.GetProjectSQL(r)
	if project == nil || pool == nil {
		writeError(w, http.StatusInternalServerError, "missing project context")
		return
	}

	// Only service_role can call admin endpoints
	role := middleware.GetAPIKeyRole(r)
	if role != "service_role" {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	// Extract user ID from URL path: /auth/v1/admin/users/{id}
	userID := r.PathValue("id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user id is required")
		return
	}

	ctx := r.Context()

	// Verify user exists
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.users WHERE id = $1 AND deleted_at IS NULL)`, userID).Scan(&exists)
	if err != nil || !exists {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}

	// Delete refresh tokens, sessions, identities, then soft-delete user — atomically
	tx, err := pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM auth.refresh_tokens WHERE user_id = $1`, userID); err != nil {
		slog.Error("failed to delete refresh tokens", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth.sessions WHERE user_id = $1`, userID); err != nil {
		slog.Error("failed to delete sessions", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth.identities WHERE user_id = $1`, userID); err != nil {
		slog.Error("failed to delete identities", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE auth.users SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1`, userID); err != nil {
		slog.Error("failed to soft-delete user", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit user deletion")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{})
}

func writeError(w http.ResponseWriter, status int, message string) {
	httputil.WriteJSON(w, status, map[string]interface{}{
		"error":             message,
		"error_description": message,
	})
}
