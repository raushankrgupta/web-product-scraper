package utils

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/api/androidpublisher/v3"
)

// creditedTokenRing bounds the per-user idempotency guard. Fifty is far more
// than the number of purchases a single user can have in flight, and keeps
// the balance document small.
const creditedTokenRing = 50

// ErrUnknownProduct is returned for a product id that is not in the star
// config. An unrecognised product is never credited with a guess.
var ErrUnknownProduct = errors.New("unknown product id")

// ErrTokenBelongsToAnotherUser guards against a purchase token being replayed
// from one account into another.
var ErrTokenBelongsToAnotherUser = errors.New("purchase token already claimed by another account")

func starPurchases() *mongo.Collection {
	return GetCollection(config.DBName, models.CollStarPurchases)
}

// PurchaseResult describes the outcome of submitting a purchase token.
type PurchaseResult struct {
	State     string `json:"state"`     // credited | pending | cancelled | rejected
	Stars     int    `json:"stars"`     // stars this purchase is worth
	Credited  bool   `json:"credited"`  // whether the balance moved on this call
	Duplicate bool   `json:"duplicate"` // token had already been credited
	Message   string `json:"message"`
}

// SubmitPurchase verifies a Play purchase token with Google and, if it is a
// completed purchase, credits the stars exactly once.
//
// The client's claim is never trusted: it supplies a token, and everything
// that matters — that the purchase exists, that it is for this package, that
// it completed rather than being abandoned — comes from Google.
func SubmitPurchase(ctx context.Context, userID, productID, token string) (PurchaseResult, error) {
	pack, ok := config.Stars.PackByProductID(productID)
	if !ok {
		return PurchaseResult{State: models.PurchaseRejected}, fmt.Errorf("%w: %s", ErrUnknownProduct, productID)
	}

	// A token that another account already claimed is an attempt to share one
	// purchase across accounts. Refuse it and leave the original credit alone.
	var existing models.StarPurchase
	err := starPurchases().FindOne(ctx, bson.M{"purchase_token": token}).Decode(&existing)
	switch {
	case err == nil && existing.UserID != userID:
		alert.Warnf("billing", "purchase token replayed by a different account", nil,
			"token_owner", existing.UserID, "claimed_by", userID, "product", productID)
		return PurchaseResult{State: models.PurchaseRejected}, ErrTokenBelongsToAnotherUser
	case err != nil && err != mongo.ErrNoDocuments:
		return PurchaseResult{}, fmt.Errorf("look up purchase: %w", err)
	}

	purchase, err := VerifyPurchase(ctx, productID, token)
	if err != nil {
		if errors.Is(err, ErrPurchaseNotFound) {
			recordPurchase(ctx, userID, productID, token, pack.Stars, models.PurchaseRejected,
				"google does not recognise this token", nil)
			return PurchaseResult{State: models.PurchaseRejected}, err
		}
		return PurchaseResult{}, err
	}

	switch purchase.PurchaseState {
	case PlayStatePending:
		// UPI and netbanking settle asynchronously. Record it and wait for
		// Google to confirm — crediting now would hand stars to anyone who
		// opens the payment sheet and abandons it.
		recordPurchase(ctx, userID, productID, token, pack.Stars, models.PurchasePending,
			"awaiting payment confirmation", purchase)
		return PurchaseResult{
			State: models.PurchasePending, Stars: pack.Stars,
			Message: "Payment is still processing. Your stars will appear as soon as it completes.",
		}, nil

	case PlayStateCancelled:
		recordPurchase(ctx, userID, productID, token, pack.Stars, models.PurchaseCancelled,
			"cancelled at Google", purchase)
		return PurchaseResult{State: models.PurchaseCancelled, Stars: pack.Stars,
			Message: "That payment was cancelled."}, nil
	}

	// Purchased. Credit exactly once.
	credited, err := creditStarsOnce(ctx, userID, pack.Stars, token)
	if err != nil {
		return PurchaseResult{}, err
	}

	recordPurchase(ctx, userID, productID, token, pack.Stars, models.PurchaseCredited, "", purchase)

	if credited {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: userID, Delta: pack.Stars, Reason: models.ReasonPurchase,
			Source: models.FundStars, PurchaseToken: token, ProductID: productID,
			OrderID: purchase.OrderId,
		})
		slog.Info("stars credited", "user_id", userID, "product", productID, "stars", pack.Stars)
	}

	// Consume so the product can be bought again, and so Play does not
	// auto-refund it after three days. A failure here is alerted and retried
	// by the reconciler rather than failing the user's request — they have
	// paid and been credited; the cleanup is our problem, not theirs.
	if err := ConsumePurchase(ctx, productID, token); err != nil {
		alert.Errorf("billing", "failed to consume a credited purchase", err,
			"user_id", userID, "product", productID)
	} else {
		markConsumed(ctx, token)
	}

	return PurchaseResult{
		State: models.PurchaseCredited, Stars: pack.Stars,
		Credited: credited, Duplicate: !credited,
	}, nil
}

