package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TryOn represents a virtual try-on session and result
type TryOn struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID            string             `bson:"user_id" json:"user_id"`
	PersonID          string             `bson:"person_id" json:"person_id"`
	ProductURL        string             `bson:"product_url" json:"product_url"`
	ProductID         string             `bson:"product_id,omitempty" json:"product_id,omitempty"` // Optional link to scraped product
	PersonImageURL    string             `bson:"person_image_url" json:"person_image_url"`
	ProductImageURL   string             `bson:"product_image_url" json:"product_image_url,omitempty"` // Specific image used if any
	GeneratedImageURL string             `bson:"generated_image_url" json:"generated_image_url"`       // Path to local file or URL
	Status            string             `bson:"status" json:"status"`                                 // e.g. "completed", "failed"
	CreatedAt         time.Time          `bson:"created_at" json:"created_at"`
	IsDeleted         bool               `bson:"is_deleted" json:"is_deleted"` // Soft delete flag
}
