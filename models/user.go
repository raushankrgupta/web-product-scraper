package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// User represents a registered user
type User struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name              string             `bson:"name" json:"name"`
	Email             string             `bson:"email" json:"email"`
	Password          string             `bson:"password" json:"-"` // Password is not returned in JSON
	DOB               string             `bson:"dob,omitempty" json:"dob,omitempty"`
	Gender            string             `bson:"gender,omitempty" json:"gender,omitempty"`
	Status            string             `bson:"status" json:"status"`        // pending, verified, active
	VerificationToken string             `bson:"verification_token" json:"-"` // Token for email verification
	OTP               string             `bson:"otp" json:"-"`                // OTP for email verification
	CreatedAt         time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt         time.Time          `bson:"updated_at" json:"updated_at"`
}
