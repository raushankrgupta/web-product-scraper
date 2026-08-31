package utils

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrInsufficientFunds is returned by ReserveGeneration when the user cannot
// pay for the requested generation from any source. Handlers translate it to
// a 402 carrying the shortfall, which is what makes the app open the store
// sheet with the right pack pre-selected.
var ErrInsufficientFunds = errors.New("insufficient stars")

// ErrUnknownTier is returned for a (type, quality) pair the config does not
// price. It must never be treated as free.
var ErrUnknownTier = errors.New("unknown try-on tier")

func starBalances() *mongo.Collection {
	return GetCollection(config.DBName, models.CollStarBalances)
}
func starLedger() *mongo.Collection {
	return GetCollection(config.DBName, models.CollStarLedger)
}

// ------------------------------------------------------------------ balance

// GetOrCreateBalance returns the user's balance document, creating an empty
// one on first touch. The upsert is $setOnInsert-only so calling it can never
// clobber an existing balance.
func GetOrCreateBalance(ctx context.Context, userID string) (*models.StarBalance, error) {
	now := time.Now()
	var b models.StarBalance
	err := starBalances().FindOneAndUpdate(
		ctx,
		bson.M{"_id": userID},
		bson.M{"$setOnInsert": bson.M{
			"stars":                    0,
			"free_credits":             0,
			"free_day":                 "",
			"free_day_used":            0,
			"held":                     bson.A{},
			"lifetime_purchased_stars": 0,
			"lifetime_spent_stars":     0,
			"lifetime_generations":     0,
			"welcome_granted":          false,
			"returning":                false,
			"created_at":               now,
			"updated_at":               now,
		}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&b)
	if err != nil {
		return nil, fmt.Errorf("load star balance: %w", err)
	}
	return &b, nil
}

// StarSummary is the shape the app renders: the balance pill, the quality
// selector's affordability state, and whether a free try-on is available.
type StarSummary struct {
	Stars       int `json:"stars"`
	FreeCredits int `json:"free_credits"`

	// FreeDailyRemaining is today's unused free allowance, already adjusted
	// for suppression — if the user can afford to pay, this is 0.
	FreeDailyRemaining int `json:"free_daily_remaining"`
	// FreeAvailable is the single flag the UI should branch on: is the next
	// eligible generation free?
	FreeAvailable bool `json:"free_available"`
	// FreeSuppressed explains a zero: the user holds enough stars to pay, so
	// free usage is paused (it resumes when the balance drops below
	// StarsThreshold). Lets the app say why rather than showing a bare 0.
	FreeSuppressed bool `json:"free_suppressed"`
	// StarsThreshold is the balance at or above which free usage pauses.
	StarsThreshold int `json:"stars_threshold"`

	LifetimePurchasedStars int `json:"lifetime_purchased_stars"`
	LifetimeGenerations    int `json:"lifetime_generations"`

	Date string `json:"date"`
}

// GetStarSummary computes the user-facing view of a balance.
func GetStarSummary(ctx context.Context, userID string, isGuest bool) (StarSummary, error) {
	cfg := config.Stars
	today := utcDateString()

	b, err := GetOrCreateBalance(ctx, userID)
	if err != nil {
		return StarSummary{}, err
	}

	dailyLimit := cfg.Free.DailyFreeCount
	if isGuest {
		dailyLimit = cfg.Free.GuestDailyFreeCount
	}

	usedToday := 0
	if b.FreeDay == today {
		usedToday = b.FreeDayUsed
	}
	dailyRemaining := dailyLimit - usedToday
	if dailyRemaining < 0 {
		dailyRemaining = 0
	}

	suppressed := freeSuppressed(b)
	if suppressed {
		dailyRemaining = 0
	}

	// FreeCredits reports what the user still owns, not what they can spend
	// right now. Zeroing it under suppression would look like the credits had
	// been taken away; they are only paused, and FreeAvailable already says so.
	return StarSummary{
		Stars:                  b.Stars,
		FreeCredits:            b.FreeCredits,
		FreeDailyRemaining:     dailyRemaining,
		FreeAvailable:          !suppressed && (dailyRemaining > 0 || b.FreeCredits > 0),
		FreeSuppressed:         suppressed,
		StarsThreshold:         cfg.CheapestTierStars(),
		LifetimePurchasedStars: b.LifetimePurchasedStars,
		LifetimeGenerations:    b.LifetimeGenerations,
		Date:                   today,
	}, nil
}

// freeSuppressed implements the rule "a user who can afford to generate does
// not get a free one". The threshold is the cheapest configured tier, derived
// at config load, so repricing keeps it correct automatically. Free usage
// resumes on its own once the balance falls back below it.
func freeSuppressed(b *models.StarBalance) bool {
	if !config.Stars.Free.SuppressWhenAffordable {
		return false
	}
	return b.Stars >= config.Stars.CheapestTierStars()
}

// ------------------------------------------------------------------- grants

// GrantWelcomeCredits issues the one-time signup bonus. `returning` selects
// the smaller grant for an email that has registered before.
//
// The welcome_granted flag makes this safe to call from every signup path
// (OTP verify, Google first login) without double-granting; the identity
// record is what stops the bonus being farmed by deleting and rejoining.
func GrantWelcomeCredits(ctx context.Context, userID string, returning bool) (int, error) {
	credits := config.Stars.Free.WelcomeCredits
	if returning {
		credits = config.Stars.Free.ReturningWelcomeCredits
	}
	if credits <= 0 {
		return 0, nil
	}

	now := time.Now()
	res, err := starBalances().UpdateOne(ctx,
		bson.M{"_id": userID, "welcome_granted": bson.M{"$ne": true}},
		bson.M{
			"$inc": bson.M{"free_credits": credits},
			"$set": bson.M{"welcome_granted": true, "returning": returning, "updated_at": now},
			"$setOnInsert": bson.M{
				"stars": 0, "free_day": "", "free_day_used": 0, "held": bson.A{},
				"lifetime_purchased_stars": 0, "lifetime_spent_stars": 0,
				"lifetime_generations": 0, "created_at": now,
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		// A duplicate-key here means a concurrent grant already inserted the
		// document; that is the desired outcome, not a failure.
		if mongo.IsDuplicateKeyError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("grant welcome credits: %w", err)
	}
	if res.ModifiedCount == 0 && res.UpsertedCount == 0 {
		return 0, nil // already granted
	}

	appendLedger(ctx, models.StarLedgerEntry{
		UserID: userID, Delta: credits, Reason: models.ReasonGrant,
		Source: models.FundFreeCredit,
		Note:   grantNote(returning),
	})
	slog.Info("welcome credits granted", "user_id", userID, "credits", credits, "returning", returning)
	return credits, nil
}

func grantNote(returning bool) string {
	if returning {
		return "returning signup welcome credits"
	}
	return "new signup welcome credits"
}

// ---------------------------------------------------------------- reserving

// Reservation is the outcome of a successful ReserveGeneration.
type Reservation struct {
	HoldID  string
	Source  string // daily_free | free_credit | stars
	Amount  int    // stars debited (0 for a free source)
	Quality string
	Type    string
	Key     string
	// Replayed is true when this key had already reserved — the caller is a
	// retry and must not be charged again.
	Replayed bool
}

// IsFree reports whether the generation was funded from a free source.
func (r Reservation) IsFree() bool { return r.Source != models.FundStars }

// ReserveGeneration debits the cost of one generation before it starts, and
// returns a hold that must be either committed or released.
//
// Funding is tried in a fixed order — today's free allowance (it expires at
// midnight regardless), then granted welcome credits (they never expire), then
// purchased stars (the only thing the user paid for). Each attempt is a single
// conditional update against the one balance document, so it either fully
// applies or does not apply at all; there is no window in which a user is
// charged without a hold to refund them from.
//
// idempotencyKey must be stable across client retries of the same logical
// generation. The app's try-on calls already time out at 90–150s and users do
// press the button twice; without this they would pay twice.
func ReserveGeneration(ctx context.Context, userID, tryOnType, quality, idempotencyKey string, isGuest bool) (Reservation, error) {
	cfg := config.Stars

	cost, ok := cfg.TierCost(tryOnType, quality)
	if !ok {
		return Reservation{}, fmt.Errorf("%w: %s/%s", ErrUnknownTier, tryOnType, quality)
	}

	b, err := GetOrCreateBalance(ctx, userID)
	if err != nil {
		return Reservation{}, err
	}

	// A retry of a key we already hold must return the original hold rather
	// than debiting again.
	if h, found := b.HoldByKey(idempotencyKey); found {
		return Reservation{
			HoldID: h.ID, Source: h.Source, Amount: h.Amount,
			Quality: h.Quality, Type: h.TryOnType, Key: h.Key, Replayed: true,
		}, nil
	}

	now := time.Now()
	newHold := func(source string, amount int) models.StarHold {
		return models.StarHold{
			ID: uuid.NewString(), Amount: amount, Source: source,
			Key: idempotencyKey, TryOnType: tryOnType, Quality: quality, At: now,
		}
	}

	freeEligible := cfg.FreeCovers(tryOnType, quality) && !freeSuppressed(b)

	if freeEligible {
		dailyLimit := cfg.Free.DailyFreeCount
		if isGuest {
			dailyLimit = cfg.Free.GuestDailyFreeCount
		}
		if dailyLimit > 0 {
			h := newHold(models.FundDailyFree, 0)
			if okDaily, err := reserveDailyFree(ctx, userID, dailyLimit, h); err != nil {
				return Reservation{}, err
			} else if okDaily {
				return Reservation{HoldID: h.ID, Source: h.Source, Quality: quality, Type: tryOnType, Key: idempotencyKey}, nil
			}
		}

		h := newHold(models.FundFreeCredit, 0)
		okCredit, err := reserveSimple(ctx, userID, h,
			bson.M{"free_credits": bson.M{"$gte": 1}},
			bson.M{"free_credits": -1})
		if err != nil {
			return Reservation{}, err
		}
		if okCredit {
			return Reservation{HoldID: h.ID, Source: h.Source, Quality: quality, Type: tryOnType, Key: idempotencyKey}, nil
		}
	}

	// Paid path.
	h := newHold(models.FundStars, cost)
	okStars, err := reserveSimple(ctx, userID, h,
		bson.M{"stars": bson.M{"$gte": cost}},
		bson.M{"stars": -cost})
	if err != nil {
		return Reservation{}, err
	}
	if okStars {
		return Reservation{HoldID: h.ID, Source: h.Source, Amount: cost, Quality: quality, Type: tryOnType, Key: idempotencyKey}, nil
	}

	return Reservation{}, fmt.Errorf("%w: need %d, have %d", ErrInsufficientFunds, cost, b.Stars)
}

// reserveDailyFree consumes one of today's free generations. The date
// rollover happens inside the update pipeline: if free_day is not today the
// counter resets to 1, otherwise it increments. Doing it here rather than on
// a nightly job means a dormant user costs nothing and there is no window in
// which yesterday's count still applies.
func reserveDailyFree(ctx context.Context, userID string, limit int, h models.StarHold) (bool, error) {
	today := utcDateString()

	filter := bson.M{
		"_id":      userID,
		"held.key": bson.M{"$ne": h.Key},
		"$or": bson.A{
			bson.M{"free_day": bson.M{"$ne": today}},
			bson.M{"free_day_used": bson.M{"$lt": limit}},
		},
	}

	pipeline := bson.A{
		bson.M{"$set": bson.M{
			"free_day": today,
			"free_day_used": bson.M{"$cond": bson.A{
				bson.M{"$eq": bson.A{"$free_day", today}},
				bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$free_day_used", 0}}, 1}},
				1,
			}},
			"held": bson.M{"$concatArrays": bson.A{
				bson.M{"$ifNull": bson.A{"$held", bson.A{}}},
				bson.A{holdDoc(h)},
			}},
			"updated_at": time.Now(),
		}},
	}

	res, err := starBalances().UpdateOne(ctx, filter, pipeline)
	if err != nil {
		return false, fmt.Errorf("reserve daily free: %w", err)
	}
	return res.ModifiedCount > 0, nil
}

// reserveSimple performs a conditional debit plus hold append in one atomic
// update. `guard` is merged into the filter (the affordability condition) and
// `dec` into $inc.
func reserveSimple(ctx context.Context, userID string, h models.StarHold, guard, dec bson.M) (bool, error) {
	filter := bson.M{"_id": userID, "held.key": bson.M{"$ne": h.Key}}
	for k, v := range guard {
		filter[k] = v
	}

	res, err := starBalances().UpdateOne(ctx, filter, bson.M{
		"$inc":  dec,
		"$push": bson.M{"held": holdDoc(h)},
		"$set":  bson.M{"updated_at": time.Now()},
	})
	if err != nil {
		return false, fmt.Errorf("reserve %s: %w", h.Source, err)
	}
	return res.ModifiedCount > 0, nil
}

// holdDoc renders a hold as a plain document. Written explicitly rather than
// via struct marshalling because it is also embedded in an aggregation
// pipeline, where field values are interpreted as expressions.
func holdDoc(h models.StarHold) bson.M {
	return bson.M{
		"id": h.ID, "amount": h.Amount, "source": h.Source, "key": h.Key,
		"tryon_type": h.TryOnType, "quality": h.Quality, "at": h.At,
	}
}

// ---------------------------------------------------------------- settling

// CommitReservation finalises a hold after a successful generation. The stars
// are already debited; this drops the hold and writes the audit row.
func CommitReservation(ctx context.Context, userID string, r Reservation) error {
	inc := bson.M{"lifetime_generations": 1}
	if r.Source == models.FundStars {
		inc["lifetime_spent_stars"] = r.Amount
	}

	res, err := starBalances().UpdateOne(ctx,
		bson.M{"_id": userID, "held.id": r.HoldID},
		bson.M{
			"$pull": bson.M{"held": bson.M{"id": r.HoldID}},
			"$inc":  inc,
			"$set":  bson.M{"updated_at": time.Now()},
		})
	if err != nil {
		return fmt.Errorf("commit hold: %w", err)
	}
	if res.ModifiedCount == 0 {
		// The sweeper already released it — the user was refunded for a
		// generation that actually succeeded. Rare, but worth knowing about
		// because it means hold_expiry_minutes is too tight.
		alert.Warnf("stars", "commit found no matching hold; it was likely swept", nil,
			"user_id", userID, "hold_id", r.HoldID, "source", r.Source)
		return nil
	}

	if r.Source == models.FundStars {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: userID, Delta: -r.Amount, Reason: models.ReasonSpend,
			Source: r.Source, HoldID: r.HoldID, IdempotencyKey: r.Key,
			TryOnType: r.Type, Quality: r.Quality,
		})
	}
	return nil
}

// ReleaseReservation refunds a hold after a failed generation. A user must
// never pay for an image they did not receive.
func ReleaseReservation(ctx context.Context, userID string, r Reservation) error {
	inc := bson.M{}
	filter := bson.M{"_id": userID, "held.id": r.HoldID}

	switch r.Source {
	case models.FundStars:
		inc["stars"] = r.Amount
	case models.FundFreeCredit:
		inc["free_credits"] = 1
	case models.FundDailyFree:
		// Only give the day's allowance back if it is still the same UTC day.
		// After a rollover the counter has already reset, and decrementing it
		// would hand the user a second free generation tomorrow.
		filter["free_day"] = utcDateString()
		inc["free_day_used"] = -1
	}

	update := bson.M{
		"$pull": bson.M{"held": bson.M{"id": r.HoldID}},
		"$set":  bson.M{"updated_at": time.Now()},
	}
	if len(inc) > 0 {
		update["$inc"] = inc
	}

	res, err := starBalances().UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("release hold: %w", err)
	}
	if res.ModifiedCount == 0 && r.Source == models.FundDailyFree {
		// Day rolled over mid-generation; just drop the hold.
		_, _ = starBalances().UpdateOne(ctx,
			bson.M{"_id": userID, "held.id": r.HoldID},
			bson.M{"$pull": bson.M{"held": bson.M{"id": r.HoldID}}})
	}

	if r.Source == models.FundStars && res.ModifiedCount > 0 {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: userID, Delta: r.Amount, Reason: models.ReasonRefund,
			Source: r.Source, HoldID: r.HoldID, IdempotencyKey: r.Key,
			TryOnType: r.Type, Quality: r.Quality,
			Note: "generation failed",
		})
	}
	return nil
}

