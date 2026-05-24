package utils

import (
	"context"
	"fmt"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// utcDateString returns today's date in UTC as YYYY-MM-DD. Using UTC (vs
// server local) keeps the quota window consistent across deploys / regions.
func utcDateString() string {
	return time.Now().UTC().Format("2006-01-02")
}

// QuotaStatus describes a user's try-on usage for the current UTC day.
type QuotaStatus struct {
	Plan      string `json:"plan"`
	Limit     int    `json:"limit"`     // 0 == unlimited
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"` // -1 == unlimited
	Date      string `json:"date"`
}

// GetTryOnQuotaStatus returns the current day's usage for `userKey`. The key
// can be a real user _id hex or a synthetic "guest:<device>" identifier — both
// share the same collection.
func GetTryOnQuotaStatus(ctx context.Context, userKey, plan string) (QuotaStatus, error) {
	limit := models.DailyLimitForPlan(plan)
	date := utcDateString()

	coll := GetCollection(config.DBName, "tryon_quota")

	var q models.TryOnQuota
	err := coll.FindOne(ctx, bson.M{"user_id": userKey, "date": date}).Decode(&q)
	if err != nil {
		// No doc for today yet → usage is zero. Don't surface mongo.ErrNoDocuments
		// as a real error — that's the normal first-of-day case.
		q.Count = 0
	}

	remaining := -1
	if limit > 0 {
		remaining = limit - q.Count
		if remaining < 0 {
			remaining = 0
		}
	}

	return QuotaStatus{
		Plan:      plan,
		Limit:     limit,
		Used:      q.Count,
		Remaining: remaining,
		Date:      date,
	}, nil
}

// IncrementTryOnQuota atomically increments today's counter for `userKey`,
// creating the document if missing. Should only be called after a successful
// try-on so we don't bill the user for failed generations.
func IncrementTryOnQuota(ctx context.Context, userKey string) error {
	coll := GetCollection(config.DBName, "tryon_quota")
	date := utcDateString()

	_, err := coll.UpdateOne(
		ctx,
		bson.M{"user_id": userKey, "date": date},
		bson.M{
			"$inc": bson.M{"count": 1},
			"$set": bson.M{"updated_at": time.Now()},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("increment quota: %w", err)
	}
	return nil
}
