package httputil

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// ExtractClientIP returns the client IP from the request.
// When trustProxy is true, X-Forwarded-For and X-Real-IP headers are checked first.
// Falls back to r.RemoteAddr with proper IPv6 handling via net.SplitHostPort.
func ExtractClientIP(r *http.Request, trustProxy bool) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	trusted := func(candidate string) bool {
		addr, err := netip.ParseAddr(candidate)
		if err != nil {
			return false
		}
		cidrs := os.Getenv("TRUSTED_PROXY_CIDRS")
		if cidrs == "" {
			cidrs = "127.0.0.0/8,::1/128"
		}
		for _, raw := range strings.Split(cidrs, ",") {
			if prefix, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil && prefix.Contains(addr.Unmap()) {
				return true
			}
		}
		return false
	}
	if trustProxy && trusted(ip) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				candidate := strings.TrimSpace(parts[i])
				if _, err := netip.ParseAddr(candidate); err != nil {
					return ip
				}
				if !trusted(candidate) {
					return candidate
				}
			}
			return strings.TrimSpace(parts[0])
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			candidate := strings.TrimSpace(xri)
			if _, err := netip.ParseAddr(candidate); err == nil {
				return candidate
			}
		}
	}
	return ip
}
