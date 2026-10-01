package config

import (
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// getEnv
// ---------------------------------------------------------------------------

func TestGetEnv_ReturnsFallback(t *testing.T) {
	// Use a key that is extremely unlikely to be set
	key := "TEST_GETENV_NONEXISTENT_KEY_12345"
	os.Unsetenv(key)

	result := getEnv(key, "fallback_value")
	if result != "fallback_value" {
		t.Errorf("expected 'fallback_value', got %q", result)
	}
}

func TestGetEnv_ReturnsEnvValue(t *testing.T) {
	key := "TEST_GETENV_SET_KEY_12345"
	os.Setenv(key, "actual_value")
	defer os.Unsetenv(key)

	result := getEnv(key, "fallback_value")
	if result != "actual_value" {
		t.Errorf("expected 'actual_value', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// getEnvInt
func TestGetEnvIntRejectsNarrowingOverflow(t *testing.T) {
	for _, value := range []string{"2147483648", "-2147483649", "4294967296"} {
		t.Setenv("DUPABASE_INT_LIMIT_TEST", value)
		if got := getEnvInt("DUPABASE_INT_LIMIT_TEST", 42); got != 42 {
			t.Fatalf("out-of-range input %s became %d", value, got)
		}
	}
}

// ---------------------------------------------------------------------------

func TestGetEnvInt_ReturnsFallback(t *testing.T) {
	key := "TEST_GETENVINT_NONEXISTENT_KEY_12345"
	os.Unsetenv(key)

	result := getEnvInt(key, 42)
	if result != 42 {
		t.Errorf("expected 42, got %d", result)
	}
}

func TestGetEnvInt_ReturnsEnvValue(t *testing.T) {
	key := "TEST_GETENVINT_SET_KEY_12345"
	os.Setenv(key, "99")
	defer os.Unsetenv(key)

	result := getEnvInt(key, 42)
	if result != 99 {
		t.Errorf("expected 99, got %d", result)
	}
}

func TestGetEnvInt_FallbackOnInvalidInt(t *testing.T) {
	key := "TEST_GETENVINT_INVALID_KEY_12345"
	os.Setenv(key, "not_a_number")
	defer os.Unsetenv(key)

	result := getEnvInt(key, 42)
	if result != 42 {
		t.Errorf("expected fallback 42 for invalid int, got %d", result)
	}
}

// ---------------------------------------------------------------------------
// getEnvBool
// ---------------------------------------------------------------------------

func TestGetEnvBool_ReturnsFallback(t *testing.T) {
	key := "TEST_GETENVBOOL_NONEXISTENT_KEY_12345"
	os.Unsetenv(key)

	result := getEnvBool(key, true)
	if result != true {
		t.Errorf("expected true, got %v", result)
	}

	result = getEnvBool(key, false)
	if result != false {
		t.Errorf("expected false, got %v", result)
	}
}

func TestGetEnvBool_TrueValues(t *testing.T) {
	key := "TEST_GETENVBOOL_TRUE_12345"

	tests := []struct {
		value    string
		expected bool
	}{
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"yes", false},  // only "true" and "1" are true
		{"TRUE", false}, // case sensitive
	}

	for _, tt := range tests {
		os.Setenv(key, tt.value)
		result := getEnvBool(key, false)
		if result != tt.expected {
			t.Errorf("getEnvBool(%q): expected %v, got %v", tt.value, tt.expected, result)
		}
	}

	os.Unsetenv(key)
}

// ---------------------------------------------------------------------------
// mustGetEnv
// ---------------------------------------------------------------------------

func TestMustGetEnv_Panics(t *testing.T) {
	key := "TEST_MUSTGETENV_NONEXISTENT_KEY_12345"
	os.Unsetenv(key)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for missing required env var")
		}
	}()

	mustGetEnv(key)
}

func TestMustGetEnv_ReturnsValue(t *testing.T) {
	key := "TEST_MUSTGETENV_SET_KEY_12345"
	os.Setenv(key, "required_value")
	defer os.Unsetenv(key)

	result := mustGetEnv(key)
	if result != "required_value" {
		t.Errorf("expected 'required_value', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoad_RejectsShortJWTSecret(t *testing.T) {
	// Set required env vars
	os.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/testdb")
	os.Setenv("PLATFORM_JWT_SECRET", "short")
	defer os.Unsetenv("DATABASE_URL")
	defer os.Unsetenv("PLATFORM_JWT_SECRET")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for short JWT secret")
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/testdb")
	os.Setenv("PLATFORM_JWT_SECRET", "this-is-a-long-enough-secret-for-testing-32chars!")
	defer os.Unsetenv("DATABASE_URL")
	defer os.Unsetenv("PLATFORM_JWT_SECRET")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.DatabaseURL != "postgresql://user:pass@localhost:5432/testdb" {
		t.Errorf("unexpected DatabaseURL: %q", cfg.DatabaseURL)
	}
	if cfg.PlatformJWTSecret != "this-is-a-long-enough-secret-for-testing-32chars!" {
		t.Errorf("unexpected PlatformJWTSecret")
	}
}

func TestLoad_DefaultValues(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/testdb")
	os.Setenv("PLATFORM_JWT_SECRET", "this-is-a-long-enough-secret-for-testing-32chars!")
	// Clear other env vars to test defaults
	os.Unsetenv("PORT")
	os.Unsetenv("HOST")
	os.Unsetenv("SITE_URL")
	os.Unsetenv("PLATFORM_JWT_EXPIRY")
	os.Unsetenv("MAX_CONNECTIONS_PER_DB")
	os.Unsetenv("GLOBAL_MAX_CONNECTIONS")
	os.Unsetenv("POOL_IDLE_TIMEOUT_SECONDS")
	os.Unsetenv("DEFAULT_ENABLE_SIGNUP")
	os.Unsetenv("DEFAULT_AUTOCONFIRM")
	os.Unsetenv("DEFAULT_PASSWORD_MIN_LENGTH")
	defer os.Unsetenv("DATABASE_URL")
	defer os.Unsetenv("PLATFORM_JWT_SECRET")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Port != 3000 {
		t.Errorf("expected default Port 3000, got %d", cfg.Port)
	}
	if cfg.Host != "0.0.0.0" {
		t.Errorf("expected default Host '0.0.0.0', got %q", cfg.Host)
	}
	if cfg.SiteURL != "http://localhost:3000" {
		t.Errorf("expected default SiteURL, got %q", cfg.SiteURL)
	}
	if cfg.PlatformJWTExpiry != 86400 {
		t.Errorf("expected default JWT expiry 86400, got %d", cfg.PlatformJWTExpiry)
	}
	if cfg.MaxConnectionsPerDB != 5 {
		t.Errorf("expected default MaxConnectionsPerDB 5, got %d", cfg.MaxConnectionsPerDB)
	}
	if cfg.GlobalMaxConnections != 100 {
		t.Errorf("expected default GlobalMaxConnections 100, got %d", cfg.GlobalMaxConnections)
	}
	if cfg.PoolIdleTimeout != 300 {
		t.Errorf("expected default PoolIdleTimeout 300, got %d", cfg.PoolIdleTimeout)
	}
	if !cfg.DefaultEnableSignup {
		t.Error("expected default DefaultEnableSignup true")
	}
	if !cfg.DefaultAutoconfirm {
		t.Error("expected default DefaultAutoconfirm true")
	}
	if cfg.DefaultPasswordMinLength != 6 {
		t.Errorf("expected default DefaultPasswordMinLength 6, got %d", cfg.DefaultPasswordMinLength)
	}
}

func TestLoad_CustomValues(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/testdb")
	os.Setenv("PLATFORM_JWT_SECRET", "this-is-a-long-enough-secret-for-testing-32chars!")
	os.Setenv("PORT", "8080")
	os.Setenv("HOST", "127.0.0.1")
	os.Setenv("MAX_CONNECTIONS_PER_DB", "10")
	os.Setenv("GLOBAL_MAX_CONNECTIONS", "200")
	os.Setenv("DEFAULT_ENABLE_SIGNUP", "false")
	os.Setenv("DEFAULT_AUTOCONFIRM", "false")
	os.Setenv("DEFAULT_PASSWORD_MIN_LENGTH", "8")
	defer func() {
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("PLATFORM_JWT_SECRET")
		os.Unsetenv("PORT")
		os.Unsetenv("HOST")
		os.Unsetenv("MAX_CONNECTIONS_PER_DB")
		os.Unsetenv("GLOBAL_MAX_CONNECTIONS")
		os.Unsetenv("DEFAULT_ENABLE_SIGNUP")
		os.Unsetenv("DEFAULT_AUTOCONFIRM")
		os.Unsetenv("DEFAULT_PASSWORD_MIN_LENGTH")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected Port 8080, got %d", cfg.Port)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected Host '127.0.0.1', got %q", cfg.Host)
	}
	if cfg.MaxConnectionsPerDB != 10 {
		t.Errorf("expected MaxConnectionsPerDB 10, got %d", cfg.MaxConnectionsPerDB)
	}
	if cfg.GlobalMaxConnections != 200 {
		t.Errorf("expected GlobalMaxConnections 200, got %d", cfg.GlobalMaxConnections)
	}
	if cfg.DefaultEnableSignup {
		t.Error("expected DefaultEnableSignup false")
	}
	if cfg.DefaultAutoconfirm {
		t.Error("expected DefaultAutoconfirm false")
	}
	if cfg.DefaultPasswordMinLength != 8 {
		t.Errorf("expected DefaultPasswordMinLength 8, got %d", cfg.DefaultPasswordMinLength)
	}
}

// ---------------------------------------------------------------------------
// Config struct fields
// ---------------------------------------------------------------------------

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		Port:                     3000,
		Host:                     "0.0.0.0",
		SiteURL:                  "http://localhost:3000",
		DatabaseURL:              "postgresql://user:pass@localhost:5432/db",
		PlatformJWTSecret:        "secret",
		PlatformJWTExpiry:        86400,
		MaxConnectionsPerDB:      5,
		GlobalMaxConnections:     100,
		PoolIdleTimeout:          300,
		DefaultEnableSignup:      true,
		DefaultAutoconfirm:       true,
		DefaultPasswordMinLength: 6,
	}

	if cfg.Port != 3000 {
		t.Error("unexpected Port")
	}
	if cfg.PoolIdleTimeout != 300 {
		t.Error("unexpected PoolIdleTimeout")
	}
}
