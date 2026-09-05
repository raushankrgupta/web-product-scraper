package utils

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Redemption outcomes the API turns into distinct, actionable messages. A
// single "invalid code" for all of them is what makes a referral feature feel
// broken: the user cannot tell a typo from their own code from one they have
// already used.
var (
	ErrReferralsDisabled   = errors.New("referrals are not enabled")
	ErrReferralCodeUnknown = errors.New("referral code not found")
	ErrReferralSelf        = errors.New("cannot redeem your own referral code")
	ErrReferralAlready     = errors.New("this account has already used a referral code")
	ErrReferralWindow      = errors.New("referral code redemption window has passed")
	ErrReferralExhausted   = errors.New("this referral code has reached its limit")

	ErrReviewDisabled = errors.New("review rewards are not enabled")
	ErrReviewClaimed  = errors.New("the review reward has already been claimed")
)

// codeAlphabet deliberately omits O/0 and I/1/L. A referral code gets read
// aloud, screenshotted and retyped, and a code that resolves only if you
// guess the right glyph is a code that silently fails.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func referralCodes() *mongo.Collection {
	return GetCollection(config.DBName, models.CollReferralCodes)
}
func referralRedemptions() *mongo.Collection {
	return GetCollection(config.DBName, models.CollReferralRedemptions)
}
func reviewRewards() *mongo.Collection {
	return GetCollection(config.DBName, models.CollReviewRewards)
}

// ---------------------------------------------------------------- referral

// NormaliseReferralCode maps user input onto the stored form. Codes arrive
// pasted with whitespace, lower-cased by a keyboard's autocorrect, or wrapped
// in the punctuation of the sentence they were shared in.
func NormaliseReferralCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		if strings.ContainsRune(codeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// EnsureReferralCode returns the user's code, minting one on first request.
//
// The code is the document's _id, so uniqueness is enforced by the collection
// rather than by the generator: a collision is a duplicate-key error we retry,
// not a race we have to reason about. The user_id unique index is what stops
// a user ending up with two codes if they open the screen twice at once.
func EnsureReferralCode(ctx context.Context, userID string) (*models.ReferralCode, error) {
	if !config.Stars.Rewards.Referral.Enabled {
		return nil, ErrReferralsDisabled
	}

	var existing models.ReferralCode
	err := referralCodes().FindOne(ctx, bson.M{"user_id": userID}).Decode(&existing)
	if err == nil {
		return &existing, nil
	}
	if err != mongo.ErrNoDocuments {
		return nil, fmt.Errorf("load referral code: %w", err)
	}

	length := config.Stars.Rewards.Referral.CodeLength
	for attempt := 0; attempt < 8; attempt++ {
		code, genErr := randomCode(length)
		if genErr != nil {
			return nil, genErr
		}

		doc := models.ReferralCode{
			Code: code, UserID: userID, CreatedAt: time.Now(),
		}
		if _, insErr := referralCodes().InsertOne(ctx, doc); insErr != nil {
			if mongo.IsDuplicateKeyError(insErr) {
				// Either the code collided (retry with a new one) or a
				// concurrent request already minted this user's code — a
				// re-read settles which.
				var raced models.ReferralCode
				if referralCodes().FindOne(ctx, bson.M{"user_id": userID}).Decode(&raced) == nil {
					return &raced, nil
				}
				continue
			}
			return nil, fmt.Errorf("create referral code: %w", insErr)
		}
		slog.Info("referral code minted", "user_id", userID)
		return &doc, nil
	}
	return nil, fmt.Errorf("could not generate a unique referral code after 8 attempts")
}

// randomCode draws from crypto/rand rather than math/rand. A predictable code
// sequence would let someone enumerate other users' codes, and every valid
// code is worth stars.
func randomCode(length int) (string, error) {
	max := big.NewInt(int64(len(codeAlphabet)))
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate referral code: %w", err)
		}
		b[i] = codeAlphabet[n.Int64()]
	}
	return string(b), nil
}

