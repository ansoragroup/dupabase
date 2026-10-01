package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/cryptocompat/bcrypt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgUsernameRegex validates generated PG usernames (u_ + 12 hex chars)
var pgUsernameRegex = regexp.MustCompile(`^u_[a-f0-9]{12}$`)

// quoteLiteral safely quotes a string for use as a PostgreSQL literal.
// Uses the dollar-quoting escape style to prevent injection.
func quoteLiteral(s string) string {
	// Use E'' syntax with proper escaping for maximum safety
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `''`)
	return "E'" + escaped + "'"
}

type AuthService struct {
	db            *pgxpool.Pool
	jwtSecret     []byte
	jwtExpiry     time.Duration
	loginAttempts map[string]*loginAttempt
	attemptsMu    sync.Mutex
	cleanupStop   chan struct{}
	cleanupWg     sync.WaitGroup
}

type loginAttempt struct {
	count    int
	lockedAt time.Time
}

func NewAuthService(db *pgxpool.Pool, jwtSecret string, jwtExpiry int) *AuthService {
	return &AuthService{
		db:            db,
		jwtSecret:     []byte(jwtSecret),
		jwtExpiry:     time.Duration(jwtExpiry) * time.Second,
		loginAttempts: make(map[string]*loginAttempt),
		cleanupStop:   make(chan struct{}),
	}
}

// StartCleanup starts a background goroutine that periodically removes expired login attempts.
func (s *AuthService) StartCleanup() {
	s.cleanupWg.Add(1)
	go func() {
		defer s.cleanupWg.Done()
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.attemptsMu.Lock()
				now := time.Now()
				for email, a := range s.loginAttempts {
					if a.count >= 5 && now.Sub(a.lockedAt) >= 15*time.Minute {
						delete(s.loginAttempts, email)
					}
				}
				s.attemptsMu.Unlock()
			case <-s.cleanupStop:
				return
			}
		}
	}()
}

