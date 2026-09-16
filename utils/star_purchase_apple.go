package utils

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// iOS star purchases. The shape deliberately mirrors SubmitPurchase for Play —
// same audit rows, same idempotency ring, same ledger — with the differences
// that matter:
//
//   - Apple is asked about a transaction id, not a token.
//   - There is no pending state and no server-side consume. The app finishes
//     the transaction once this returns credited; until it does, StoreKit
//     keeps delivering it on every launch, which is the retry.
//   - Refunds arrive as signed notifications rather than a polled list.

// ErrApplePurchaseInvalid is a genuine Apple transaction that is not a star
// purchase from this app — a different bundle, a subscription, a product we
// do not sell.
var ErrApplePurchaseInvalid = errors.New("apple transaction is not a valid star purchase")

// ApplePurchaseKey is the purchase_token value for an App Store transaction.
func ApplePurchaseKey(transactionID string) string {
	return models.ApplePurchaseKeyPrefix + strings.TrimSpace(transactionID)
}

// ApplePurchaseInput is what the app sends. Either field identifies the
// transaction; the id is preferred, and the signed transaction is accepted so
// a client that could not read the id still works. Neither is trusted — the
// transaction is always fetched from Apple.
type ApplePurchaseInput struct {
	ProductID         string
	TransactionID     string
	SignedTransaction string
}

// SubmitApplePurchase verifies an App Store transaction and credits it once.
//
// userID is the caller. When the transaction's appAccountToken names a
// different account, the stars go to that account — the one that paid — and
// the result says so; the caller's balance is never credited with someone
// else's purchase.
func SubmitApplePurchase(ctx context.Context, userID string, in ApplePurchaseInput) (PurchaseResult, error) {
	txID := strings.TrimSpace(in.TransactionID)
	if txID == "" && in.SignedTransaction != "" {
		var claimed AppleTransaction
		if err := VerifyAppleJWS(in.SignedTransaction, &claimed); err != nil {
			return PurchaseResult{State: models.PurchaseRejected}, fmt.Errorf("%w: %v", ErrPurchaseNotFound, err)
		}
		txID = claimed.TransactionID
	}
	if txID == "" {
		return PurchaseResult{State: models.PurchaseRejected}, fmt.Errorf("%w: no transaction id", ErrPurchaseNotFound)
	}
	key := ApplePurchaseKey(txID)

	var existing models.StarPurchase
	err := starPurchases().FindOne(ctx, bson.M{"purchase_token": key}).Decode(&existing)
	switch {
	case err == nil && existing.State == models.PurchaseCredited:
		pack, _ := config.Stars.PackByProductID(existing.ProductID)
		return PurchaseResult{
			State: models.PurchaseCredited, Stars: pack.Stars, Duplicate: true,
			OtherAccount: existing.UserID != userID,
		}, nil
	case err != nil && err != mongo.ErrNoDocuments:
		return PurchaseResult{}, fmt.Errorf("look up purchase: %w", err)
	}

	txn, err := GetAppleTransaction(ctx, txID)
	if err != nil {
		if errors.Is(err, ErrAppleTransactionNotFound) {
			recordApplePurchase(userID, key, in.ProductID, 0, models.PurchaseRejected,
				"apple does not recognise this transaction", nil)
			return PurchaseResult{State: models.PurchaseRejected}, fmt.Errorf("%w: %v", ErrPurchaseNotFound, err)
		}
		// Credentials, network, Apple down: none of it is the user's doing,
		// and none of it may be recorded as a rejection — the app finishes
		// transactions it is told were rejected.
		return PurchaseResult{}, err
	}

	// Everything below is decided from Apple's copy, not the request.
	if txn.BundleID != config.AppleBundleID {
		alert.Warnf("billing", "apple transaction for a different bundle", nil,
			"bundle", txn.BundleID, "user_id", userID)
		recordApplePurchase(userID, key, txn.ProductID, 0, models.PurchaseRejected, "wrong bundle id", txn)
		return PurchaseResult{State: models.PurchaseRejected}, ErrApplePurchaseInvalid
	}
	if txn.Type != "" && txn.Type != "Consumable" {
		recordApplePurchase(userID, key, txn.ProductID, 0, models.PurchaseRejected, "not a consumable: "+txn.Type, txn)
		return PurchaseResult{State: models.PurchaseRejected}, ErrApplePurchaseInvalid
	}
	pack, ok := config.Stars.PackByProductID(txn.ProductID)
	if !ok {
		return PurchaseResult{State: models.PurchaseRejected}, fmt.Errorf("%w: %s", ErrUnknownProduct, txn.ProductID)
	}
	if txn.Environment == AppleEnvSandbox && !config.AppStoreAllowSandbox {
		recordApplePurchase(userID, key, txn.ProductID, pack.Stars, models.PurchaseRejected, "sandbox not accepted", txn)
		return PurchaseResult{State: models.PurchaseRejected}, ErrApplePurchaseInvalid
	}

	// Credit the account that paid. The token is stamped by the app at
	// purchase time, so it survives a sign-out between paying and crediting.
	creditUser := userID
	if owner := UserIDFromAppAccountToken(txn.AppAccountToken); owner != "" && owner != userID {
		if userExists(ctx, owner) {
			creditUser = owner
		} else {
			slog.Warn("apple purchase names an account that does not exist; crediting the caller",
				"caller", userID, "token_user", owner)
		}
	}
	// A rejected row is not a claim — Apple may simply not have known the id
	// yet — so only a live record by another account blocks this.
	if existing.UserID != "" && existing.UserID != creditUser && existing.State != models.PurchaseRejected {
		alert.Warnf("billing", "apple transaction replayed by a different account", nil,
			"owner", existing.UserID, "claimed_by", userID)
		return PurchaseResult{State: models.PurchaseRejected}, ErrTokenBelongsToAnotherUser
	}

	if txn.RevocationDate > 0 {
		recordApplePurchase(creditUser, key, txn.ProductID, pack.Stars, models.PurchaseRefunded,
			"refunded before it was credited", txn)
		return PurchaseResult{
			State: models.PurchaseCancelled, Stars: pack.Stars,
			Message: "This purchase was refunded.",
		}, nil
	}

	credited, err := creditStarsOnce(ctx, creditUser, pack.Stars, key)
	if err != nil {
		return PurchaseResult{}, err
	}
	recordApplePurchase(creditUser, key, txn.ProductID, pack.Stars, models.PurchaseCredited, "", txn)

	if credited {
		appendLedger(ctx, models.StarLedgerEntry{
			UserID: creditUser, Delta: pack.Stars, Reason: models.ReasonPurchase,
			Source: models.FundStars, PurchaseToken: key, ProductID: txn.ProductID,
			OrderID: txn.TransactionID, Store: models.StoreApple,
		})
		slog.Info("stars credited", "store", models.StoreApple, "user_id", creditUser,
			"product", txn.ProductID, "stars", pack.Stars, "environment", txn.Environment)
		if txn.Environment == AppleEnvSandbox && config.IsProd() {
			slog.Warn("sandbox apple purchase credited on production",
				"user_id", creditUser, "product", txn.ProductID)
		}
	}

	return PurchaseResult{
		State: models.PurchaseCredited, Stars: pack.Stars,
		Credited: credited && creditUser == userID, Duplicate: !credited,
		OtherAccount: creditUser != userID,
	}, nil
}

