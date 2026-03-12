package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Theme represents a visual style or background environment for the try-on features
type Theme struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Title              string             `bson:"title,omitempty" json:"title,omitempty"`
	Description        string             `bson:"description,omitempty" json:"description,omitempty"`
	ThemeImageURL      string             `bson:"theme_image_url,omitempty" json:"theme_image_url,omitempty"`
	ThemeBlankImageURL string             `bson:"theme_blank_image_url,omitempty" json:"theme_blank_image_url,omitempty"`
	Type               string             `bson:"type,omitempty" json:"type,omitempty"`
	CreatedAt          time.Time          `bson:"created_at" json:"created_at"`
	IsActive           bool               `bson:"is_active" json:"is_active"`
}