// SweepExpiredHolds returns stars from generations that never reported back —
// a crashed process, a dropped connection, a handler that panicked between
// reserving and settling. Without it those stars are lost to the user with no
// image to show for them, which is the single most damaging billing bug a
// paid app can have.
func SweepExpiredHolds(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-time.Duration(config.Stars.Billing.HoldExpiryMinutes) * time.Minute)

	cur, err := starBalances().Find(ctx, bson.M{"held.at": bson.M{"$lt": cutoff}})
	if err != nil {
		return 0, fmt.Errorf("find expired holds: %w", err)
	}
	defer cur.Close(ctx)

	swept := 0
	for cur.Next(ctx) {
		var b models.StarBalance
		if err := cur.Decode(&b); err != nil {
			continue
		}
		for _, h := range b.Held {
			if h.At.After(cutoff) {
				continue
			}
			r := Reservation{HoldID: h.ID, Source: h.Source, Amount: h.Amount,
				Quality: h.Quality, Type: h.TryOnType, Key: h.Key}
			if err := ReleaseReservation(ctx, b.UserID, r); err != nil {
				slog.Warn("sweep failed to release hold", "user_id", b.UserID, "hold_id", h.ID, "error", err)
				continue
			}
			swept++
			slog.Info("swept expired star hold",
				"user_id", b.UserID, "hold_id", h.ID, "source", h.Source, "amount", h.Amount,
				"age", time.Since(h.At).Round(time.Second).String())
		}
	}
	return swept, cur.Err()
}

