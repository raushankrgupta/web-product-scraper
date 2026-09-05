package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// The production log shows 30 /try-on requests covering 11 unique
// (person, product) pairs — 2.7 attempts each, with one pair retried six
// times. Billing only charges for a 2xx, which is the right call for
// fairness but leaves *failed* generations completely uncapped. That is a
// direct 2.7× bill multiplier on an upstream we pay per call.
//
// This file adds three cheap guards in front of the generation call:
//
//	inflight  — one identical request at a time per user (409 for the rest)
//	results   — a 10-minute (user, person, product, theme) → S3 key cache
//	failures  — a per-user consecutive-failure throttle
//
// All three are in-process maps. That is correct at current traffic (one
// replica); running more than one replica means moving them to Redis, because
// each replica would otherwise keep its own view.

// ---------------------------------------------------------------- in-flight

const inflightTTL = 2 * time.Minute

type inflightSet struct {
	mu    sync.Mutex
	items map[string]time.Time
}

var inflight = &inflightSet{items: map[string]time.Time{}}

// TryAcquire claims the key. It returns false when an identical request is
// already running (and hasn't gone stale).
func (s *inflightSet) TryAcquire(key string) bool {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Opportunistic reap — the map only ever holds in-flight try-ons, so
	// this stays tiny.
	for k, t := range s.items {
		if now.Sub(t) > inflightTTL {
			delete(s.items, k)
		}
	}

	if t, ok := s.items[key]; ok && now.Sub(t) <= inflightTTL {
		return false
	}
	s.items[key] = now
	return true
}

func (s *inflightSet) Release(key string) {
	s.mu.Lock()
	delete(s.items, key)
	s.mu.Unlock()
}

// -------------------------------------------------------------- result cache

const resultCacheTTL = 10 * time.Minute

type cachedResult struct {
	objectKey string
	at        time.Time
}

var (
	resultsMu sync.Mutex
	results   = map[string]cachedResult{}
)