// creditStarsOnce adds stars in a single atomic update that cannot apply
// twice for the same token. The filter requires the token to be absent from
// the user's bounded ring of recently credited tokens; $slice keeps that ring
// from growing. Returns false when the token had already been credited.
func creditStarsOnce(ctx context.Context, userID string, stars int, token string) (bool, error) {
	now := time.Now()

	res, err := starBalances().UpdateOne(ctx,
		bson.M{"_id": userID, "credited_tokens": bson.M{"$ne": token}},
		bson.M{
			"$inc": bson.M{"stars": stars, "lifetime_purchased_stars": stars},
			"$push": bson.M{"credited_tokens": bson.M{
				"$each":  bson.A{token},
				"$slice": -creditedTokenRing,
			}},
			"$set": bson.M{"updated_at": now},
			"$setOnInsert": bson.M{
				"free_credits": 0, "free_day": "", "free_day_used": 0,
				"held": bson.A{}, "lifetime_spent_stars": 0,
				"lifetime_generations": 0, "welcome_granted": false,
				"returning": false, "created_at": now,
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("credit stars: %w", err)
	}
	return res.ModifiedCount > 0 || res.UpsertedCount > 0, nil
}

// recordPurchase upserts the audit row for a token. Every token we have ever
// seen is recorded, including the ones we refused, because "why did my
// payment not give me stars" is otherwise unanswerable.
func recordPurchase(ctx context.Context, userID, productID, token string, stars int,
	state, reason string, p *androidpublisher.ProductPurchase) {

	now := time.Now()
	set := bson.M{
		"user_id": userID, "product_id": productID, "stars": stars,
		"state": state, "reason": reason, "updated_at": now,
	}
	if p != nil {
		set["order_id"] = p.OrderId
		set["google_purchase_state"] = int(p.PurchaseState)
		set["google_ack_state"] = int(p.AcknowledgementState)
		if p.PurchaseTimeMillis > 0 {
			set["purchased_at"] = time.UnixMilli(p.PurchaseTimeMillis)
		}
	}
	if state == models.PurchaseCredited {
		set["credited_at"] = now
	}

	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := starPurchases().UpdateOne(writeCtx,
		bson.M{"purchase_token": token},
		bson.M{"$set": set, "$setOnInsert": bson.M{
			"purchase_token": token, "consumed": false, "created_at": now,
		}},
		options.Update().SetUpsert(true))
	if err != nil {
		alert.Errorf("billing", "failed to record purchase", err,
			"user_id", userID, "product", productID, "state", state)
	}
}

func markConsumed(ctx context.Context, token string) {
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = starPurchases().UpdateOne(writeCtx,
		bson.M{"purchase_token": token},
		bson.M{"$set": bson.M{"consumed": true, "updated_at": time.Now()}})
}

// ------------------------------------------------------------------ refunds

// RevokePurchase debits stars for a refunded or charged-back purchase.
//
// The balance is allowed to go negative. Clamping it to zero would let a user
// buy stars, spend them, refund the payment, and keep the images — repeatedly.
// A negative balance simply means the next purchase pays off the debt first.
func RevokePurchase(ctx context.Context, token, reason string) error {
	var p models.StarPurchase
	if err := starPurchases().FindOne(ctx, bson.M{"purchase_token": token}).Decode(&p); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil // never credited; nothing to claw back
		}
		return fmt.Errorf("look up purchase for revoke: %w", err)
	}
	if p.State == models.PurchaseRefunded {
		return nil // already handled
	}
	if p.State != models.PurchaseCredited {
		// Recorded but never credited — just mark it and move on.
		_, _ = starPurchases().UpdateOne(ctx, bson.M{"purchase_token": token},
			bson.M{"$set": bson.M{"state": models.PurchaseRefunded, "reason": reason, "updated_at": time.Now()}})
		return nil
	}

	res, err := starBalances().UpdateOne(ctx,
		bson.M{"_id": p.UserID},
		bson.M{
			"$inc": bson.M{"stars": -p.Stars},
			"$set": bson.M{"updated_at": time.Now()},
		})
	if err != nil {
		return fmt.Errorf("revoke stars: %w", err)
	}

	_, _ = starPurchases().UpdateOne(ctx, bson.M{"purchase_token": token},
		bson.M{"$set": bson.M{"state": models.PurchaseRefunded, "reason": reason, "updated_at": time.Now()}})

	if res.ModifiedCount > 0 {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: p.UserID, Delta: -p.Stars, Reason: models.ReasonChargeback,
			Source: models.FundStars, PurchaseToken: token, ProductID: p.ProductID,
			Note: reason,
		})
		alert.Warnf("billing", "purchase revoked", nil,
			"user_id", p.UserID, "product", p.ProductID,
			"stars", fmt.Sprint(p.Stars), "reason", reason)
	}
	return nil
}