// ReferralStats is the referral screen's payload.
type ReferralStats struct {
	Code           string `json:"code"`
	Redemptions    int    `json:"redemptions"`
	StarsEarned    int    `json:"stars_earned"`
	ReferrerStars  int    `json:"referrer_stars"`
	RefereeStars   int    `json:"referee_stars"`
	MaxRedemptions int    `json:"max_redemptions"`

	// Redeemed reports whether this account has itself used someone's code,
	// so the app can hide the entry field instead of offering an action that
	// can only fail.
	Redeemed bool `json:"redeemed"`
	// CanRedeem is false once the account is older than the redemption
	// window, for the same reason.
	CanRedeem bool `json:"can_redeem"`
}

// GetReferralStats loads (or mints) a user's code and their earnings.
func GetReferralStats(ctx context.Context, userID, email string, accountCreatedAt time.Time) (*ReferralStats, error) {
	rc, err := EnsureReferralCode(ctx, userID)
	if err != nil {
		return nil, err
	}
	cfg := config.Stars.Rewards.Referral

	redeemed := false
	if email != "" {
		n, cErr := referralRedemptions().CountDocuments(ctx, bson.M{"_id": EmailIdentityHash(email)})
		if cErr == nil && n > 0 {
			redeemed = true
		}
	}

	return &ReferralStats{
		Code:           rc.Code,
		Redemptions:    rc.Redemptions,
		StarsEarned:    rc.StarsEarned,
		ReferrerStars:  cfg.ReferrerStars,
		RefereeStars:   cfg.RefereeStars,
		MaxRedemptions: cfg.MaxRedemptionsPerReferrer,
		Redeemed:       redeemed,
		CanRedeem:      !redeemed && withinRedeemWindow(accountCreatedAt),
	}, nil
}

func withinRedeemWindow(accountCreatedAt time.Time) bool {
	if accountCreatedAt.IsZero() {
		// An unknown creation date is an old document, not a new signup.
		// Failing closed here costs one referral; failing open turns the
		// window into decoration.
		return false
	}
	window := time.Duration(config.Stars.Rewards.Referral.RedeemWindowHours) * time.Hour
	return time.Since(accountCreatedAt) <= window
}

// RedeemResult reports what a successful redemption paid out.
type RedeemResult struct {
	Code          string `json:"code"`
	RefereeStars  int    `json:"referee_stars"`
	ReferrerStars int    `json:"referrer_stars"`
}