// tryOnCacheKey identifies a logically identical generation.
//
// The customer's styling note is part of the identity of the request, not
// decoration on it: "same person, same garment, but on a rooftop at night" is
// a different image, and hashing the note in is what stops the cache handing
// back the daylight one. Hashed rather than concatenated so a 1000-character
// note doesn't become a 1000-character map key.
func tryOnCacheKey(userID, personID, productID, themeID, specialRequest string) string {
	note := ""
	if specialRequest != "" {
		sum := sha256.Sum256([]byte(specialRequest))
		note = hex.EncodeToString(sum[:8])
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s", userID, personID, productID, themeID, note)
}

func rememberTryOnResult(key, objectKey string) {
	if key == "" || objectKey == "" {
		return
	}
	resultsMu.Lock()
	results[key] = cachedResult{objectKey: objectKey, at: time.Now()}
	// Bound the map: drop anything already past its TTL.
	for k, v := range results {
		if time.Since(v.at) > resultCacheTTL {
			delete(results, k)
		}
	}
	resultsMu.Unlock()
}

// lookupTryOnResult returns a recent S3 key for this exact combination, so a
// user tapping "try on" twice gets the same image instantly and at no cost.
func lookupTryOnResult(key string) (string, bool) {
	resultsMu.Lock()
	defer resultsMu.Unlock()
	v, ok := results[key]
	if !ok || time.Since(v.at) > resultCacheTTL {
		return "", false
	}
	return v.objectKey, true
}

// ----------------------------------------------------------- failure throttle

const (
	failureWindow    = 10 * time.Minute
	failureThreshold = 3
)

type failureTracker struct {
	mu     sync.Mutex
	counts map[string][]time.Time
}

var failures = &failureTracker{counts: map[string][]time.Time{}}

// RecordFailure notes a failed generation for a user and reports whether they
// have now crossed the threshold.
func (f *failureTracker) RecordFailure(userID string) bool {
	now := time.Now()

	f.mu.Lock()
	defer f.mu.Unlock()

	kept := f.counts[userID][:0]
	for _, t := range f.counts[userID] {
		if now.Sub(t) <= failureWindow {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	f.counts[userID] = kept

	return len(kept) >= failureThreshold
}

// Throttled reports whether the user is currently over the failure threshold.
func (f *failureTracker) Throttled(userID string) bool {
	now := time.Now()

	f.mu.Lock()
	defer f.mu.Unlock()

	var n int
	for _, t := range f.counts[userID] {
		if now.Sub(t) <= failureWindow {
			n++
		}
	}
	return n >= failureThreshold
}

func (f *failureTracker) Clear(userID string) {
	f.mu.Lock()
	delete(f.counts, userID)
	f.mu.Unlock()
}

// ------------------------------------------------------------------ middleware

// TryOnGuardMiddleware sits between AuthMiddleware and StarGateMiddleware. It
// rejects duplicate in-flight requests and users in a failure loop before
// either one can reach a paid upstream call.
func TryOnGuardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := GetUserIDFromContext(r.Context())
		if err != nil {
			utils.RespondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Please sign in again to continue."})
			return
		}

		// A user whose last three attempts failed inside ten minutes is in a
		// loop that more attempts will not fix. In the production log one
		// user burned six consecutive attempts on the same pair; nobody
		// found out for six days.
		if failures.Throttled(userID) {
			slog.Warn("try-on throttled after repeated failures", "user_id", userID, "path", r.URL.Path)
			recordGateRejection(r, "failure_throttled", http.StatusTooManyRequests,
				fmt.Sprintf("%d failures within %s", failureThreshold, failureWindow))
			utils.RespondJSON(w, http.StatusTooManyRequests, map[string]interface{}{
				"error":       "We're having trouble generating images right now — we've been alerted. Please try again in a few minutes.",
				"retry_after": int(failureWindow.Seconds()),
			})
			return
		}

		// Fingerprint the decoded body so two taps on the same button
		// collapse, while two genuinely different try-ons don't.
		//
		// Only JSON bodies are fingerprinted. The guest endpoint is
		// multipart with an image attached: buffering that to hash it would
		// mean holding the upload in memory twice, and truncating it at any
		// limit would corrupt the multipart parse downstream. Guest requests
		// therefore skip in-flight dedup and keep only the failure throttle.
		if !isJSONRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		body, readErr := io.ReadAll(io.LimitReader(r.Body, maxJSONBody))
		r.Body.Close()
		if readErr != nil {
			utils.RespondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		key := userID + ":" + r.URL.Path + ":" + fingerprintBody(body)
		if !inflight.TryAcquire(key) {
			slog.Info("duplicate try-on rejected", "user_id", userID, "path", r.URL.Path)
			recordGateRejection(r, "duplicate_in_flight", http.StatusConflict,
				"an identical try-on was already running")
			utils.RespondJSON(w, http.StatusConflict, map[string]string{
				"error": "This try-on is already being generated. Please wait.",
			})
			return
		}
		defer inflight.Release(key)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		switch {
		case rec.status >= 200 && rec.status < 300:
			failures.Clear(userID)
		case rec.status >= 500 || rec.status == http.StatusUnprocessableEntity:
			if failures.RecordFailure(userID) {
				alert.Errorf("tryon", "user hit the consecutive-failure threshold", nil,
					"user_id", userID, "route", r.URL.Path,
					"threshold", fmt.Sprintf("%d in %s", failureThreshold, failureWindow))
			}
		}
	})
}

// maxJSONBody bounds the try-on JSON payload we are willing to buffer for
// fingerprinting. The real bodies are a few hundred bytes.
const maxJSONBody = 1 << 20

// isJSONRequest reports whether the body is JSON we can safely buffer.
func isJSONRequest(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
}

// fingerprintBody hashes the *canonical* JSON body so that key ordering and
// whitespace don't produce two different fingerprints for the same request.
// Non-JSON bodies (multipart guest try-on) fall back to hashing the raw bytes.
func fingerprintBody(body []byte) string {
	var v interface{}
	if json.Unmarshal(body, &v) == nil {
		if canonical, err := json.Marshal(v); err == nil {
			body = canonical
		}
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:12])
}