// UserForAppleTransaction finds whose balance a notified transaction belongs
// to: the account that submitted it, or failing that the account named by its
// appAccountToken.
func UserForAppleTransaction(ctx context.Context, txn *AppleTransaction) (string, bool) {
	if txn == nil {
		return "", false
	}
	if id, ok := UserForPurchaseToken(ctx, ApplePurchaseKey(txn.TransactionID)); ok {
		return id, true
	}
	if owner := UserIDFromAppAccountToken(txn.AppAccountToken); owner != "" && userExists(ctx, owner) {
		return owner, true
	}
	return "", false
}

func userExists(ctx context.Context, userID string) bool {
	oid, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return GetCollection(config.DBName, "users").
		FindOne(lookupCtx, bson.M{"_id": oid, "status": bson.M{"$ne": "deleted"}}).Err() == nil
}

func recordApplePurchase(userID, key, productID string, stars int, state, reason string, txn *AppleTransaction) {
	now := time.Now()
	set := bson.M{
		"user_id": userID, "product_id": productID, "stars": stars,
		"state": state, "reason": reason, "updated_at": now,
		"store": models.StoreApple,
	}
	if txn != nil {
		set["transaction_id"] = txn.TransactionID
		set["original_transaction_id"] = txn.OriginalTransactionID
		set["environment"] = txn.Environment
		set["storefront"] = txn.Storefront
		set["order_id"] = txn.TransactionID
		if txn.AppAccountToken != "" {
			set["app_account_token"] = txn.AppAccountToken
		}
		if txn.PurchaseDate > 0 {
			set["purchased_at"] = time.UnixMilli(txn.PurchaseDate)
		}
	}
	if state == models.PurchaseCredited {
		set["credited_at"] = now
	}
	upsertPurchaseRecord(userID, productID, key, state, set)
}
