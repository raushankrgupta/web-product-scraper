package utils

import (
	"net"
	"testing"
)

func TestIsRestrictedIP(t *testing.T) {
	tests := []struct {
		ip         string
		restricted bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // AWS/GCP metadata
		{"169.254.1.1", true},
		{"100.64.0.1", true}, // CGNAT
		{"0.0.0.0", true},
		{"8.8.8.8", false},        // Public Google DNS
		{"1.1.1.1", false},        // Public Cloudflare DNS
		{"142.250.190.46", false}, // Public Google IP
	}

	for _, tt := range tests {
		parsed := net.ParseIP(tt.ip)
		if parsed == nil {
			t.Fatalf("failed to parse IP %s", tt.ip)
		}
		got := IsRestrictedIP(parsed)
		if got != tt.restricted {
			t.Errorf("IsRestrictedIP(%s) = %v, want %v", tt.ip, got, tt.restricted)
		}
	}
}

func TestValidateSafeURL(t *testing.T) {
	blockedURLs := []string{
		"http://127.0.0.1:8080/admin",
		"http://localhost:3000",
		"http://sub.localhost/api",
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://10.0.0.5:27017",
		"http://192.168.0.100/",
		"ftp://example.com/file",
		"file:///etc/passwd",
		"http:///test",
	}

	for _, raw := range blockedURLs {
		if err := ValidateSafeURL(raw); err == nil {
			t.Errorf("expected ValidateSafeURL(%q) to be blocked, but it passed", raw)
		}
	}

	allowedURLs := []string{
		"https://www.google.com",
		"https://amazon.in/dp/B012345",
		"https://www.flipkart.com/item",
	}

	for _, raw := range allowedURLs {
		if err := ValidateSafeURL(raw); err != nil {
			t.Errorf("expected ValidateSafeURL(%q) to pass, got error: %v", raw, err)
		}
	}
}