// RedeemReferral credits both sides of a referral, exactly once per identity.
//
// The order here is the whole design. The redemption document is inserted
// *first*, keyed on the referee's peppered email hash, and its uniqueness is
// what makes the payout one-shot — two concurrent requests, or a retry after
// a dropped connection, both hit a duplicate-key error and only one proceeds
// to grant. Granting first and recording after would pay out twice on any
// retry, which is the standard way referral programmes leak money.
//
// Keying on the email hash rather than the user id is what makes
// delete-and-rejoin useless: the record holds no readable personal data (same
// reasoning as SignupIdentity) so it legitimately outlives the account.
func RedeemReferral(ctx context.Context, refereeUserID, refereeEmail, rawCode string, accountCreatedAt time.Time) (*RedeemResult, error) {
	cfg := config.Stars.Rewards.Referral
	if !cfg.Enabled {
		return nil, ErrReferralsDisabled
	}

	code := NormaliseReferralCode(rawCode)
	if code == "" {
		return nil, ErrReferralCodeUnknown
	}
	if strings.TrimSpace(refereeEmail) == "" {
		// Guests have no durable identity to guard the one-shot rule with.
		// The handler rejects them earlier; this is defence in depth.
		return nil, ErrReferralsDisabled
	}
	if !withinRedeemWindow(accountCreatedAt) {
		return nil, ErrReferralWindow
	}

	var owner models.ReferralCode
	if err := referralCodes().FindOne(ctx, bson.M{"_id": code}).Decode(&owner); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrReferralCodeUnknown
		}
		return nil, fmt.Errorf("look up referral code: %w", err)
	}
	if owner.UserID == refereeUserID {
		return nil, ErrReferralSelf
	}
	if owner.Redemptions >= cfg.MaxRedemptionsPerReferrer {
		return nil, ErrReferralExhausted
	}

	redemption := models.ReferralRedemption{
		RefereeEmailHash: EmailIdentityHash(refereeEmail),
		Code:             code,
		ReferrerUserID:   owner.UserID,
		RefereeUserID:    refereeUserID,
		RefereeStars:     cfg.RefereeStars,
		ReferrerStars:    cfg.ReferrerStars,
		CreatedAt:        time.Now(),
	}
	if _, err := referralRedemptions().InsertOne(ctx, redemption); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrReferralAlready
		}
		return nil, fmt.Errorf("record referral redemption: %w", err)
	}

	// From here the redemption is committed. A failure below costs a user
	// their stars, so both grants are logged loudly rather than swallowed —
	// the redemption row is the record support needs to fix it by hand.
	if err := GrantStars(ctx, refereeUserID, cfg.RefereeStars,
		models.ReasonReferral, "Referral bonus"); err != nil {
		slog.Error("referral referee grant failed after redemption was recorded",
			"user_id", refereeUserID, "code", code, "error", err)
	}

	if err := GrantStars(ctx, owner.UserID, cfg.ReferrerStars,
		models.ReasonReferral, "Friend joined with your code"); err != nil {
		slog.Error("referral referrer grant failed after redemption was recorded",
			"user_id", owner.UserID, "code", code, "error", err)
	} else {
		// Counters are advisory (the redemption documents are the truth), so
		// a failure to bump them must not fail the redemption.
		if _, err := referralCodes().UpdateOne(ctx,
			bson.M{"_id": code},
			bson.M{"$inc": bson.M{"redemptions": 1, "stars_earned": cfg.ReferrerStars}},
		); err != nil {
			slog.Warn("referral counter update failed", "code", code, "error", err)
		}
		if _, err := referralRedemptions().UpdateOne(ctx,
			bson.M{"_id": redemption.RefereeEmailHash},
			bson.M{"$set": bson.M{"referrer_credited": true}},
		); err != nil {
			slog.Warn("referral credit flag update failed", "code", code, "error", err)
		}
	}

	slog.Info("referral redeemed",
		"code", code, "referrer", owner.UserID, "referee", refereeUserID,
		"referee_stars", cfg.RefereeStars, "referrer_stars", cfg.ReferrerStars)

	return &RedeemResult{
		Code: code, RefereeStars: cfg.RefereeStars, ReferrerStars: cfg.ReferrerStars,
	}, nil
}

// ------------------------------------------------------------------ review

// ReviewRewardStatus tells the app whether to offer the reward at all.
type ReviewRewardStatus struct {
	Available bool `json:"available"`
	Claimed   bool `json:"claimed"`
	Stars     int  `json:"stars"`
}

// GetReviewRewardStatus reports whether this identity can still claim.
func GetReviewRewardStatus(ctx context.Context, email string) ReviewRewardStatus {
	cfg := config.Stars.Rewards.Review
	if !cfg.Enabled || strings.TrimSpace(email) == "" {
		return ReviewRewardStatus{Stars: cfg.Stars}
	}

	claimed := false
	if n, err := reviewRewards().CountDocuments(ctx, bson.M{"_id": EmailIdentityHash(email)}); err == nil {
		claimed = n > 0
	}
	return ReviewRewardStatus{Available: !claimed, Claimed: claimed, Stars: cfg.Stars}
}

