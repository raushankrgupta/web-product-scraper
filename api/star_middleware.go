package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

const (
	// ReservationKey carries the star hold taken for this request.
	ReservationKey contextKey = "star_reservation"
	// QualityKey carries the resolved quality tier, so handlers pick the
	// right model without re-parsing the body.
	QualityKey contextKey = "tryon_quality"
)

// starRequestEnvelope is the subset of every try-on payload the billing layer
// needs. Decoded separately from the handler's own struct so that adding a
// field here never risks changing how a generation request is interpreted.
type starRequestEnvelope struct {
	Quality string `json:"quality"`
	// IdempotencyKey lets a client retry safely. When absent we fall back to
	// a fingerprint of the request body, which gives the same protection for
	// older app builds that do not send one.
	IdempotencyKey string `json:"idempotency_key"`
}

// tryOnTypeForPath maps a route onto the type priced in config/stars.json.
// The legacy /try-on endpoint and the one-shot guest endpoint are both
// single-person generations and are priced as individual.
func tryOnTypeForPath(path string) string {
	switch {
	case strings.HasSuffix(path, "/couple"):
		return "couple"
	case strings.HasSuffix(path, "/group"):
		return "group"
	default:
		return "individual"
	}
}

// planBypassesStars reports whether a plan is billed some other way. No
// subscription product exists yet, but accounts flagged plus/pro predate the
// star system and must not suddenly find themselves unable to generate.
func planBypassesStars(plan string) bool {
	return plan == models.PlanPlus || plan == models.PlanPro
}

// StarGateMiddleware reserves the cost of a generation before the handler
// runs, and settles that reservation afterwards: committed on success,
// refunded on any failure.
//
// It replaces QuotaMiddleware on the try-on routes. The ordering that matters
// is that the debit happens *before* the upstream call and the refund happens
// on *every* non-success path — a user must never pay for an image they did
// not receive, and must never receive one they did not pay for.
//
// Wrap inside AuthMiddleware and TryOnGuardMiddleware, so that duplicate
// in-flight requests are rejected before they can take a second hold.
func StarGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := GetUserIDFromContext(r.Context())
		if err != nil {
			utils.RespondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Please sign in again to continue."})
			return
		}
		plan := GetUserPlanFromContext(r.Context())
		isGuest := IsGuestFromContext(r.Context())
		tryOnType := tryOnTypeForPath(r.URL.Path)

		// Read the billing envelope, then put the body back so the handler
		// decodes it exactly as before.
		var env starRequestEnvelope
		if isJSONRequest(r) {
			body, readErr := io.ReadAll(io.LimitReader(r.Body, maxJSONBody))
			r.Body.Close()
			if readErr != nil {
				utils.RespondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			_ = json.Unmarshal(body, &env)
			if env.IdempotencyKey == "" {
				env.IdempotencyKey = userID + ":" + r.URL.Path + ":" + fingerprintBody(body)
			}
		} else {
			// Multipart guest upload. The body holds an image and cannot be
			// buffered cheaply enough to fingerprint, so the key is random.
			//
			// Random rather than time-bucketed on purpose: a bucketed key
			// would let two taps inside the same window share one hold, so
			// both generations would run against a single charge. Random
			// means the second attempt needs its own free allowance and is
			// correctly refused once the daily one is used.
			env.IdempotencyKey = userID + ":" + r.URL.Path + ":" + uuid.NewString()
		}

		quality := config.Stars.NormaliseQuality(env.Quality)
		if isGuest {
			// Guests cannot buy stars, so offering them the paid tier would
			// be a dead end. Pin them to the free quality.
			quality = config.Stars.Free.FreeQuality
		}

		ctx := context.WithValue(r.Context(), QualityKey, quality)

		// Legacy paid plans keep their old unlimited behaviour and are not
		// charged stars.
		if planBypassesStars(plan) {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		res, err := utils.ReserveGeneration(r.Context(), userID, tryOnType, quality, env.IdempotencyKey, isGuest)
		if err != nil {
			respondReserveError(w, r, err, userID, tryOnType, quality, isGuest)
			return
		}

		ctx = context.WithValue(ctx, ReservationKey, res)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		settle(r, userID, res, rec)
	})
}