// StartHoldSweeper runs SweepExpiredHolds on a ticker for the life of ctx.
func StartHoldSweeper(ctx context.Context) {
	interval := time.Duration(config.Stars.Billing.HoldExpiryMinutes) * time.Minute
	if interval < time.Minute {
		interval = time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				n, err := SweepExpiredHolds(sweepCtx)
				cancel()
				if err != nil {
					slog.Warn("star hold sweep failed", "error", err)
				} else if n > 0 {
					slog.Info("star hold sweep released holds", "count", n)
				}
			}
		}
	}()
}

// ------------------------------------------------------------------- ledger

// appendLedger writes an audit row. Failures are logged and alerted but never
// propagated: the money has already moved correctly in the balance document,
// and refusing the user's generation because an audit write failed would be
// the worse outcome.
func appendLedger(ctx context.Context, e models.StarLedgerEntry) {
	e.CreatedAt = time.Now()

	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if b, err := GetOrCreateBalance(writeCtx, e.UserID); err == nil {
		e.BalanceAfter = b.Stars
	}

	if _, err := starLedger().InsertOne(writeCtx, e); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return // replayed purchase; the unique index did its job
		}
		alert.Errorf("stars", "ledger write failed — balance moved without an audit row", err,
			"user_id", e.UserID, "reason", e.Reason, "delta", fmt.Sprint(e.Delta))
	}
}

