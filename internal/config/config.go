package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port    int
	Host    string
	SiteURL string

	// Platform database (superuser connection)
	DatabaseURL string

	// Platform JWT
	PlatformJWTSecret string
	PlatformJWTExpiry int // seconds

	// Pool Manager
	MaxConnectionsPerDB  int
	GlobalMaxConnections int
	PoolIdleTimeout      int // seconds

	// Platform pool
	PlatformMaxConns int
	PlatformMinConns int

	// API keys
	APIKeyExpiryDays int

	// Default auth settings for new projects
	DefaultEnableSignup      bool
	DefaultAutoconfirm       bool
	DefaultPasswordMinLength int

	// Backup
	BackupEncryptionKey string

	// Import
	ImportMaxSizeMB int
	ImportTempDir   string

	// Admin
	AdminEmail    string
	AdminPassword string

	// Proxy
	TrustProxy bool
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to load .env file", "error", err)
		}
	}

	cfg := &Config{
		Port:                     getEnvInt("PORT", 3000),
		Host:                     getEnv("HOST", "0.0.0.0"),
		SiteURL:                  getEnv("SITE_URL", "http://localhost:3000"),
		DatabaseURL:              mustGetEnv("DATABASE_URL"),
		PlatformJWTSecret:        mustGetEnv("PLATFORM_JWT_SECRET"),
		PlatformJWTExpiry:        getEnvInt("PLATFORM_JWT_EXPIRY", 86400),
		MaxConnectionsPerDB:      getEnvInt("MAX_CONNECTIONS_PER_DB", 5),
		GlobalMaxConnections:     getEnvInt("GLOBAL_MAX_CONNECTIONS", 100),
		PoolIdleTimeout:          getEnvInt("POOL_IDLE_TIMEOUT_SECONDS", 300),
		DefaultEnableSignup:      getEnvBool("DEFAULT_ENABLE_SIGNUP", true),
		DefaultAutoconfirm:       getEnvBool("DEFAULT_AUTOCONFIRM", true),
		DefaultPasswordMinLength: getEnvInt("DEFAULT_PASSWORD_MIN_LENGTH", 6),
		PlatformMaxConns:         getEnvInt("PLATFORM_MAX_CONNECTIONS", 10),
		PlatformMinConns:         getEnvInt("PLATFORM_MIN_CONNECTIONS", 2),
		APIKeyExpiryDays:         getEnvInt("API_KEY_EXPIRY_DAYS", 365),
		BackupEncryptionKey:      getEnv("BACKUP_ENCRYPTION_KEY", ""),
		ImportMaxSizeMB:          getEnvInt("IMPORT_MAX_SIZE_MB", 500),
		ImportTempDir:            getEnv("IMPORT_TEMP_DIR", "/tmp/imports"),
		AdminEmail:               getEnv("ADMIN_EMAIL", ""),
		AdminPassword:            getEnv("ADMIN_PASSWORD", ""),
		TrustProxy:               getEnvBool("TRUST_PROXY", false),
	}

	if len(cfg.PlatformJWTSecret) < 32 {
		return nil, fmt.Errorf("PLATFORM_JWT_SECRET must be at least 32 characters")
	}

	// Validate PORT
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid PORT: %d (must be 1-65535)", cfg.Port)
	}

	// Validate connection config
	if cfg.MaxConnectionsPerDB < 1 {
		return nil, fmt.Errorf("MAX_CONNECTIONS_PER_DB must be >= 1, got %d", cfg.MaxConnectionsPerDB)
	}
	if cfg.GlobalMaxConnections < 1 {
		return nil, fmt.Errorf("GLOBAL_MAX_CONNECTIONS must be >= 1, got %d", cfg.GlobalMaxConnections)
	}
	if cfg.DefaultPasswordMinLength < 6 || cfg.DefaultPasswordMinLength > 72 {
		return nil, fmt.Errorf("DEFAULT_PASSWORD_MIN_LENGTH must be between 6 and 72")
	}

	// Validate backup encryption key length
	if cfg.BackupEncryptionKey != "" && len(cfg.BackupEncryptionKey) < 32 {
		return nil, fmt.Errorf("BACKUP_ENCRYPTION_KEY must be at least 32 characters")
	}

	// Validate admin config: both or neither
	if (cfg.AdminEmail != "" && cfg.AdminPassword == "") || (cfg.AdminEmail == "" && cfg.AdminPassword != "") {
		return nil, fmt.Errorf("ADMIN_EMAIL and ADMIN_PASSWORD must both be set or both be empty")
	}
	if cfg.AdminPassword != "" && len(cfg.AdminPassword) < 8 {
		return nil, fmt.Errorf("ADMIN_PASSWORD must be at least 8 characters")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %s is not set", key))
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	// These settings are also passed to PostgreSQL's int32 pool limits.
	// Reject out-of-range input before any narrowing conversion.
	i, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return fallback
	}
	return int(i)
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1"
}
