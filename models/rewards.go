package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Collection names for earned stars. Same reasoning as the star collections
// in stars.go: declared once so a typo is a compile error rather than a
// silently empty query.
const (
	CollReferralCodes       = "referral_codes"
	CollReferralRedemptions = "referral_redemptions"
	CollReviewRewards       = "review_rewards"
	CollDeletionFeedback    = "account_deletion_feedback"
)

// Ledger reasons for earned stars. Distinct from ReasonGrant so the in-app
// history can label them precisely — "Referral bonus" tells a user where 50
// stars came from; "Grant" starts a support email.
const (
	ReasonReferral = "referral"
	ReasonReview   = "review"
)

// ReferralCode is one user's shareable code. Exactly one document per user:
// _id is the code itself, which makes the redemption lookup a primary-key
// read and makes code uniqueness a property of the collection rather than
// something the generator has to guarantee.
type ReferralCode struct {
	Code   string `bson:"_id" json:"code"`
	UserID string `bson:"user_id" json:"user_id"`

	// Redemptions and StarsEarned are denormalised counters for the referral
	// screen. The redemption documents are authoritative; these exist so
	// rendering the screen is one read instead of an aggregation.
	Redemptions int `bson:"redemptions" json:"redemptions"`
	StarsEarned int `bson:"stars_earned" json:"stars_earned"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// ReferralRedemption records that one identity used one code.
//
// _id is the *peppered email hash* of the redeeming account, not its user id.
// That is the whole anti-farming design: the record survives account deletion
// (it holds no readable personal data — see SignupIdentity for the same
// reasoning), so deleting and re-registering cannot claim a second referral
// bonus. Using the user id would reset the guard on every re-signup.
type ReferralRedemption struct {
	RefereeEmailHash string `bson:"_id" json:"-"`

	Code             string `bson:"code" json:"code"`
	ReferrerUserID   string `bson:"referrer_user_id" json:"referrer_user_id"`
	RefereeUserID    string `bson:"referee_user_id" json:"referee_user_id"`
	RefereeStars     int    `bson:"referee_stars" json:"referee_stars"`
	ReferrerStars    int    `bson:"referrer_stars" json:"referrer_stars"`
	ReferrerCredited bool   `bson:"referrer_credited" json:"referrer_credited"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// ReviewReward records that an identity has claimed the store-review reward.
//
// Keyed on the email hash for the same reason as ReferralRedemption: the
// reward is once per person, not once per account, and an account is cheap to
// recreate.
//
// The grant is an honour-system one — no store exposes an API that proves a
// given user left a review — so it is bounded rather than verified: one
// claim, ever, worth rewards.review.stars. Claimed is what that bound is
// made of.
type ReviewReward struct {
	EmailHash string `bson:"_id" json:"-"`

	UserID   string `bson:"user_id" json:"user_id"`
	Stars    int    `bson:"stars" json:"stars"`
	Platform string `bson:"platform,omitempty" json:"platform,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// Deletion reasons the app offers. The app sends the key; the server stores
// both the key (queryable) and the free-text detail (readable).
const (
	DeleteReasonNotUseful    = "not_useful"
	DeleteReasonPoorResults  = "poor_results"
	DeleteReasonTooExpensive = "too_expensive"
	DeleteReasonPrivacy      = "privacy"
	DeleteReasonBugs         = "bugs"
	DeleteReasonFoundAlt     = "found_alternative"
	DeleteReasonTemporary    = "temporary_break"
	DeleteReasonOther        = "other"
)

// DeletionFeedback is why someone left.
//
// Deliberately holds no email address or name. The account is being deleted;
// retaining contact details in a survey table would undo that, and the reason
// is useful in aggregate rather than per person. UserID is kept because it is
// an opaque id that still joins to the (soft-deleted) user row for a support
// question, and the usage snapshot is what makes the reason interpretable —
// "too expensive" from someone who ran 40 generations means something
// different from the same answer after two.
type DeletionFeedback struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID string             `bson:"user_id" json:"user_id"`

	Reason  string `bson:"reason" json:"reason"`
	Details string `bson:"details,omitempty" json:"details,omitempty"`

	// Usage snapshot at the moment of deletion.
	StarsAtDeletion     int `bson:"stars_at_deletion" json:"stars_at_deletion"`
	LifetimeGenerations int `bson:"lifetime_generations" json:"lifetime_generations"`
	LifetimePurchased   int `bson:"lifetime_purchased_stars" json:"lifetime_purchased_stars"`
	AccountAgeDays      int `bson:"account_age_days" json:"account_age_days"`

	AppVersion string    `bson:"app_version,omitempty" json:"app_version,omitempty"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`
}
