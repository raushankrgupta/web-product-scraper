package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Collection names for the star economy. Referenced from utils/stars.go and
// utils/indexes.go; declared here so a typo is a compile error rather than a
// silently empty query.
const (
	CollStarBalances     = "star_balances"
	CollStarLedger       = "star_ledger"
	CollStarPurchases    = "star_purchases"
	CollSignupIdentities = "signup_identities"
)

// Funding sources, in the order they are spent. Daily free goes first because
// it expires at midnight anyway; purchased stars go last because they are the
// only thing the user paid for.
const (
	FundDailyFree  = "daily_free"
	FundFreeCredit = "free_credit"
	FundStars      = "stars"
)

// Ledger entry reasons.
const (
	ReasonPurchase   = "purchase"
	ReasonSpend      = "spend"
	ReasonRefund     = "refund"
	ReasonGrant      = "grant"
	ReasonChargeback = "chargeback"
	ReasonAdjustment = "adjustment"
)

// StarHold is a reservation taken before a generation starts. It lives inside
// the balance document (not its own collection) so that "can this user afford
// it?" and "debit them" are a single atomic findOneAndUpdate — MongoDB
// guarantees atomicity per document, which is the only guarantee available
// without a replica-set transaction.
//
// The array is naturally bounded: TryOnGuardMiddleware already rejects a
// second identical in-flight request per user, and the sweeper releases
// anything older than billing.hold_expiry_minutes.
type StarHold struct {
	ID        string    `bson:"id" json:"id"`
	Amount    int       `bson:"amount" json:"amount"` // stars, or 1 for a free unit
	Source    string    `bson:"source" json:"source"` // daily_free | free_credit | stars
	Key       string    `bson:"key" json:"key"`       // client idempotency key
	TryOnType string    `bson:"tryon_type" json:"tryon_type"`
	Quality   string    `bson:"quality" json:"quality"`
	At        time.Time `bson:"at" json:"at"`
}