// settle commits or refunds the hold based on what the handler actually did.
// It runs on its own background context: a client that hangs up mid-generation
// must still get its stars back.
func settle(r *http.Request, userID string, res utils.Reservation, rec *statusRecorder) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A response served from the in-process result cache did not call the
	// upstream and cost us nothing, so it must not cost the user either.
	cached := rec.Header().Get(CachedResultHeader) == "1"
	success := rec.status >= 200 && rec.status < 300

	if success && !cached {
		if err := utils.CommitReservation(ctx, userID, res); err != nil {
			utils.L(r.Context()).Error("failed to commit star hold",
				"user_id", userID, "hold_id", res.HoldID, "error", err.Error())
		}
		return
	}

	if err := utils.ReleaseReservation(ctx, userID, res); err != nil {
		// This is the failure that actually costs a user money, so it is
		// logged at error level with everything needed to refund by hand —
		// and persisted, because a log line naming a hold id is worthless
		// once the log has rotated and somebody finally asks who is owed what.
		utils.L(r.Context()).Error("failed to refund star hold — manual refund may be needed",
			"user_id", userID, "hold_id", res.HoldID, "source", res.Source,
			"amount", res.Amount, "error", err.Error())
		recordRefundFailure(r, userID, res, err)
	}
}

// respondReserveError turns a reservation failure into a response the app can
// act on. 402 carries the exact shortfall and the catalogue, so the store
// sheet can open with the right pack already selected instead of making the
// user work out what to buy.
func respondReserveError(w http.ResponseWriter, r *http.Request, err error,
	userID, tryOnType, quality string, isGuest bool) {

	if errors.Is(err, utils.ErrUnknownTier) {
		recordGateRejection(r, "unknown_tier", http.StatusBadRequest, err.Error())
		utils.RespondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": "That combination is not available.",
		})
		return
	}

	if !errors.Is(err, utils.ErrInsufficientFunds) {
		recordGateRejection(r, "reserve_failed", http.StatusInternalServerError, err.Error())
		utils.RespondInternalError(w, r, nil, "stars",
			"We couldn't start that try-on. Please try again.", err, http.StatusInternalServerError)
		return
	}

	cost, _ := config.Stars.TierCost(tryOnType, quality)

	summary, sumErr := utils.GetStarSummary(r.Context(), userID, isGuest)
	balance := 0
	if sumErr == nil {
		balance = summary.Stars
	}

	if isGuest {
		// A guest has no balance to top up — the next step is signing up,
		// which is also where the welcome credits are.
		recordGateRejection(r, "guest_limit", http.StatusPaymentRequired,
			fmt.Sprintf("guest out of free try-ons (cost=%d)", cost))
		utils.RespondJSON(w, http.StatusPaymentRequired, map[string]interface{}{
			"error":                  "You've used today's free try-on. Sign up free to get more.",
			"reason":                 "guest_limit",
			"signup_cta":             true,
			"free_credits_on_signup": config.Stars.Free.WelcomeCredits,
		})
		return
	}

	recordGateRejection(r, "insufficient_stars", http.StatusPaymentRequired,
		fmt.Sprintf("cost=%d balance=%d", cost, balance))
	utils.RespondJSON(w, http.StatusPaymentRequired, map[string]interface{}{
		"error":    "You don't have enough stars for this try-on.",
		"reason":   "insufficient_stars",
		"required": cost,
		"balance":  balance,
		"shortfall": func() int {
			if d := cost - balance; d > 0 {
				return d
			}
			return 0
		}(),
		"tryon_type": tryOnType,
		"quality":    quality,
		"packs":      config.Stars.SortedPacks(),
	})
}

// GetReservationFromContext returns the hold taken for this request, if any.
// Absent for plans that bypass star charging.
func GetReservationFromContext(ctx context.Context) (utils.Reservation, bool) {
	r, ok := ctx.Value(ReservationKey).(utils.Reservation)
	return r, ok
}

// GetQualityFromContext returns the resolved quality tier for this request.
// Falls back to the configured default so a handler can never accidentally
// pick the expensive model because the context was not populated.
func GetQualityFromContext(ctx context.Context) string {
	q, ok := ctx.Value(QualityKey).(string)
	if !ok || q == "" {
		return config.Stars.DefaultQuality
	}
	return q
}