// ------------------------------------------------------------- reconciliation

// ReconcilePurchases is the safety net behind the live purchase path. It does
// two things the request path cannot:
//
//  1. Re-checks purchases still marked pending. Indian UPI and netbanking
//     settle minutes to hours after the app has moved on, and nothing else
//     will ever credit those stars.
//  2. Pulls Google's voided-purchase list and claws back refunds and
//     chargebacks.
//
// It also retries consumption for credited purchases that were never
// consumed, which is what stops Play auto-refunding them after three days.
func ReconcilePurchases(ctx context.Context, voidedSince time.Time) error {
	if !PlayBillingConfigured() {
		return nil
	}

	// 1. Pending purchases.
	cutoff := time.Now().Add(-72 * time.Hour)
	cur, err := starPurchases().Find(ctx, bson.M{
		"state":      models.PurchasePending,
		"created_at": bson.M{"$gt": cutoff},
	})
	if err != nil {
		return fmt.Errorf("find pending purchases: %w", err)
	}
	var pending []models.StarPurchase
	if err := cur.All(ctx, &pending); err != nil {
		return fmt.Errorf("decode pending purchases: %w", err)
	}
	for _, p := range pending {
		if _, err := SubmitPurchase(ctx, p.UserID, p.ProductID, p.PurchaseToken); err != nil {
			slog.Warn("reconcile: pending purchase re-check failed",
				"user_id", p.UserID, "product", p.ProductID, "error", err)
		}
	}

	// 2. Credited but unconsumed — Play auto-refunds these after three days.
	cur, err = starPurchases().Find(ctx, bson.M{
		"state": models.PurchaseCredited, "consumed": false,
	})
	if err == nil {
		var stale []models.StarPurchase
		if cur.All(ctx, &stale) == nil {
			for _, p := range stale {
				if err := ConsumePurchase(ctx, p.ProductID, p.PurchaseToken); err == nil {
					markConsumed(ctx, p.PurchaseToken)
				}
			}
		}
	}

	// 3. Refunds and chargebacks.
	voided, err := ListVoidedPurchases(ctx, voidedSince)
	if err != nil {
		return fmt.Errorf("reconcile voided: %w", err)
	}
	for _, v := range voided {
		if err := RevokePurchase(ctx, v.PurchaseToken, "voided at Google"); err != nil {
			slog.Warn("reconcile: revoke failed", "error", err)
		}
	}

	if len(pending) > 0 || len(voided) > 0 {
		slog.Info("purchase reconciliation done", "pending_rechecked", len(pending), "voided", len(voided))
	}
	return nil
}

// StartPurchaseReconciler runs ReconcilePurchases hourly for the life of ctx.
func StartPurchaseReconciler(ctx context.Context) {
	if !PlayBillingConfigured() {
		slog.Info("play billing not configured — purchase reconciler disabled")
		return
	}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		// Look back a day on each pass so a missed run self-heals.
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				if err := ReconcilePurchases(runCtx, time.Now().Add(-24*time.Hour)); err != nil {
					slog.Warn("purchase reconciliation failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

// UserForPurchaseToken resolves a purchase token back to the account that
// submitted it.
//
// A Real-time Developer Notification carries only the token, so this record —
// written when the app first submitted the purchase, including when it came
// back PENDING — is the only link between a settled payment and whose balance
// to credit. Without it a UPI purchase that completes after the app has closed
// could never be attributed to anyone.
func UserForPurchaseToken(ctx context.Context, token string) (string, bool) {
	var p models.StarPurchase
	err := starPurchases().FindOne(ctx, bson.M{"purchase_token": token}).Decode(&p)
	if err != nil || p.UserID == "" {
		return "", false
	}
	return p.UserID, true
}
