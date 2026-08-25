package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// RequestIDMiddleware stamps every request with an id, echoes it back as
// X-Request-ID, and binds it to a request-scoped slog logger. This is the
// thread that ties a Telegram alert to the exact log lines for that request.
//
// An inbound X-Request-ID is honoured (so the mobile app or a proxy can
// correlate from its side) but bounded in length so a hostile client can't
// bloat every log line.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(utils.WithRequestID(r.Context(), id)))
	})
}

// RequestLogMiddleware replaces the old LatencyMiddleware, which logged only
// method, path and duration. It emits one structured line per request with
// the status code, user id, request id and response size — the fields whose
// absence made the 22 sub-10µs requests in the production log unexplainable.
//
// has_auth_header is deliberately included: a 401 with no Authorization
// header is a client bug, and a 401 *with* one is an expired token. Those two
// were indistinguishable before.
func RequestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		dur := time.Since(start)
		// The handler chain may have attached the user id after auth ran.
		userID, _ := GetUserIDFromContext(r.Context())

		attrs := []any{
			"request_id", utils.RequestIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", float64(dur.Microseconds()) / 1000,
			"bytes", rec.written,
			"has_auth_header", r.Header.Get("Authorization") != "",
		}
		if userID != "" {
			attrs = append(attrs, "user_id", userID)
		}

		switch {
		case rec.status >= 500:
			slog.Error("request", attrs...)
		case rec.status >= 400:
			slog.Warn("request", attrs...)
		default:
			slog.Info("request", attrs...)
		}
	})
}

// RecoverMiddleware turns a panic into a 500 plus a FATAL alert instead of a
// dead process. Before this existed, the nil-pointer dereference on a
// safety-blocked Gemini candidate (utils/gemini_client.go) would have taken
// the whole service down with no notification.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the documented way for a handler to
			// abandon a response; it is not a bug and must not be reported.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			stack := string(debug.Stack())
			reqID := utils.RequestIDFromContext(r.Context())
			userID, _ := GetUserIDFromContext(r.Context())

			slog.Error("panic recovered",
				"request_id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
				"panic", fmt.Sprintf("%v", rec),
				"stack", stack,
			)

			alert.Report(alert.Event{
				Level:     alert.LevelFatal,
				Component: "http",
				Title:     "panic in " + r.Method + " " + r.URL.Path,
				Err:       fmt.Errorf("%v", rec),
				RequestID: reqID,
				UserID:    userID,
				Route:     r.URL.Path,
				Method:    r.Method,
				Status:    http.StatusInternalServerError,
				Stack:     firstLines(stack, 12),
			})

			utils.RespondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. We've been alerted.",
			})
		}()

		next.ServeHTTP(w, r)
	})
}

// firstLines trims a stack trace to something a Telegram message can hold.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// MethodGuard rejects unexpected verbs *before* AuthMiddleware runs, with a
// logged 405 rather than a silent 401.
//
// This is the fix for the 22 requests in the production log that returned in
// under 10µs and were therefore invisible: GET /gallery ×11 and
// GET /auth/delete-account ×10 were bouncing off AuthMiddleware's missing
// header check, which looks identical to an expired token. Now a wrong verb
// says so, in a line that names the verb, the path and whether the caller
// even sent a token.
func MethodGuard(allowed []string, next http.Handler) http.Handler {
	allow := make(map[string]bool, len(allowed)+1)
	for _, m := range allowed {
		allow[strings.ToUpper(m)] = true
	}
	allow[http.MethodOptions] = true
	allowHeader := strings.Join(append(allowed, http.MethodOptions), ", ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allow[r.Method] {
			next.ServeHTTP(w, r)
			return
		}

		slog.Warn("method not allowed",
			"request_id", utils.RequestIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"allowed", allowHeader,
			"user_agent", r.UserAgent(),
			"has_auth_header", r.Header.Get("Authorization") != "",
		)
		alert.Warnf("http", "unexpected method on "+r.URL.Path, nil,
			"method", r.Method, "allowed", allowHeader, "user_agent", r.UserAgent())

		w.Header().Set("Allow", allowHeader)
		utils.RespondJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error":   fmt.Sprintf("%s is not allowed on %s", r.Method, r.URL.Path),
			"allowed": allowHeader,
		})
	})
}