// StopCleanup signals the cleanup goroutine to stop and waits for it to finish.
func (s *AuthService) StopCleanup() {
	close(s.cleanupStop)
	s.cleanupWg.Wait()
}

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
	InviteCode  string `json:"invite_code,omitempty"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Token string       `json:"token"`
	User  PlatformUser `json:"user"`
}

type PlatformUser struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName *string   `json:"display_name"`
	PgUsername  string    `json:"pg_username,omitempty"`
	IsAdmin     bool      `json:"is_admin"`
	CreatedAt   time.Time `json:"created_at"`
}

type PlatformClaims struct {
	Email string `json:"email"`
	Type  string `json:"type"`
	jwt.RegisteredClaims
}

// Register creates a new platform user and their PostgreSQL role.
func (s *AuthService) Register(ctx context.Context, req RegisterRequest) (*AuthResponse, int, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("email and password are required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid email format")
	}
	if len(req.Password) < 8 {
		return nil, http.StatusBadRequest, fmt.Errorf("password must be at least 8 characters")
	}
	if len(req.Password) > 72 {
		return nil, http.StatusBadRequest, fmt.Errorf("password must not exceed 72 characters")
	}

	// Check registration mode
	mode := s.GetRegistrationMode(ctx)
	if mode == "disabled" {
		return nil, http.StatusForbidden, fmt.Errorf("registration is disabled")
	}

	// Validate invite code if invite-only mode
	var inviteID *string
	if mode == "invite" {
		if req.InviteCode == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("invite code is required")
		}
		var id string
		var usedBy *string
		var usedAt *time.Time
		var targetEmail *string
		var expiresAt time.Time
		err := s.db.QueryRow(ctx, `
			SELECT id, used_by, used_at, expires_at, email FROM platform.invites WHERE code = $1
		`, req.InviteCode).Scan(&id, &usedBy, &usedAt, &expiresAt, &targetEmail)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid invite code")
		}
		if usedBy != nil || usedAt != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invite code already used")
		}
		if time.Now().After(expiresAt) {
			return nil, http.StatusBadRequest, fmt.Errorf("invite code expired")
		}
		if targetEmail != nil && strings.ToLower(strings.TrimSpace(*targetEmail)) != email {
			return nil, http.StatusBadRequest, fmt.Errorf("invite code is for another email")
		}
		inviteID = &id
	}

	// Check if email already exists
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.users WHERE email = $1)`, email).Scan(&exists)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("check email: %w", err)
	}
	if exists {
		return nil, http.StatusConflict, fmt.Errorf("email already registered")
	}

	// Hash platform password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("hash password: %w", err)
	}

	// Generate PG username and password
	randBytes := make([]byte, 6)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate random: %w", err)
	}
	pgUsername := "u_" + hex.EncodeToString(randBytes)

	pgPassword, err := GenerateRandomPassword(32)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate pg password: %w", err)
	}

	// Hash PG password for storage
	pgHash, err := bcrypt.GenerateFromPassword([]byte(pgPassword), 12)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("hash pg password: %w", err)
	}

	// Encrypt PG password with user's platform password
	pgEncrypted, err := EncryptPgPassword(pgPassword, req.Password)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("encrypt pg password: %w", err)
	}

	// Begin transaction
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Insert platform user
	var userID string
	var createdAt time.Time
	displayName := &req.DisplayName
	if req.DisplayName == "" {
		displayName = nil
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO platform.users (email, password_hash, display_name)
		VALUES ($1, $2, $3)
		RETURNING id, created_at
	`, email, string(hash), displayName).Scan(&userID, &createdAt)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("insert user: %w", err)
	}

	// Create PostgreSQL role with LOGIN and CONNECTION LIMIT (no CREATEDB for tenant isolation)
	// pgUsername is safe (generated as "u_" + hex, validated below)
	if !pgUsernameRegex.MatchString(pgUsername) {
		return nil, http.StatusInternalServerError, fmt.Errorf("invalid pg username format")
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`CREATE ROLE "%s" LOGIN CONNECTION LIMIT 10`, pgUsername))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("create pg role: %w", err)
	}
	// Set password separately via ALTER ROLE using format string with escaped password
	// This is safe because pgPassword is randomly generated (alphanumeric only)
	_, err = tx.Exec(ctx, fmt.Sprintf(`ALTER ROLE "%s" PASSWORD %s`, pgUsername, quoteLiteral(pgPassword)))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("create pg role: %w", err)
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`ALTER ROLE "%s" SET statement_timeout = '30s'`, pgUsername))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("set statement timeout: %w", err)
	}

	// Insert pg_user mapping
	_, err = tx.Exec(ctx, `
		INSERT INTO platform.pg_users (user_id, pg_username, pg_password_hash, pg_password_encrypted)
		VALUES ($1, $2, $3, $4)
	`, userID, pgUsername, string(pgHash), pgEncrypted)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("insert pg user: %w", err)
	}

	// Mark invite as used if applicable
	if inviteID != nil {
		tag, consumeErr := tx.Exec(ctx, `
			UPDATE platform.invites SET used_by = $1, used_at = NOW()
			WHERE id = $2 AND used_by IS NULL AND used_at IS NULL AND expires_at > NOW()
			AND (email IS NULL OR lower(trim(email)) = $3)
		`, userID, *inviteID, email)
		err = consumeErr
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("mark invite used: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, http.StatusBadRequest, fmt.Errorf("invite code is no longer valid")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("commit: %w", err)
	}

	// Auto-create personal organization for the new user
	s.createPersonalOrg(ctx, userID)

	// Generate JWT
	token, err := s.generateToken(ctx, userID, email)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate token: %w", err)
	}

	return &AuthResponse{
		Token: token,
		User: PlatformUser{
			ID:          userID,
			Email:       email,
			DisplayName: displayName,
			PgUsername:  pgUsername,
			IsAdmin:     false,
			CreatedAt:   createdAt,
		},
	}, http.StatusCreated, nil
}

// dummyHash is a pre-computed bcrypt hash used for timing-safe login.
// When user is not found, we still run bcrypt comparison to prevent timing attacks.
var dummyHash []byte

func init() {
	var err error
	dummyHash, err = bcrypt.GenerateFromPassword([]byte("timing-safe-dummy-password-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic("failed to generate dummy bcrypt hash: " + err.Error())
	}
}

// Login authenticates a platform user and returns a JWT.
func (s *AuthService) Login(ctx context.Context, req LoginRequest) (*AuthResponse, int, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Check lockout
	s.attemptsMu.Lock()
	attempt := s.loginAttempts[email]
	if attempt != nil && attempt.count >= 5 {
		if time.Since(attempt.lockedAt) < 15*time.Minute {
			s.attemptsMu.Unlock()
			bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password)) // timing-safe
			return nil, http.StatusTooManyRequests, fmt.Errorf("account temporarily locked, try again later")
		}
		// Lock expired, reset
		delete(s.loginAttempts, email)
	}
	s.attemptsMu.Unlock()

	var userID, passwordHash string
	var displayName *string
	var createdAt time.Time
	var isAdmin bool

	err := s.db.QueryRow(ctx, `
		SELECT id, password_hash, display_name, created_at, is_admin
		FROM platform.users WHERE email = $1
	`, email).Scan(&userID, &passwordHash, &displayName, &createdAt, &isAdmin)
	if err != nil {
		// Timing-safe: always run bcrypt even if user doesn't exist
		bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		return nil, http.StatusUnauthorized, fmt.Errorf("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		s.attemptsMu.Lock()
		a := s.loginAttempts[email]
		if a == nil {
			a = &loginAttempt{}
			s.loginAttempts[email] = a
		}
		a.count++
		if a.count >= 5 {
			a.lockedAt = time.Now()
		}
		s.attemptsMu.Unlock()
		return nil, http.StatusUnauthorized, fmt.Errorf("invalid credentials")
	}

	// Clear login attempts on successful login
	s.attemptsMu.Lock()
	delete(s.loginAttempts, email)
	s.attemptsMu.Unlock()

	// Get pg_username
	var pgUsername string
	err = s.db.QueryRow(ctx, `
		SELECT pg_username FROM platform.pg_users WHERE user_id = $1
	`, userID).Scan(&pgUsername)
	if err != nil {
		pgUsername = ""
	}

	token, err := s.generateToken(ctx, userID, email)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate token: %w", err)
	}

	return &AuthResponse{
		Token: token,
		User: PlatformUser{
			ID:          userID,
			Email:       email,
			DisplayName: displayName,
			PgUsername:  pgUsername,
			IsAdmin:     isAdmin,
			CreatedAt:   createdAt,
		},
	}, http.StatusOK, nil
}

// GetUser returns the current platform user from a JWT.
func (s *AuthService) GetUser(ctx context.Context, userID string) (*PlatformUser, error) {
	var user PlatformUser
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, u.created_at, u.is_admin, COALESCE(p.pg_username, '')
		FROM platform.users u
		LEFT JOIN platform.pg_users p ON p.user_id = u.id
		WHERE u.id = $1
	`, userID).Scan(&user.ID, &user.Email, &user.DisplayName, &user.CreatedAt, &user.IsAdmin, &user.PgUsername)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetRegistrationMode returns the current platform registration mode.
