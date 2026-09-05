package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Earned stars: the two ways to get stars without paying.
//
// Both rewards are bounded by an identity rather than by an account —
// see utils.RedeemReferral and utils.ClaimReviewReward for why. These
// handlers are the thin part: they resolve the caller's email and signup
// date, hand off, and translate the sentinel errors into status codes the
// app can branch on without parsing prose.

// rewardIdentity is what both rewards need about the caller and what neither
// can get from the auth context: the email hash is the one-shot key, and the
// signup date bounds the referral window.
type rewardIdentity struct {
	UserID    string
	Email     string
	CreatedAt time.Time
}

func loadRewardIdentity(ctx context.Context, userID string) (rewardIdentity, error) {
	objID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return rewardIdentity{}, err
	}

	var user struct {
		Email     string    `bson:"email"`
		CreatedAt time.Time `bson:"created_at"`
	}
	if err := utils.GetCollection(config.DBName, "users").
		FindOne(ctx, bson.M{"_id": objID}).Decode(&user); err != nil {
		return rewardIdentity{}, err
	}
	return rewardIdentity{UserID: userID, Email: user.Email, CreatedAt: user.CreatedAt}, nil
}

// rewardCaller resolves the caller, rejecting guests.
//
// Guests are refused rather than served an empty state because both rewards
// are one-per-identity and a guest token has no durable identity to bind
// that to — a guest could claim the review bonus indefinitely.
func rewardCaller(w http.ResponseWriter, r *http.Request) (rewardIdentity, context.Context, context.CancelFunc, bool) {
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return rewardIdentity{}, nil, nil, false
	}
	if IsGuestFromContext(r.Context()) {
		utils.RespondErrorReason(w, nil, "Sign in to earn stars", "guest_not_eligible", http.StatusForbidden)
		return rewardIdentity{}, nil, nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	id, err := loadRewardIdentity(ctx, userID)
	if err != nil {
		cancel()
		utils.RespondError(w, nil, "Account not found", http.StatusUnauthorized)
		return rewardIdentity{}, nil, nil, false
	}
	return id, ctx, cancel, true
}

// RewardsHandler is the one read the rewards screen needs: the referral code
// and both payouts, plus whether the review bonus is still claimable.
//
// Served as a single endpoint because the screen renders both together and
// two round-trips would show it half-populated.
func RewardsHandler(w http.ResponseWriter, r *http.Request) {
	id, ctx, cancel, ok := rewardCaller(w, r)
	if !ok {
		return
	}
	defer cancel()

	payload := map[string]interface{}{
		"review": utils.GetReviewRewardStatus(ctx, id.Email),
	}

	if config.Stars.Rewards.Referral.Enabled {
		stats, err := utils.GetReferralStats(ctx, id.UserID, id.Email, id.CreatedAt)
		if err != nil {
			utils.RespondInternalError(w, r, nil, "rewards",
				"Couldn't load your referral code", err, http.StatusInternalServerError)
			return
		}
		payload["referral"] = stats
	} else {
		payload["referral"] = nil
	}

	utils.RespondJSON(w, http.StatusOK, payload)
}

// ReferralHandler returns just the referral half, for the share sheet.
func ReferralHandler(w http.ResponseWriter, r *http.Request) {
	id, ctx, cancel, ok := rewardCaller(w, r)
	if !ok {
		return
	}
	defer cancel()

	if !config.Stars.Rewards.Referral.Enabled {
		utils.RespondErrorReason(w, nil, "Referrals aren't available right now",
			"referrals_disabled", http.StatusServiceUnavailable)
		return
	}

	stats, err := utils.GetReferralStats(ctx, id.UserID, id.Email, id.CreatedAt)
	if err != nil {
		utils.RespondInternalError(w, r, nil, "rewards",
			"Couldn't load your referral code", err, http.StatusInternalServerError)
		return
	}
	utils.RespondJSON(w, http.StatusOK, stats)
}

