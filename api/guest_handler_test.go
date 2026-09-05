package api

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	// 1. CF-Connecting-IP takes precedence
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("CF-Connecting-IP", "203.0.113.195")
	req.Header.Set("X-Real-IP", "198.51.100.1")
	req.Header.Set("X-Forwarded-For", "192.0.2.1")
	req.RemoteAddr = "10.0.0.1:12345"

	ip := clientIP(req)
	if ip != "203.0.113.195" {
		t.Errorf("expected 203.0.113.195, got %s", ip)
	}

	// 2. X-Real-IP fallback
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Real-IP", "198.51.100.1")
	req2.RemoteAddr = "10.0.0.1:12345"

	ip2 := clientIP(req2)
	if ip2 != "198.51.100.1" {
		t.Errorf("expected 198.51.100.1, got %s", ip2)
	}

	// 3. RemoteAddr fallback
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "192.0.2.55:54321"

	ip3 := clientIP(req3)
	if ip3 != "192.0.2.55" {
		t.Errorf("expected 192.0.2.55, got %s", ip3)
	}
}

func TestGuestRateLimiter(t *testing.T) {
	limiter := &guestRateLimiter{
		history: make(map[string][]time.Time),
	}

	ip := "192.0.2.10"
	// Should allow up to limit 3
	if !limiter.allow(ip, 3, time.Minute) {
		t.Errorf("expected request 1 to be allowed")
	}
	if !limiter.allow(ip, 3, time.Minute) {
		t.Errorf("expected request 2 to be allowed")
	}
	if !limiter.allow(ip, 3, time.Minute) {
		t.Errorf("expected request 3 to be allowed")
	}
	// 4th request must be blocked
	if limiter.allow(ip, 3, time.Minute) {
		t.Errorf("expected request 4 to be rejected")
	}
}