func (s *AuthService) GetRegistrationMode(ctx context.Context) string {
	var mode string
	err := s.db.QueryRow(ctx, `SELECT value FROM platform.settings WHERE key = 'registration_mode'`).Scan(&mode)
	if err != nil {
		return "open"
	}
	return mode
}

// IsAdmin checks if a user is an admin.
func (s *AuthService) IsAdmin(ctx context.Context, userID string) bool {
	var isAdmin bool
	err := s.db.QueryRow(ctx, `SELECT is_admin FROM platform.users WHERE id = $1`, userID).Scan(&isAdmin)
	if err != nil {
		return false
	}
	return isAdmin
}

// EnsureAdmin creates the admin user from env vars if it doesn't exist.
func (s *AuthService) EnsureAdmin(ctx context.Context, email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))

	// Check if already exists
	var existingID string
	var existingAdmin bool
	var existingHash string
	err := s.db.QueryRow(ctx, `SELECT id, is_admin, password_hash FROM platform.users WHERE email = $1`, email).Scan(&existingID, &existingAdmin, &existingHash)
	if err == nil {
		if existingAdmin {
			return nil
		}
		if bcrypt.CompareHashAndPassword([]byte(existingHash), []byte(password)) != nil {
			return fmt.Errorf("admin bootstrap refused: existing account password does not match configured admin password")
		}
		_, err = s.db.Exec(ctx, `UPDATE platform.users SET is_admin = TRUE WHERE id = $1`, existingID)
		return err
	}
	if err != pgx.ErrNoRows {
		return fmt.Errorf("lookup admin account: %w", err)
	}

	// Create admin user via normal Register flow
	_, _, regErr := s.registerInternal(ctx, RegisterRequest{Email: email, Password: password}, true)
	return regErr
}

