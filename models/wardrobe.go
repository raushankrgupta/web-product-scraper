package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// WardrobeItem represents a saved product in the user's wardrobe
type WardrobeItem struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID     string             `bson:"user_id" json:"user_id"`
	Category   string             `bson:"category" json:"category"`
	Images     []string           `bson:"images" json:"images"`
	SourceURL  string             `bson:"source_url,omitempty" json:"source_url,omitempty"`
	IsFavorite bool               `bson:"is_favorite" json:"is_favorite"`
	SavedAt    time.Time          `bson:"saved_at" json:"saved_at"`
}