// StarBalance is the authoritative, mutable state for one user. Exactly one
// document per user, _id is the user id hex (or "guest:<device>").
//
// Everything a spend decision depends on lives here on purpose: the star
// balance, the granted free credits, and the daily-free counter with its own
// date stamp. A reserve is therefore one conditional update against one
// document, and can never half-apply.
type StarBalance struct {
	UserID string `bson:"_id" json:"user_id"`

	Stars       int `bson:"stars" json:"stars"`
	FreeCredits int `bson:"free_credits" json:"free_credits"`

	// FreeDay is a UTC YYYY-MM-DD stamp; FreeDayUsed counts today's free
	// generations. Rollover happens inside the reserve pipeline rather than
	// on a cron, so a user who does not open the app costs us nothing.
	FreeDay     string `bson:"free_day" json:"free_day"`
	FreeDayUsed int    `bson:"free_day_used" json:"free_day_used"`

	Held []StarHold `bson:"held" json:"held"`

	// CreditedTokens is a bounded ring of the most recently credited Play
	// purchase tokens, kept via $push with $slice:-50. It is what makes
	// crediting a purchase idempotent in a single atomic update: the filter
	// requires the token to be absent, so a retried or replayed
	// /billing/purchase call cannot add the same stars twice. The star_ledger
	// unique index is the durable record; this is the hot-path guard.
	CreditedTokens []string `bson:"credited_tokens,omitempty" json:"-"`

	// SettledHolds is the same trick applied to generations: a bounded ring
	// of hold ids that have already been charged for. CommitReservation
	// filters on it so a retried commit — or a commit that races the expiry
	// sweeper — settles the hold exactly once instead of debiting twice.
	SettledHolds []string `bson:"settled_holds,omitempty" json:"-"`

	// Lifetime counters, for support and analytics. Never used in a spend
	// decision — the live balance above is the only thing that gates.
	LifetimePurchasedStars int `bson:"lifetime_purchased_stars" json:"lifetime_purchased_stars"`
	LifetimeSpentStars     int `bson:"lifetime_spent_stars" json:"lifetime_spent_stars"`
	LifetimeGenerations    int `bson:"lifetime_generations" json:"lifetime_generations"`

	// WelcomeGranted stops the welcome bonus being issued twice if the signup
	// path is retried. Separate from the identity record, which is what stops
	// it being farmed via delete-and-rejoin.
	WelcomeGranted bool `bson:"welcome_granted" json:"welcome_granted"`
	// Returning records that this account's email had been seen before, so
	// the smaller returning grant was issued. Kept for support questions.
	Returning bool `bson:"returning" json:"returning"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// AvailableHold finds a hold by its idempotency key.
func (b *StarBalance) HoldByKey(key string) (StarHold, bool) {
	for _, h := range b.Held {
		if h.Key == key {
			return h, true
		}
	}
	return StarHold{}, false
}

// StarLedgerEntry is the append-only audit trail. The balance document above
// is what gates a spend; this is what lets us reconstruct how a balance got
// to where it is when a user disputes it.
//
// PurchaseToken carries a unique index, which is the structural guarantee
// that a Play purchase can never be credited twice — not a code path someone
// has to remember to write.
type StarLedgerEntry struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID string             `bson:"user_id" json:"user_id"`

	Delta  int    `bson:"delta" json:"delta"` // +150 purchase, -25 spend
	Reason string `bson:"reason" json:"reason"`
	Source string `bson:"source,omitempty" json:"source,omitempty"`

	// BalanceAfter is a snapshot for support; it is not authoritative.
	BalanceAfter int `bson:"balance_after" json:"balance_after"`

	// PurchaseToken is set on purchase/chargeback rows only, and is unique.
	PurchaseToken string `bson:"purchase_token,omitempty" json:"purchase_token,omitempty"`
	ProductID     string `bson:"product_id,omitempty" json:"product_id,omitempty"`
	OrderID       string `bson:"order_id,omitempty" json:"order_id,omitempty"`

	// HoldID / IdempotencyKey link a spend to the generation that caused it.
	HoldID         string `bson:"hold_id,omitempty" json:"hold_id,omitempty"`
	IdempotencyKey string `bson:"idempotency_key,omitempty" json:"idempotency_key,omitempty"`
	TryOnType      string `bson:"tryon_type,omitempty" json:"tryon_type,omitempty"`
	Quality        string `bson:"quality,omitempty" json:"quality,omitempty"`

	Note      string    `bson:"note,omitempty" json:"note,omitempty"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// Play purchase lifecycle states, mirroring the Android Publisher API's
// purchaseState plus our own terminal states.
const (
	PurchasePending   = "pending"   // UPI / netbanking not yet settled
	PurchaseCredited  = "credited"  // verified, consumed, stars added
	PurchaseCancelled = "cancelled" // user abandoned or Play cancelled it
	PurchaseRefunded  = "refunded"  // voided after crediting
	PurchaseRejected  = "rejected"  // failed verification
)

// StarPurchase records every purchase token we have ever seen, including ones
// we refused to credit.
//
// PENDING is the state that matters in India: a large share of Play purchases
// there are UPI or netbanking, which settle asynchronously. Crediting on
// PENDING gives away stars to anyone who abandons the payment sheet, so a
// pending token is recorded here and credited only when Play confirms it.
type StarPurchase struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID string             `bson:"user_id" json:"user_id"`

	PurchaseToken string `bson:"purchase_token" json:"purchase_token"` // unique
	ProductID     string `bson:"product_id" json:"product_id"`
	OrderID       string `bson:"order_id,omitempty" json:"order_id,omitempty"`

	Stars  int    `bson:"stars" json:"stars"`
	State  string `bson:"state" json:"state"`
	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`

	// PurchaseState / AcknowledgementState as reported by Google, kept raw so
	// a support question can be answered without re-querying Play.
	GooglePurchaseState int `bson:"google_purchase_state" json:"google_purchase_state"`
	GoogleAckState      int `bson:"google_ack_state" json:"google_ack_state"`

	Consumed    bool      `bson:"consumed" json:"consumed"`
	PurchasedAt time.Time `bson:"purchased_at,omitempty" json:"purchased_at,omitempty"`
	CreditedAt  time.Time `bson:"credited_at,omitempty" json:"credited_at,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at" json:"updated_at"`
}

// SignupIdentity records that an email address has signed up before, so a
// delete-and-rejoin cycle cannot farm the welcome bonus repeatedly.
//
// Only a peppered SHA-256 of the normalised address is stored — never the
// address itself. That is what lets the record legitimately outlive an
// account deletion: it retains no readable personal data, and exists solely
// for fraud prevention. Disclose it in the privacy policy.
type SignupIdentity struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	EmailHash string             `bson:"email_hash" json:"email_hash"` // unique
	// SignupCount increments on every registration with this address.
	SignupCount int       `bson:"signup_count" json:"signup_count"`
	FirstSeenAt time.Time `bson:"first_seen_at" json:"first_seen_at"`
	LastSeenAt  time.Time `bson:"last_seen_at" json:"last_seen_at"`
	// DeletedCount tracks how many of those accounts were deleted. A high
	// value next to a high SignupCount is the farming signature.
	DeletedCount int `bson:"deleted_count" json:"deleted_count"`
}