// registerInternal creates a user, optionally as admin. Bypasses registration mode checks.
func (s *AuthService) registerInternal(ctx context.Context, req RegisterRequest, asAdmin bool) (*AuthResponse, int, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))

	if _, err := mail.ParseAddress(email); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid email format")
	}
	if len(req.Password) > 72 {
		return nil, http.StatusBadRequest, fmt.Errorf("password must not exceed 72 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("hash password: %w", err)
	}

	randBytes := make([]byte, 6)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate random: %w", err)
	}
	pgUsername := "u_" + hex.EncodeToString(randBytes)

	pgPassword, err := GenerateRandomPassword(32)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate pg password: %w", err)
	}

	pgHash, err := bcrypt.GenerateFromPassword([]byte(pgPassword), 12)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("hash pg password: %w", err)
	}

	pgEncrypted, err := EncryptPgPassword(pgPassword, req.Password)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("encrypt pg password: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var userID string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO platform.users (email, password_hash, is_admin)
		VALUES ($1, $2, $3)
		RETURNING id, created_at
	`, email, string(hash), asAdmin).Scan(&userID, &createdAt)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("insert user: %w", err)
	}

	if !pgUsernameRegex.MatchString(pgUsername) {
		return nil, http.StatusInternalServerError, fmt.Errorf("invalid pg username format")
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`CREATE ROLE "%s" LOGIN CONNECTION LIMIT 10`, pgUsername))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("create pg role: %w", err)
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`ALTER ROLE "%s" PASSWORD %s`, pgUsername, quoteLiteral(pgPassword)))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("set pg password: %w", err)
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`ALTER ROLE "%s" SET statement_timeout = '30s'`, pgUsername))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("set statement timeout: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO platform.pg_users (user_id, pg_username, pg_password_hash, pg_password_encrypted)
		VALUES ($1, $2, $3, $4)
	`, userID, pgUsername, string(pgHash), pgEncrypted)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("insert pg user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("commit: %w", err)
	}

	// Auto-create personal organization for the new user
	s.createPersonalOrg(ctx, userID)

	token, err := s.generateToken(ctx, userID, email)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate token: %w", err)
	}

	return &AuthResponse{
		Token: token,
		User: PlatformUser{
			ID:         userID,
			Email:      email,
			PgUsername: pgUsername,
			IsAdmin:    asAdmin,
			CreatedAt:  createdAt,
		},
	}, http.StatusCreated, nil
}