// ListLedger returns a user's most recent ledger rows, newest first. Powers
// the in-app transaction history, which is what stops "where did my stars go"
// becoming a support email.
func ListLedger(ctx context.Context, userID string, limit int) ([]models.StarLedgerEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cur, err := starLedger().Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer cur.Close(ctx)

	out := []models.StarLedgerEntry{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode ledger: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------- dev/test support

// AdjustStars adds (or removes, with a negative delta) stars and free credits
// outside the purchase flow.
//
// Exists for two reasons: the dev-only test endpoint, and manual support
// remediation when a generation failed in a way that lost someone's stars.
// Every adjustment writes a ledger row with the supplied note, so a balance
// that was changed by hand is never indistinguishable from one that was
// earned.
func AdjustStars(ctx context.Context, userID string, stars, freeCredits int, note string) (*models.StarBalance, error) {
	if _, err := GetOrCreateBalance(ctx, userID); err != nil {
		return nil, err
	}

	inc := bson.M{}
	if stars != 0 {
		inc["stars"] = stars
	}
	if freeCredits != 0 {
		inc["free_credits"] = freeCredits
	}
	if len(inc) == 0 {
		return GetOrCreateBalance(ctx, userID)
	}

	if _, err := starBalances().UpdateOne(ctx,
		bson.M{"_id": userID},
		bson.M{"$inc": inc, "$set": bson.M{"updated_at": time.Now()}},
	); err != nil {
		return nil, fmt.Errorf("adjust stars: %w", err)
	}

	if stars != 0 {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: userID, Delta: stars, Reason: models.ReasonAdjustment,
			Source: models.FundStars, Note: note,
		})
	}
	if freeCredits != 0 {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: userID, Delta: freeCredits, Reason: models.ReasonAdjustment,
			Source: models.FundFreeCredit, Note: note,
		})
	}

	slog.Info("star balance adjusted manually",
		"user_id", userID, "stars", stars, "free_credits", freeCredits, "note", note)
	return GetOrCreateBalance(ctx, userID)
}

