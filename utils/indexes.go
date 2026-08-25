package utils

import (
	"context"
	"log/slog"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureIndexes creates the compound indexes every hot query path relies on.
//
// These are cheap at today's traffic and mandatory at 100×: without them each
// gallery/wardrobe/persons read is a full collection scan, and the quota
// lookup — which runs on the critical path of every single try-on — is too.
//
// Index creation is idempotent; an existing index with the same keys is a
// no-op. Failures are logged and never fatal: a missing index makes the app
// slow, but refusing to boot over one makes it unavailable.
func EnsureIndexes(ctx context.Context, dbName string) {
	type spec struct {
		collection string
		model      mongo.IndexModel
	}

	specs := []spec{
		// Every /persons read filters on owner + not-deleted.
		{"person", mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "is_deleted", Value: 1}}}},
		// Wardrobe listing, newest first.
		{"wardrobe", mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}}},
		// Gallery listing, newest first.
		{"tryons", mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}}},
		// Quota lookup runs on every try-on request, before the handler.
		{"tryon_quota", mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "date", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},
		// Login/signup/Google-login all look users up by email. Unique is the
		// structural guarantee behind the deleted-account tombstone rename:
		// two live rows must never share an address.
		{"users", mongo.IndexModel{
			Keys:    bson.D{{Key: "email", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},
		// Powers the "which domains can't we scrape" digest.
		{"products", mongo.IndexModel{Keys: bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}}}},
	}

	for _, s := range specs {
		name, err := GetCollection(dbName, s.collection).Indexes().CreateOne(ctx, s.model)
		if err != nil {
			// A unique index fails to build if the collection already holds
			// duplicates. That is worth knowing about, but it is not a
			// reason to refuse to serve traffic.
			slog.Warn("index creation failed",
				"collection", s.collection, "error", err.Error())
			continue
		}
		slog.Debug("index ensured", "collection", s.collection, "index", name)
	}
}
