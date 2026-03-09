package api

import "time"

// ProfileJobStatus represents the lifecycle state of a 3D profile generation job.
type ProfileJobStatus string

const (
	JobStatusPending    ProfileJobStatus = "pending"
	JobStatusProcessing ProfileJobStatus = "processing"
	JobStatusFailed     ProfileJobStatus = "failed"
	JobStatusCompleted  ProfileJobStatus = "completed"
)

// ProfileJobResult holds the CDN URLs for generated 3D assets, populated on completion.
type ProfileJobResult struct {
	GLBURL       string `bson:"glb_url,omitempty"       json:"glb_url,omitempty"`
	USDZURL      string `bson:"usdz_url,omitempty"      json:"usdz_url,omitempty"`
	PreviewImage string `bson:"preview_image,omitempty" json:"preview_image,omitempty"`
}

// ProfileJob is stored in the "profile_jobs" MongoDB collection.
// It tracks an async 3D reconstruction job submitted by a user.
type ProfileJob struct {
	// JobID is a UUID string used as the primary lookup key.
	JobID  string           `bson:"job_id"            json:"job_id"`
	UserID string           `bson:"user_id"           json:"user_id"`
	Status ProfileJobStatus `bson:"status"            json:"status"`
	// Progress is 0-100%; updated by the background worker as stages complete.
	Progress int              `bson:"progress"          json:"progress"`
	Error    string           `bson:"error,omitempty"   json:"error,omitempty"`
	Result   ProfileJobResult `bson:"result,omitempty"  json:"result,omitempty"`
	Height   float64          `bson:"height,omitempty"  json:"height,omitempty"`

	// S3 object keys for the raw uploaded images (not exposed to client).
	// The worker reads these to process the images and deletes them when done.
	FrontKey string `bson:"front_key" json:"-"`
	BackKey  string `bson:"back_key"  json:"-"`
	SideKey  string `bson:"side_key"  json:"-"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}