type redeemReferralRequest struct {
	Code string `json:"code"`
}

// RedeemReferralHandler credits both sides of a referral.
//
// Every failure mode carries a machine-readable `reason` because the app
// shows a different thing for each: an unknown code is a typo to correct, an
// already-redeemed one is a state to reflect, and an expired window is a
// dead end that should stop offering the field.
func RedeemReferralHandler(w http.ResponseWriter, r *http.Request) {
	id, ctx, cancel, ok := rewardCaller(w, r)
	if !ok {
		return
	}
	defer cancel()

	var req redeemReferralRequest
	body := http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		utils.RespondErrorReason(w, nil, "Invalid request", "bad_request", http.StatusBadRequest)
		return
	}

	result, err := utils.RedeemReferral(ctx, id.UserID, id.Email, req.Code, id.CreatedAt)
	if err != nil {
		status, reason, msg := referralErrorResponse(err)
		if status == http.StatusInternalServerError {
			utils.RespondInternalError(w, r, nil, "rewards", msg, err, status)
			return
		}
		utils.RespondErrorReason(w, nil, msg, reason, status)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"code":           result.Code,
		"stars_awarded":  result.RefereeStars,
		"referrer_stars": result.ReferrerStars,
		"message":        "Referral applied",
	})
}

func referralErrorResponse(err error) (status int, reason, msg string) {
	switch {
	case errors.Is(err, utils.ErrReferralsDisabled):
		return http.StatusServiceUnavailable, "referrals_disabled",
			"Referrals aren't available right now"
	case errors.Is(err, utils.ErrReferralCodeUnknown):
		return http.StatusNotFound, "code_unknown",
			"That code doesn't look right. Check it and try again."
	case errors.Is(err, utils.ErrReferralSelf):
		return http.StatusBadRequest, "code_self",
			"That's your own code — share it with a friend instead."
	case errors.Is(err, utils.ErrReferralAlready):
		return http.StatusConflict, "already_redeemed",
			"You've already used a referral code."
	case errors.Is(err, utils.ErrReferralWindow):
		return http.StatusForbidden, "window_closed",
			"Referral codes can only be used on a new account."
	case errors.Is(err, utils.ErrReferralExhausted):
		return http.StatusConflict, "code_exhausted",
			"That code has reached its limit."
	default:
		return http.StatusInternalServerError, "server_error",
			"Couldn't apply that code"
	}
}

type reviewRewardRequest struct {
	Platform string `json:"platform"`
}

// ReviewRewardHandler grants the store-review bonus.
//
// The grant is for *leaving a review*, never for the score. Google Play's
// Developer Program Policy forbids incentivising ratings, so there is no
// rating field to send here and the app copy must not mention one.
func ReviewRewardHandler(w http.ResponseWriter, r *http.Request) {
	id, ctx, cancel, ok := rewardCaller(w, r)
	if !ok {
		return
	}
	defer cancel()

	var req reviewRewardRequest
	body := http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		utils.RespondErrorReason(w, nil, "Invalid request", "bad_request", http.StatusBadRequest)
		return
	}
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if platform != "ios" {
		platform = "android"
	}

	stars, err := utils.ClaimReviewReward(ctx, id.UserID, id.Email, platform)
	switch {
	case errors.Is(err, utils.ErrReviewClaimed):
		utils.RespondErrorReason(w, nil, "You've already claimed this reward",
			"already_claimed", http.StatusConflict)
		return
	case errors.Is(err, utils.ErrReviewDisabled):
		utils.RespondErrorReason(w, nil, "That reward isn't available right now",
			"review_disabled", http.StatusServiceUnavailable)
		return
	case err != nil:
		utils.RespondInternalError(w, r, nil, "rewards",
			"Couldn't add your stars", err, http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"stars_awarded": stars,
		"reason":        models.ReasonReview,
		"message":       "Thanks — your stars have been added",
	})
}
