package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Theme represents a visual style or background environment for the try-on features
type Theme struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TemplateID     string             `bson:"template_id" json:"template_id"`                   // E.g., "0" for No Background, "1" for Casual
	Name           string             `bson:"name" json:"name"`                                 // E.g., "Casual", "Business Pro"
	Title          string             `bson:"title,omitempty" json:"title,omitempty"`
	Description    string             `bson:"description,omitempty" json:"description,omitempty"`
	ThemeImageURL  string             `bson:"theme_image_url,omitempty" json:"theme_image_url,omitempty"`
	Type           string             `bson:"type,omitempty" json:"type,omitempty"`
	ImageURL       string             `bson:"image_url" json:"image_url"`                       // URL to the thumbnail image shown on the home page
	Category       string             `bson:"category" json:"category"`                         // E.g., "Daily", "Formal", "Seasonal"
	PromptModifier string             `bson:"prompt_modifier" json:"prompt_modifier,omitempty"` // Words to inject into the Gemini prompt
	CreatedAt      time.Time          `bson:"created_at" json:"created_at"`
	IsActive       bool               `bson:"is_active" json:"is_active"`
}
