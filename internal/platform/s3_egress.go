package platform

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

func endpointOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("s3_endpoint must be an HTTP(S) URL without credentials or a fragment")
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

func approvedStorageEndpoint(raw string) bool {
	origin, err := endpointOrigin(raw)
	if err != nil {
		return false
	}
	for _, entry := range strings.Split(os.Getenv("BACKUP_ALLOWED_ENDPOINTS"), ",") {
		if allowed, err := endpointOrigin(strings.TrimSpace(entry)); err == nil && allowed == origin {
			return true
		}
	}
	return false
}

func publicStorageAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "198.18.0.0/15", "192.0.0.0/24"} {
		if netip.MustParsePrefix(cidr).Contains(address) {
			return false
		}
	}
	return true
}

// Resolve and validate at dial time, then dial the validated IP itself. This
// prevents DNS rebinding and applies equally to tests, downloads and uploads.
func storageHTTPClient(endpoint string) (*http.Client, error) {
	var approvedHost string
	if endpoint != "" {
		if _, err := endpointOrigin(endpoint); err != nil {
			return nil, err
		}
		if approvedStorageEndpoint(endpoint) {
			u, _ := url.Parse(endpoint)
			approvedHost = strings.ToLower(u.Hostname())
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("storage endpoint has no usable addresses")
		}
		for _, ip := range addresses {
			if !publicStorageAddress(ip) && strings.ToLower(host) != approvedHost {
				return nil, fmt.Errorf("private storage endpoint requires an operator entry in BACKUP_ALLOWED_ENDPOINTS")
			}
		}
		var lastErr error
		for _, ip := range addresses {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return fmt.Errorf("storage endpoint redirects are not allowed")
	}}, nil
}
