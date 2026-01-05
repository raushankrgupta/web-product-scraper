package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Person represents a user profile with body dimensions and images
type Person struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name       string             `bson:"name" json:"name"`
	Age        int                `bson:"age" json:"age"`
	Gender     string             `bson:"gender" json:"gender"`
	Height     float64            `bson:"height" json:"height"` // in cm
	Weight     float64            `bson:"weight" json:"weight"` // in kg
	Chest      float64            `bson:"chest" json:"chest"`   // in inches
	Waist      float64            `bson:"waist" json:"waist"`   // in inches
	Hips       float64            `bson:"hips" json:"hips"`     // in inches
	ImagePaths []string           `bson:"image_paths" json:"image_paths"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt  time.Time          `bson:"updated_at" json:"updated_at"`
}
