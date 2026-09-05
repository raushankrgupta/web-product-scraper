package utils

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// failureWriteTimeout bounds the post-mortem write. The request has already
// failed by the time we get here, so a few extra milliseconds cost nothing —
// but a stalled Mongo must not turn a 422 into a hung connection.
const failureWriteTimeout = 5 * time.Second

// maxRawErrorLen truncates the stored upstream message. Gemini's transport
// errors are occasionally enormous (a full HTML error page from a proxy), and
// a 200 KB string per failed try-on is a slow way to fill a collection.
const maxRawErrorLen = 4000

// FailureRetention is how long a post-mortem is kept, enforced by a TTL index
// on expires_at (see EnsureIndexes).
//
// 90 days covers the investigation happening now plus a quarter of trend, which
// is what these rows are actually read for. It also matters that the `gate`
// stage records every duplicate tap and every out-of-stars bounce: that is by
// far the highest-volume bucket, and without an expiry it would be the reason
// somebody has to think about this collection's size later.
const FailureRetention = 90 * 24 * time.Hour

func truncateError(s string) string {
	if len(s) > maxRawErrorLen {
		return s[:maxRawErrorLen] + "…(truncated)"
	}
	return s
}

// RecordTryOnFailure persists a failed generation for manual investigation.
//
// Deliberately synchronous and rooted at context.Background(): the caller's
// request context is frequently already cancelled when a generation fails
// (the client gave up during those 20-odd seconds), and a write inheriting it
// would be dropped precisely in the cases most worth keeping. It never
// returns an error — a failure to record a failure must not change what the
// user is told, so the fallback is a log line.
//
// Nothing reads this collection on a user-facing path. See
// models.CollTryOnFailures for why it is a separate collection.
func RecordTryOnFailure(f models.TryOnFailure) {
	// GetCollection log.Fatal's on a nil client, and this function now runs on
	// paths as ordinary as a double-tap (stage "gate"). Recording a failure
	// must never be able to take the process down — least of all during the
	// database outage that would be causing the failures in the first place.
	if !MongoReady() {
		slog.Warn("skipping try-on failure record: mongo not ready",
			"type", f.Type, "stage", f.Stage, "reason", f.Reason)
		return
	}

	f.ID = primitive.NewObjectID()
	f.CreatedAt = time.Now()
	f.ExpiresAt = f.CreatedAt.Add(FailureRetention)
	f.RawError = truncateError(f.RawError)
	for i := range f.Attempts {
		f.Attempts[i].RawError = truncateError(f.Attempts[i].RawError)
	}

	ctx, cancel := context.WithTimeout(context.Background(), failureWriteTimeout)
	defer cancel()

	if _, err := GetCollection(config.DBName, models.CollTryOnFailures).InsertOne(ctx, f); err != nil {
		// Logged at Warn, not Error: this is diagnostics about diagnostics.
		// Alerting here would page someone about a lost post-mortem while the
		// actual failure has already alerted on its own.
		slog.Warn("failed to record try-on failure",
			"user_id", f.UserID, "type", f.Type, "reason", f.Reason, "error", err.Error())
		return
	}
	slog.Info("try-on failure recorded",
		"user_id", f.UserID, "type", f.Type, "stage", f.Stage,
		"reason", f.Reason, "finish_reason", f.FinishReason, "model", f.Model)
}

// KeysFromPresigned recovers the S3 object keys behind a set of presigned
// URLs so a failure record stays useful after the signatures expire.
//
// A URL that is not one of ours (a retailer CDN link kept verbatim by the
// scraper — see ScrapeHandler's `unhosted` counter) is stored as-is: it is
// still the most faithful description of what the model was actually sent.
func KeysFromPresigned(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if u == "" {
			continue
		}
		if i := strings.Index(u, "amazonaws.com/"); i >= 0 {
			key := u[i+len("amazonaws.com/"):]
			out = append(out, strings.SplitN(key, "?", 2)[0])
			continue
		}
		out = append(out, strings.SplitN(u, "?", 2)[0])
	}
	return out
}