// ResetBalance returns a user to the state of a brand-new account: no stars,
// no credits, no holds, and the welcome grant un-issued so it can fire again.
//
// Test-only. It does not touch the ledger, so the reset itself stays visible
// in the history.
func ResetBalance(ctx context.Context, userID string) error {
	now := time.Now()
	_, err := starBalances().UpdateOne(ctx,
		bson.M{"_id": userID},
		bson.M{"$set": bson.M{
			"stars": 0, "free_credits": 0, "free_day": "", "free_day_used": 0,
			"held": bson.A{}, "credited_tokens": bson.A{},
			"welcome_granted": false, "returning": false, "updated_at": now,
		}},
		options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("reset balance: %w", err)
	}
	slog.Info("star balance reset", "user_id", userID)
	return nil
}

// ForgetSignupIdentity deletes the stored hash for an email address, so the
// next signup with it counts as brand new again.
//
// Test-only, and the single thing that makes the returning-user rule testable
// more than once: without it, the first delete-and-rejoin test marks your own
// address as returning permanently, and you can never exercise the full
// welcome grant again.
func ForgetSignupIdentity(ctx context.Context, email string) (bool, error) {
	res, err := signupIdentities().DeleteOne(ctx, bson.M{"email_hash": EmailIdentityHash(email)})
	if err != nil {
		return false, fmt.Errorf("forget signup identity: %w", err)
	}
	return res.DeletedCount > 0, nil
}

// LookupSignupIdentity returns the stored identity record for an address, for
// the dev inspect endpoint.
func LookupSignupIdentity(ctx context.Context, email string) (*models.SignupIdentity, bool) {
	var id models.SignupIdentity
	if err := signupIdentities().FindOne(ctx,
		bson.M{"email_hash": EmailIdentityHash(email)}).Decode(&id); err != nil {
		return nil, false
	}
	return &id, true
}
