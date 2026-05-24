package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TryOnQuota tracks per-user (or per-guest-device) try-on usage for a single
// UTC calendar day. One document per (user_id, date) pair. Increment via
// $inc; when the date rolls over we just insert a new document.
type TryOnQuota struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    string             `bson:"user_id" json:"user_id"` // user._id hex, or "guest:<device_id>"
	Date      string             `bson:"date"    json:"date"`    // YYYY-MM-DD in UTC
	Count     int                `bson:"count"   json:"count"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
}

// DailyLimitForPlan returns how many try-ons a given plan is allowed per UTC
// day. A return value of 0 means "unlimited" (Pro tier or B2B).
func DailyLimitForPlan(plan string) int {
	switch plan {
	case PlanPro:
		return 0
	case PlanPlus:
		return 50
	case PlanGuest:
		return 1
	case PlanFree:
		fallthrough
	default:
		return 5
	}
}
