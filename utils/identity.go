package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func signupIdentities() *mongo.Collection {
	return GetCollection(config.DBName, models.CollSignupIdentities)
}

// NormaliseEmail reduces an address to the identity it actually delivers to,
// so that two spellings of the same inbox are one identity for the purpose of
// the welcome bonus.
//
// For Gmail this means stripping dots and any +tag: Google genuinely routes
// r.k.gupta+test@gmail.com, rkgupta@gmail.com and r.k.gupta@gmail.com to one
// inbox, so treating them as three new users is a free-credit vending machine
// that costs nothing to operate. Other providers are only lowercased and
// trimmed, because dot-insensitivity is not safe to assume generally.
func NormaliseEmail(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return e
	}

	local, domain := e[:at], e[at+1:]

	if config.Stars.Identity.NormaliseGmailDots &&
		(domain == "gmail.com" || domain == "googlemail.com") {
		if plus := strings.Index(local, "+"); plus >= 0 {
			local = local[:plus]
		}
		local = strings.ReplaceAll(local, ".", "")
		domain = "gmail.com" // googlemail.com is an alias of the same inbox
	}

	return local + "@" + domain
}

// EmailIdentityHash returns the peppered SHA-256 of a normalised address.
//
// Only this value is ever stored. That is what lets the record legitimately
// outlive an account deletion: it holds no readable personal data and exists
// solely to stop the welcome bonus being farmed. The pepper means the stored
// hashes are not a lookup table of every address that ever signed up.
func EmailIdentityHash(email string) string {
	sum := sha256.Sum256([]byte(config.StarsIdentityPepper + "|" + NormaliseEmail(email)))
	return hex.EncodeToString(sum[:])
}

// RecordSignup registers an address as having signed up and reports whether
// it had signed up before.
//
// A true result is what downgrades the welcome bonus from the full grant to
// the returning grant, which is what makes deleting an account and
// re-registering pointless as a way to farm free try-ons.
//
// The upsert is atomic and returns the pre-update document, so two concurrent
// signups with the same address cannot both be told they are new.
func RecordSignup(ctx context.Context, email string) (returning bool, err error) {
	if !config.Stars.Identity.Enabled || strings.TrimSpace(email) == "" {
		return false, nil
	}

	hash := EmailIdentityHash(email)
	now := time.Now()

	var before models.SignupIdentity
	err = signupIdentities().FindOneAndUpdate(ctx,
		bson.M{"email_hash": hash},
		bson.M{
			"$inc": bson.M{"signup_count": 1},
			"$set": bson.M{"last_seen_at": now},
			"$setOnInsert": bson.M{
				"email_hash": hash, "first_seen_at": now, "deleted_count": 0,
			},
		},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.Before),
	).Decode(&before)

	if err == mongo.ErrNoDocuments {
		return false, nil // first ever signup for this address
	}
	if err != nil {
		// Fail open: a lookup failure must not block a legitimate signup.
		// The cost of being wrong is one extra welcome bonus.
		slog.Warn("signup identity lookup failed — treating as new user", "error", err)
		return false, nil
	}

	if before.SignupCount > 0 {
		slog.Info("returning signup detected",
			"signup_count", before.SignupCount+1, "deleted_count", before.DeletedCount)
		return true, nil
	}
	return false, nil
}

// RecordAccountDeletion notes that an account for this address was deleted. A
// high deleted_count next to a high signup_count is the farming signature.
func RecordAccountDeletion(ctx context.Context, email string) {
	if !config.Stars.Identity.Enabled || strings.TrimSpace(email) == "" {
		return
	}

	hash := EmailIdentityHash(email)
	now := time.Now()

	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := signupIdentities().UpdateOne(writeCtx,
		bson.M{"email_hash": hash},
		bson.M{
			"$inc": bson.M{"deleted_count": 1},
			"$set": bson.M{"last_seen_at": now},
			"$setOnInsert": bson.M{
				"email_hash": hash, "first_seen_at": now, "signup_count": 1,
			},
		},
		options.Update().SetUpsert(true))
	if err != nil {
		slog.Warn("failed to record account deletion identity", "error", err)
	}
}

// PurgeUserStarState removes a deleted user's balance. Called on account
// deletion so unspent stars do not silently survive into a re-registration.
//
// The ledger rows are deliberately kept: they are the financial record behind
// real payments, they are needed to answer a refund dispute, and they carry no
// contact details. The signup identity is kept for the same reason — both hold
// only an opaque user id.
func PurgeUserStarState(ctx context.Context, userID string) {
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := starBalances().DeleteOne(writeCtx, bson.M{"_id": userID}); err != nil {
		slog.Warn("failed to purge star balance on account deletion",
			"user_id", userID, "error", err)
		return
	}
	slog.Info("purged star balance for deleted account", "user_id", userID)
}

// StarStateSummaryForSupport renders a compact description of a user's star
// state. Used by the deletion flow's log line so a "I deleted my account and
// lost my stars" enquiry can be answered from the logs alone.
func StarStateSummaryForSupport(ctx context.Context, userID string) string {
	b, err := GetOrCreateBalance(ctx, userID)
	if err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("stars=%d free_credits=%d lifetime_purchased=%d lifetime_generations=%d",
		b.Stars, b.FreeCredits, b.LifetimePurchasedStars, b.LifetimeGenerations)
}
