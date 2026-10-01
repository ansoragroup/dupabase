package httputil

import (
	"net/http/httptest"
	"testing"
)

func TestProxyIdentityUsesTrustedChain(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.2/32,127.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.55, 198.51.100.1")
	if got := ExtractClientIP(r, true); got != "198.51.100.1" {
		t.Fatalf("spoofed leftmost address used: %s", got)
	}
	r.RemoteAddr = "198.51.100.2:1234"
	if got := ExtractClientIP(r, true); got != "198.51.100.2" {
		t.Fatalf("untrusted peer headers accepted: %s", got)
	}
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ExtractClientIP(r, true); got != "10.0.0.2" {
		t.Fatalf("invalid identity accepted: %s", got)
	}
}
