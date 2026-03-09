package api

import "time"

// Profile3D is stored in the "profiles_3d" MongoDB collection.
// There is at most one document per user (upserted on job completion).
// It holds the final CDN URLs for the user's 3D avatar assets.
type Profile3D struct {
	UserID       string `bson:"user_id"       json:"user_id"`
	GLBURL       string `bson:"glb_url"       json:"glb_url"`
	USDZURL      string `bson:"usdz_url"      json:"usdz_url"`
	PreviewImage string `bson:"preview_image" json:"preview_image"`
	// Version is incremented each time the user regenerates their profile.
	Version   int       `bson:"version"    json:"version"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}
