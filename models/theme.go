package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Theme represents a visual style or background environment for the try-on features.
//
// Two kinds of document live in this collection and they are told apart by
// UserID alone:
//
//   - Curated themes have no UserID. They are the catalogue everyone sees, and
//     GET /themes serves them from a public, shared cache.
//   - Custom backgrounds carry the UserID of whoever uploaded them. They are
//     private, must never reach the public listing, and are only usable in a
//     try-on by their owner.
//
// Every query on this collection therefore has to be explicit about which kind
// it wants. A filter that forgets is how one user's living room ends up in
// another user's theme picker.
type Theme struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Title              string             `bson:"title,omitempty" json:"title,omitempty"`
	Description        string             `bson:"description,omitempty" json:"description,omitempty"`
	ThemeImageURL      string             `bson:"theme_image_url,omitempty" json:"theme_image_url,omitempty"`
	ThemeBlankImageURL string             `bson:"theme_blank_image_url,omitempty" json:"theme_blank_image_url,omitempty"`
	Type               string             `bson:"type,omitempty" json:"type,omitempty"`
	CreatedAt          time.Time          `bson:"created_at" json:"created_at"`
	IsActive           bool               `bson:"is_active" json:"is_active"`

	// UserID is set only on a user's own uploaded background. Its absence is
	// what makes a theme public, so it is omitempty on purpose: curated themes
	// carry no such field at all, and `user_id: {$exists: false}` is the
	// filter that selects them.
	UserID primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`

	// IsCustom is denormalised from "UserID is set" for the client's benefit —
	// the picker renders a delete affordance on these and not on curated ones.
	IsCustom bool `bson:"is_custom,omitempty" json:"is_custom,omitempty"`

	// IsDeleted soft-deletes a custom background. Soft rather than hard
	// because past try-on records reference the theme id, and a hard delete
	// would leave that history pointing at nothing.
	IsDeleted bool `bson:"is_deleted,omitempty" json:"-"`
}

// CustomThemeUploadLimit caps how many backgrounds one account can keep.
//
// Not an anti-abuse measure so much as a cost bound: every upload is an S3
// object we store indefinitely, and there is no plausible use for a hundred
// of them. Users delete one to add another.
const CustomThemeUploadLimit = 20
