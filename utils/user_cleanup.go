package utils

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// purgeablePrefixes is the allow-list of S3 key prefixes that belong to a
// single user and may be deleted when that user's account is.
//
// The list is deliberately narrow. Wardrobe items hold keys under the scrape
// folders, and those objects are *shared* — the same retailer image backs
// every user who saved that product — so deleting them on one account's
// closure would blank out other people's wardrobes. Filtering against this
// list, rather than deleting whatever key a document happens to name, is what
// makes the purge safe to run.
var purgeablePrefixes = []string{
	"person_images/",
	"generated_images/",
	"product_uploads/",
}

// isPurgeableKey reports whether a stored value is an S3 object key this user
// owns outright. Absolute URLs are external references, never our objects.
func isPurgeableKey(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return false
	}
	for _, p := range purgeablePrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

// PurgeUserData erases a deleted account's owned content: the S3 objects are
// deleted outright, and the documents that referenced them are soft-deleted so
// the audit trail survives.
//
// It is written to be re-runnable. Every step is idempotent, so a partial
// failure can be retried by calling it again with the same user id.
func PurgeUserData(ctx context.Context, userIDStr string) error {
	now := time.Now()
	slog.Info("purging user data for deleted account", "user_id", userIDStr)

	var keys []string

	// Persons. The collection is "person", singular — "persons" silently
	// matches nothing, which is how an earlier version of this purge came to
	// be a no-op. user_id is stored as an ObjectID here, unlike the string
	// used by wardrobe and tryons.
	if objID, err := primitive.ObjectIDFromHex(userIDStr); err == nil {
		persons := GetCollection(config.DBName, "person")
		cur, err := persons.Find(ctx, bson.M{"user_id": objID})
		if err != nil {
			slog.Warn("failed to read persons for purge", "user_id", userIDStr, "error", err)
		} else {
			var docs []models.Person
			if err := cur.All(ctx, &docs); err != nil {
				slog.Warn("failed to decode persons for purge", "user_id", userIDStr, "error", err)
			}
			for _, p := range docs {
				for _, k := range p.ImagePaths {
					if isPurgeableKey(k) {
						keys = append(keys, k)
					}
				}
			}
		}

		if _, err := persons.UpdateMany(ctx,
			bson.M{"user_id": objID},
			bson.M{"$set": bson.M{"is_deleted": true, "deleted_at": now}},
		); err != nil {
			slog.Warn("failed to soft delete persons", "user_id", userIDStr, "error", err)
		}
	}

	// Try-ons. The generated image is ours; the person and product images are
	// the uploads that fed it, and are equally user-owned.
	tryons := GetCollection(config.DBName, "tryons")
	if cur, err := tryons.Find(ctx, bson.M{"user_id": userIDStr}); err != nil {
		slog.Warn("failed to read tryons for purge", "user_id", userIDStr, "error", err)
	} else {
		var docs []models.TryOn
		if err := cur.All(ctx, &docs); err != nil {
			slog.Warn("failed to decode tryons for purge", "user_id", userIDStr, "error", err)
		}
		for _, t := range docs {
			for _, k := range []string{t.GeneratedImageURL, t.PersonImageURL, t.ProductImageURL} {
				if isPurgeableKey(k) {
					keys = append(keys, k)
				}
			}
		}
	}

	if _, err := tryons.UpdateMany(ctx,
		bson.M{"user_id": userIDStr},
		bson.M{"$set": bson.M{"is_deleted": true, "deleted_at": now}},
	); err != nil {
		slog.Warn("failed to soft delete tryons", "user_id", userIDStr, "error", err)
	}

	// Wardrobe. Nothing to delete from S3 — see purgeablePrefixes — but the
	// rows themselves go. A hard delete, because the wardrobe carries no
	// audit value and every read path would otherwise have to learn about a
	// flag it does not currently check.
	if _, err := GetCollection(config.DBName, "wardrobe").DeleteMany(ctx,
		bson.M{"user_id": userIDStr},
	); err != nil {
		slog.Warn("failed to delete wardrobe items", "user_id", userIDStr, "error", err)
	}

	if len(keys) == 0 {
		return nil
	}

	deleted, err := DeleteObjectsFromS3(ctx, dedupeStrings(keys))
	if err != nil {
		// Worth an alert rather than a warning: this is the step the privacy
		// policy and the Play Data Safety declaration actually promise, and a
		// silent failure here is a compliance gap nobody would notice.
		slog.Error("failed to purge S3 objects for deleted account",
			"user_id", userIDStr, "deleted", deleted, "total", len(keys), "error", err)
		return err
	}

	slog.Info("purged user data", "user_id", userIDStr, "s3_objects_deleted", deleted)
	return nil
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
