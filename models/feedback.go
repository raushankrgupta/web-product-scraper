package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Feedback represents user feedback
type Feedback struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID       primitive.ObjectID `bson:"user_id" json:"user_id"`
	Name         string             `bson:"name" json:"name"`
	Email        string             `bson:"email" json:"email"`
	CountryCode  string             `bson:"country_code" json:"country_code"`
	MobileNumber string             `bson:"mobile_number" json:"mobile_number"`
	Message      string             `bson:"message" json:"message"`
	ContactBack  bool               `bson:"contact_back" json:"contact_back"`
	FilePaths    []string           `bson:"file_paths" json:"file_paths"`
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
}
