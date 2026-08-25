package api

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// AlertTestHandler emits one WARN, one ERROR and one FATAL, plus a duplicate
// of the WARN to prove dedup is working (the duplicate should be suppressed
// and reappear ~60s later as a rollup line).
//
// Registered only when ENVIRONMENT != "prod", and additionally requires the
// internal shared secret — two independent gates, because an endpoint that
// can push arbitrary-looking messages into the ops channel is worth guarding
// twice.
func AlertTestHandler(w http.ResponseWriter, r *http.Request) {
	if config.IsProd() {
		http.NotFound(w, r)
		return
	}

	secret := config.InternalAPISecret
	given := r.Header.Get("X-Internal-Secret")
	if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(given)) != 1 {
		utils.RespondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	if !alert.Enabled() {
		utils.RespondJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Alerts are disabled — set TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID and ALERTS_ENABLED=true",
		})
		return
	}

	reqID := utils.RequestIDFromContext(r.Context())

	alert.Report(alert.Event{
		Level: alert.LevelWarn, Component: "system", Title: "alert smoke test (warn)",
		Err: errors.New("this is a test warning"), RequestID: reqID,
		Fields: map[string]string{"kind": "smoke-test"},
	})
	// Deliberate duplicate — must be suppressed by the cooldown.
	alert.Report(alert.Event{
		Level: alert.LevelWarn, Component: "system", Title: "alert smoke test (warn)",
		Err: errors.New("this is a test warning"), RequestID: reqID,
		Fields: map[string]string{"kind": "smoke-test"},
	})
	alert.Report(alert.Event{
		Level: alert.LevelError, Component: "system", Title: "alert smoke test (error)",
		Err: errors.New("this is a test error"), RequestID: reqID,
		Route: r.URL.Path, Method: r.Method, Status: 500,
	})
	alert.Report(alert.Event{
		Level: alert.LevelFatal, Component: "system", Title: "alert smoke test (fatal)",
		Err: errors.New("this is a test fatal"), RequestID: reqID,
	})

	sent, dropped, queued := alert.Stats()
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"emitted":       4,
		"expected_sent": 3,
		"note":          "the duplicate WARN should be suppressed and appear as a rollup within ~60s",
		"stats":         map[string]interface{}{"sent": sent, "dropped": dropped, "queued": queued},
	})
}
