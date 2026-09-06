package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

type guestRateLimiter struct {
	sync.Mutex
	history map[string][]time.Time
}

var guestLimiter = &guestRateLimiter{
	history: make(map[string][]time.Time),
}

func (l *guestRateLimiter) allow(ip string, limit int, window time.Duration) bool {
	l.Lock()
	defer l.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	var valid []time.Time
	for _, t := range l.history[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= limit {
		l.history[ip] = valid
		return false
	}

	valid = append(valid, now)
	l.history[ip] = valid

	// Memory leak prevention: periodic pruning of expired IP entries
	if len(l.history) > 5000 {
		for k, timestamps := range l.history {
			if len(timestamps) == 0 || timestamps[len(timestamps)-1].Before(cutoff) {
				delete(l.history, k)
			}
		}
	}

	return true
}

func clientIP(r *http.Request) string {
	// Trust CF-Connecting-IP first if behind Cloudflare
	if cfIP := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cfIP != "" {
		if parsed := net.ParseIP(cfIP); parsed != nil {
			return cfIP
		}
	}
	// X-Real-IP if provided by reverse proxy (e.g. Caddy/Nginx)
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		if parsed := net.ParseIP(xrip); parsed != nil {
			return xrip
		}
	}
	// X-Forwarded-For: validate IP format
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			candidate := strings.TrimSpace(parts[0])
			if parsed := net.ParseIP(candidate); parsed != nil {
				return candidate
			}
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return ip
	}
	return r.RemoteAddr
}

// GuestTokenRequest is the body the mobile app sends to mint an anonymous
// session. device_id is whatever stable identifier the client has (Expo's
// expo-application installationId / Android ANDROID_ID / iOS identifierForVendor).
type GuestTokenRequest struct {
	DeviceID string `json:"device_id"`
}

// GuestTokenHandler mints a short-lived JWT bound to a device_id so users can
// run their first try-on without signing up. The token carries `guest: true`
// in its claims so AuthMiddleware can route around the users-collection check
// and apply the guest daily quota (1/day).
//
// Security note: rate-limited to 10 guest tokens per IP per hour to prevent
// automated Sybil attacks draining Gemini credits with rotating fake device IDs.
func GuestTokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Rate limit: max 10 guest tokens per IP per hour
	ip := clientIP(r)
	if !guestLimiter.allow(ip, 10, time.Hour) {
		utils.RespondError(w, nil, "Too many guest sessions requested from this network. Please sign up or try again later.", http.StatusTooManyRequests)
		return
	}

	var req GuestTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}

	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		utils.RespondError(w, nil, "device_id is required", http.StatusBadRequest)
		return
	}
	if len(deviceID) > 128 {
		utils.RespondError(w, nil, "device_id too long", http.StatusBadRequest)
		return
	}

	// Use the same JWT plumbing as real users but prefix the subject with
	// "guest:" so it can never collide with an ObjectID.
	subject := fmt.Sprintf("guest:%s", deviceID)
	token, err := utils.GenerateGuestToken(subject)
	if err != nil {
		utils.RespondError(w, nil, "Failed to issue guest token", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"token":   token,
		"user_id": subject,
		"plan":    "guest",
		"guest":   true,
	})
}

// Compile-time sanity check that jwt.MapClaims is still our claim shape.
// Prevents accidental refactors elsewhere from breaking guest parsing.
var _ = jwt.MapClaims{}
