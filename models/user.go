package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Subscription plan tiers. Free is the implicit default for any user that has
// not signed up for a paid plan (or whose `plan` field is missing in Mongo —
// older documents pre-dating this field).
const (
	PlanFree  = "free"
	PlanPlus  = "plus"
	PlanPro   = "pro"
	PlanGuest = "guest"
)

// User represents a registered user
type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name         string             `bson:"name" json:"name"`
	Email        string             `bson:"email" json:"email"`
	Password     string             `bson:"password" json:"-"` // Password is not returned in JSON
	DOB          string             `bson:"dob,omitempty" json:"dob,omitempty"`
	Gender       string             `bson:"gender,omitempty" json:"gender,omitempty"`
	Status       string             `bson:"status" json:"status"`                 // pending, verified, active, deleted
	Plan         string             `bson:"plan,omitempty" json:"plan,omitempty"` // free | plus | pro | guest
	OTP          string             `bson:"otp,omitempty" json:"-"`               // OTP for email verification / password reset
	OTPExpiresAt time.Time          `bson:"otp_expires_at,omitempty" json:"-"`    // OTP expiration timestamp
	OTPAttempts  int                `bson:"otp_attempts,omitempty" json:"-"`      // Consecutive failed OTP attempts
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt    time.Time          `bson:"updated_at" json:"updated_at"`

	// DeletedEmail preserves the original address when an account is
	// soft-deleted. The live `email` field is renamed to a tombstone so the
	// address is free for a genuine re-signup — leaving it in place created a
	// permanent Google-login lockout.
	DeletedEmail string    `bson:"deleted_email,omitempty" json:"-"`
	DeletedAt    time.Time `bson:"deleted_at,omitempty" json:"-"`
}

// PlanOrDefault returns the user's plan, falling back to PlanFree for documents
// that don't yet have the field populated (back-compat after introducing
// `plan` on the User struct).
func (u *User) PlanOrDefault() string {
	if u == nil || u.Plan == "" {
		return PlanFree
	}
	return u.Plan
}
