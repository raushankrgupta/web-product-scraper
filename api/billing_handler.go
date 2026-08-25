package api

import (
	"context"
	"net/http"
	"time"

	"github.com/raushankrgupta/web-product-scraper/utils"
)

// BillingStatusHandler returns the caller's current plan and today's try-on
// quota usage. Powers the "X try-ons left today" pill on the mobile app.
// Wrap with AuthMiddleware.
func BillingStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return
	}
	plan := GetUserPlanFromContext(r.Context())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := utils.GetTryOnQuotaStatus(ctx, userID, plan)
	if err != nil {
		utils.RespondError(w, nil, "Failed to load quota: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Surface the upstream circuit state so the app can grey out the try-on
	// button instead of letting users queue up doomed requests. During the
	// production outage this endpoint was called 24 times and cheerfully
	// reported healthy every single time.
	breaker := utils.GeminiBreaker.Snapshot()
	available := breaker["state"] == string(utils.StateClosed)

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"is_guest":        IsGuestFromContext(r.Context()),
		"quota":           status,
		"tryon_available": available,
		"tryon_status":    breaker,
	})
}
