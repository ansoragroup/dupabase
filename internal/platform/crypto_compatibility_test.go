package platform

import (
	"strings"
	"testing"
)

// These fixed fixtures were produced with x/crypto v0.57.0 PBKDF2 and the
// pre-migration AES-GCM format, independently of the current encrypt helper.
func TestLegacyEncryptedCredentials(t *testing.T) {
	vectors := []struct{ password, plaintext, encrypted string }{
		{"LegacyFixture2026!", "old-postgres-password", "000102030405060708090a0b0c0d0e0f:101112131415161718191a1b:0493f6d3854461ef54e123155d687ad9:8d372366c9cadddb20f39fa98909f525c2135542ec"},
		{"платформа!@#", "пароль123", "000102030405060708090a0b0c0d0e0f:101112131415161718191a1b:ac7fce3130e5d670d8c88f30631533eb:96bff88c5d108bf0ccb938f02b7a4c"},
		{strings.Repeat("p", 200), "long-password-secret", "000102030405060708090a0b0c0d0e0f:101112131415161718191a1b:f5389830e9c3353d65e139ed1aeac049:cd60950081d15cdb94c9c7f2d4ddb589de338766"},
		{"LegacyFixture2026!", "", "000102030405060708090a0b0c0d0e0f:101112131415161718191a1b:7f8135da95962b177ed99c5c7d62ee1b:"},
	}
	for _, vector := range vectors {
		actual, err := DecryptPgPassword(vector.encrypted, vector.password)
		if err != nil || actual != vector.plaintext {
			t.Fatalf("legacy credential rejected or changed: %v", err)
		}
		if _, err := DecryptPgPassword(vector.encrypted, "wrong-fixture-password"); err == nil {
			t.Fatal("wrong credential key accepted")
		}
		parts := strings.Split(vector.encrypted, ":")
		parts[2] = "ffffffffffffffffffffffffffffffff"
		if _, err := DecryptPgPassword(strings.Join(parts, ":"), vector.password); err == nil {
			t.Fatal("tampered credential tag accepted")
		}
	}
}
