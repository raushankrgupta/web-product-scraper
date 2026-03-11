package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TryOnPerson represents a person and their selected clothing items in a try-on session
type TryOnPerson struct {
	PersonID    string `bson:"person_id" json:"person_id"`
	TopID       string `bson:"top_id,omitempty" json:"top_id,omitempty"`
	BottomID    string `bson:"bottom_id,omitempty" json:"bottom_id,omitempty"`
	AccessoryID string `bson:"accessory_id,omitempty" json:"accessory_id,omitempty"`
}

// TryOn represents a virtual try-on session and result
type TryOn struct {
	ID      primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID  string             `bson:"user_id" json:"user_id"`
	Type    string             `bson:"type" json:"type"` // "individual", "couple", "group"
	ThemeID string             `bson:"theme_id" json:"theme_id,omitempty"`
	People  []TryOnPerson      `bson:"people" json:"people"`

	// Legacy fields (Keeping for backward compatibility with older generated results)
	PersonID        string `bson:"person_id,omitempty" json:"person_id,omitempty"`
	ProductURL      string `bson:"product_url,omitempty" json:"product_url,omitempty"`
	ProductID       string `bson:"product_id,omitempty" json:"product_id,omitempty"`
	PersonImageURL  string `bson:"person_image_url,omitempty" json:"person_image_url,omitempty"`
	ProductImageURL string `bson:"product_image_url,omitempty" json:"product_image_url,omitempty"`

	GeneratedImageURL string    `bson:"generated_image_url" json:"generated_image_url"`
	Status            string    `bson:"status" json:"status"`
	CreatedAt         time.Time `bson:"created_at" json:"created_at"`
	IsDeleted         bool      `bson:"is_deleted" json:"is_deleted"`
	IsFavorite        bool      `bson:"is_favorite" json:"is_favorite"`
	IsSaved           bool      `bson:"is_saved" json:"is_saved"`
	Rating            int       `bson:"rating,omitempty" json:"rating,omitempty"`
	Comment           string    `bson:"comment,omitempty" json:"comment,omitempty"`
}