// ClaimReviewReward grants the store-review bonus, once per identity ever.
//
// This is an honour-system grant: neither Google Play nor the App Store
// exposes an API that proves a *specific* user left a review, and Play's
// in-app review flow deliberately returns no signal about what the user did.
// So the reward is bounded rather than verified — the insert below is the
// bound, and a unique _id on the peppered email hash is what enforces it
// through account deletion and re-registration.
//
// The grant is for leaving a review, never for the score. Google Play's
// Developer Program Policy forbids incentivising ratings, and conditioning
// this on a star count is what turns a growth feature into a listing
// takedown. Keep the app copy aligned.
func ClaimReviewReward(ctx context.Context, userID, email, platform string) (int, error) {
	cfg := config.Stars.Rewards.Review
	if !cfg.Enabled {
		return 0, ErrReviewDisabled
	}
	if strings.TrimSpace(email) == "" {
		return 0, ErrReviewDisabled // guests have no durable identity to bound on
	}

	record := models.ReviewReward{
		EmailHash: EmailIdentityHash(email),
		UserID:    userID,
		Stars:     cfg.Stars,
		Platform:  platform,
		CreatedAt: time.Now(),
	}
	if _, err := reviewRewards().InsertOne(ctx, record); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return 0, ErrReviewClaimed
		}
		return 0, fmt.Errorf("record review reward: %w", err)
	}

	if err := GrantStars(ctx, userID, cfg.Stars, models.ReasonReview, "Thanks for reviewing us"); err != nil {
		slog.Error("review reward grant failed after the claim was recorded",
			"user_id", userID, "error", err)
		return 0, err
	}
	return cfg.Stars, nil
}

// -------------------------------------------------------- deletion feedback

// SaveDeletionFeedback records why someone deleted their account.
//
// Best-effort by design: it is called from the deletion path, and a failure
// to store a survey answer must never block a user from deleting their
// account. That is a data-rights obligation, not a preference.
//
// The snapshot is taken before the balance is purged, because "too expensive"
// from someone who generated forty images means something entirely different
// from the same answer after two.
func SaveDeletionFeedback(ctx context.Context, fb models.DeletionFeedback) {
	if strings.TrimSpace(fb.Reason) == "" && strings.TrimSpace(fb.Details) == "" {
		return // the user skipped the survey
	}

	if b, err := GetOrCreateBalance(ctx, fb.UserID); err == nil {
		fb.StarsAtDeletion = b.Stars
		fb.LifetimeGenerations = b.LifetimeGenerations
		fb.LifetimePurchased = b.LifetimePurchasedStars
	}
	fb.CreatedAt = time.Now()

	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := GetCollection(config.DBName, models.CollDeletionFeedback).
		InsertOne(writeCtx, fb); err != nil {
		slog.Warn("failed to save account deletion feedback", "error", err)
		return
	}
	slog.Info("account deletion feedback recorded",
		"user_id", fb.UserID, "reason", fb.Reason,
		"lifetime_generations", fb.LifetimeGenerations,
		"account_age_days", fb.AccountAgeDays)
}

// ------------------------------------------------------------ test support

// ForgetRewardClaims deletes an identity's referral redemption and review
// claim so both can be exercised again.
//
// Test-only, and the counterpart to ForgetSignupIdentity: without it the
// first end-to-end referral test permanently marks your own address as having
// redeemed, and the flow becomes untestable on that account forever.
func ForgetRewardClaims(ctx context.Context, email string) (int64, error) {
	hash := EmailIdentityHash(email)

	r1, err := referralRedemptions().DeleteOne(ctx, bson.M{"_id": hash})
	if err != nil {
		return 0, fmt.Errorf("forget referral redemption: %w", err)
	}
	r2, err := reviewRewards().DeleteOne(ctx, bson.M{"_id": hash})
	if err != nil {
		return r1.DeletedCount, fmt.Errorf("forget review reward: %w", err)
	}
	return r1.DeletedCount + r2.DeletedCount, nil
}

// PurgeReferralCode removes a deleted account's code so it stops resolving.
//
// The redemption records are deliberately kept: they are the anti-farming
// guard and hold only opaque ids and a peppered hash. The code itself is
// removed because it is a live handle that would otherwise pay stars to an
// account that no longer exists.
func PurgeReferralCode(ctx context.Context, userID string) {
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := referralCodes().DeleteOne(writeCtx, bson.M{"user_id": userID}); err != nil {
		slog.Warn("failed to purge referral code on account deletion",
			"user_id", userID, "error", err)
	}
}

// countReferralRedemptions is used by the dev inspect endpoint.
func CountReferralRedemptions(ctx context.Context, userID string) int64 {
	n, err := referralRedemptions().CountDocuments(ctx, bson.M{"referrer_user_id": userID},
		options.Count().SetMaxTime(3*time.Second))
	if err != nil {
		return 0
	}
	return n
}
