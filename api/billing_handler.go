package api

import (
	"context"
	"net/http"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// BillingStatusHandler returns the caller's star balance, free entitlements
// and the upstream's health. Powers the balance pill and the quality
// selector's affordability state.
//
// The legacy `quota` block is still emitted, and must stay: app builds
// already in users' hands read `quota.remaining` to decide whether to grey
// out the try-on button. It is synthesised from the star state rather than
// from the old daily counter, so an old client blocks exactly when a new one
// would — see synthesiseLegacyQuota.
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
	isGuest := IsGuestFromContext(r.Context())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stars, err := utils.GetStarSummary(ctx, userID, isGuest)
	if err != nil {
		utils.RespondError(w, nil, "Failed to load balance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Surface the upstream circuit state so the app can grey out the try-on
	// button instead of letting users queue up doomed requests. During the
	// production outage this endpoint was called 24 times and cheerfully
	// reported healthy every single time.
	breaker := utils.GeminiBreaker.Snapshot()
	available := breaker["state"] == string(utils.StateClosed)

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"is_guest":        isGuest,
		"plan":            plan,
		"stars":           stars,
		"quota":           synthesiseLegacyQuota(plan, stars),
		"tryon_available": available,
		"tryon_status":    breaker,
	})
}

// synthesiseLegacyQuota renders the star state in the shape older app builds
// expect.
//
// Those builds treat `limit == 0` as unlimited and `remaining <= 0` as "come
// back tomorrow". Mapping "can this user generate anything right now?" onto
// that pair keeps them correct: a user holding stars sees no cap, and a user
// with neither stars nor a free try-on is blocked — which is exactly the new
// behaviour, just expressed in the old vocabulary.
func synthesiseLegacyQuota(plan string, s utils.StarSummary) utils.QuotaStatus {
	canGenerate := planBypassesStars(plan) ||
		s.FreeAvailable ||
		s.Stars >= config.Stars.CheapestTierStars()

	if canGenerate {
		return utils.QuotaStatus{
			Plan: plan, Limit: 0, Used: 0, Remaining: -1, Date: s.Date,
		}
	}
	return utils.QuotaStatus{
		Plan: plan, Limit: 1, Used: 1, Remaining: 0, Date: s.Date,
	}
}