// ValidateToken verifies a platform JWT and returns the claims.
// It also checks the token_version claim against the database to
// detect tokens that were invalidated by a password change.
func (s *AuthService) ValidateToken(ctx context.Context, tokenString string) (*PlatformClaims, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	// Build PlatformClaims from map
	claims := &PlatformClaims{}
	if sub, _ := mapClaims["sub"].(string); sub != "" {
		claims.Subject = sub
	}
	if email, _ := mapClaims["email"].(string); email != "" {
		claims.Email = email
	}
	if typ, _ := mapClaims["type"].(string); typ != "" {
		claims.Type = typ
	}
	if iss, _ := mapClaims["iss"].(string); iss != "" {
		claims.Issuer = iss
	}

	// Check token_version to detect revoked tokens
	if tv, exists := mapClaims["tv"]; exists && s.db != nil {
		tvFloat, _ := tv.(float64)
		var currentTV int
		err := s.db.QueryRow(ctx, `SELECT token_version FROM platform.users WHERE id = $1`, claims.Subject).Scan(&currentTV)
		if err == nil && int(tvFloat) != currentTV {
			return nil, fmt.Errorf("token revoked")
		}
	}

	return claims, nil
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type ChangePasswordResponse struct {
	Message string `json:"message"`
	Token   string `json:"token"`
}

// ChangePassword updates the platform password and re-encrypts the PG password.
// Returns a new JWT token so the old one is implicitly invalidated.
func (s *AuthService) ChangePassword(ctx context.Context, userID string, req ChangePasswordRequest) (*ChangePasswordResponse, int, error) {
	if req.CurrentPassword == "" || req.NewPassword == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("current_password and new_password are required")
	}
	if len(req.NewPassword) < 8 {
		return nil, http.StatusBadRequest, fmt.Errorf("new password must be at least 8 characters")
	}
	if len(req.NewPassword) > 72 {
		return nil, http.StatusBadRequest, fmt.Errorf("new password must not exceed 72 characters")
	}

	// Begin transaction first to prevent TOCTOU race conditions
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Get current password hash and email inside the transaction with FOR UPDATE lock
	var passwordHash, email string
	err = tx.QueryRow(ctx, `SELECT password_hash, email FROM platform.users WHERE id = $1 FOR UPDATE`, userID).Scan(&passwordHash, &email)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("user not found")
	}

	// Verify current password
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.CurrentPassword)); err != nil {
		return nil, http.StatusUnauthorized, fmt.Errorf("current password is incorrect")
	}

	// Hash new password
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("hash password: %w", err)
	}

	// Decrypt PG password with current password, re-encrypt with new password
	var pgEncrypted string
	err = tx.QueryRow(ctx, `SELECT pg_password_encrypted FROM platform.pg_users WHERE user_id = $1`, userID).Scan(&pgEncrypted)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("get pg credentials: %w", err)
	}

	pgPassword, err := DecryptPgPassword(pgEncrypted, req.CurrentPassword)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("decrypt pg password: %w", err)
	}

	newPgEncrypted, err := EncryptPgPassword(pgPassword, req.NewPassword)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("re-encrypt pg password: %w", err)
	}

	// Update password
	_, err = tx.Exec(ctx, `UPDATE platform.users SET password_hash = $1, updated_at = NOW() WHERE id = $2`, string(newHash), userID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("update password: %w", err)
	}

	_, err = tx.Exec(ctx, `UPDATE platform.pg_users SET pg_password_encrypted = $1 WHERE user_id = $2`, newPgEncrypted, userID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("update pg encryption: %w", err)
	}

	// Increment token_version to invalidate all existing tokens
	_, err = tx.Exec(ctx, `UPDATE platform.users SET token_version = token_version + 1 WHERE id = $1`, userID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("increment token version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("commit: %w", err)
	}

	// Generate a new JWT so the caller can use it going forward,
	// with the updated token_version.
	token, err := s.generateToken(ctx, userID, email)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("generate token: %w", err)
	}

	return &ChangePasswordResponse{
		Message: "password changed successfully",
		Token:   token,
	}, http.StatusOK, nil
}

func (s *AuthService) generateToken(ctx context.Context, userID, email string) (string, error) {
	// Fetch token_version for JWT invalidation
	var tokenVersion int
	if s.db != nil {
		_ = s.db.QueryRow(ctx, `SELECT token_version FROM platform.users WHERE id = $1`, userID).Scan(&tokenVersion)
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   userID,
		"email": email,
		"type":  "platform",
		"iat":   now.Unix(),
		"exp":   now.Add(s.jwtExpiry).Unix(),
		"iss":   "dupabase",
		"tv":    tokenVersion,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// createPersonalOrg creates a "Personal" organization for a newly registered user.
// This is non-fatal: if it fails, the user still works and migration 008 can backfill.
func (s *AuthService) createPersonalOrg(ctx context.Context, userID string) {
	var orgID string
	err := s.db.QueryRow(ctx, `
		INSERT INTO platform.organizations (name, slug, created_by)
		VALUES ('Personal', $1, $2)
		RETURNING id
	`, "personal-"+userID, userID).Scan(&orgID)
	if err != nil {
		slog.Warn("failed to create personal org", "error", err, "user_id", userID)
		return
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO platform.org_members (org_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, orgID, userID)
	if err != nil {
		slog.Warn("failed to add user to personal org", "error", err, "user_id", userID)
	}
}
