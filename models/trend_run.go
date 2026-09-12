package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TrendRun is one generation attempt — successful or not.
//
// It exists alongside the TryOn documents a successful run also writes,
// because the two answer different questions. A TryOn is the user's picture:
// it belongs in their gallery, it can be favourited and deleted, and it only
// exists when something worked. A TrendRun is the record of what we asked the
// model and what came back, it survives the user deleting the picture, and it
// is written just as faithfully when the generation failed — which is the
// half that makes a prompt debuggable.
type TrendRun struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TrendID      string             `bson:"trend_id" json:"trend_id"`
	TrendSlug    string             `bson:"trend_slug" json:"trend_slug"`
	TrendTitle   string             `bson:"trend_title,omitempty" json:"trend_title,omitempty"`
	TrendVersion int                `bson:"trend_version" json:"trend_version"`

	UserID    string `bson:"user_id" json:"user_id"`
	UserEmail string `bson:"user_email,omitempty" json:"user_email,omitempty"`

	Status   string `bson:"status" json:"status"`
	Quality  string `bson:"quality,omitempty" json:"quality,omitempty"`
	Provider string `bson:"provider,omitempty" json:"provider,omitempty"`
	Model    string `bson:"model,omitempty" json:"model,omitempty"`

	Inputs TrendRunInputs `bson:"inputs" json:"inputs"`
	// InputKeys are the unsigned S3 keys of every image sent upstream, kept
	// alongside the presigned URLs the generator used so the exact inputs are
	// still resolvable long after those signatures expire.
	InputKeys []string `bson:"input_keys,omitempty" json:"input_keys,omitempty"`

	// ResolvedPrompt is the literal text the model received. Stored verbatim
	// because a trend can be edited afterwards, and "why did it produce that"
	// is only answerable against the prompt that actually ran.
	ResolvedPrompt string `bson:"resolved_prompt,omitempty" json:"resolved_prompt,omitempty"`

	OutputKeys []string `bson:"output_keys,omitempty" json:"output_keys,omitempty"`
	// TryOnIDs are the gallery documents this run produced, one per output.
	TryOnIDs []string `bson:"tryon_ids,omitempty" json:"tryon_ids,omitempty"`

	StarsCharged     int            `bson:"stars_charged" json:"stars_charged"`
	PricingBreakdown map[string]int `bson:"pricing_breakdown,omitempty" json:"pricing_breakdown,omitempty"`

	DurationMS int64          `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
	Error      *TrendRunError `bson:"error,omitempty" json:"error,omitempty"`

	// IsPreview marks an admin test generation: no stars are charged, and it
	// produces no gallery document. Recorded rather than discarded so the
	// cost of iterating on a prompt is visible instead of invisible.
	IsPreview bool `bson:"is_preview,omitempty" json:"is_preview,omitempty"`

	RequestID string    `bson:"request_id,omitempty" json:"request_id,omitempty"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// Run statuses.
const (
	TrendRunCompleted = "completed"
	TrendRunFailed    = "failed"
)

// TrendRunInputs is the resolved selection, after ownership checks and after
// every free-text value has been sanitised.
type TrendRunInputs struct {
	People     []TrendRunPerson   `bson:"people,omitempty" json:"people,omitempty"`
	Wardrobe   []TrendRunGarment  `bson:"wardrobe,omitempty" json:"wardrobe,omitempty"`
	Background TrendRunBackground `bson:"background,omitempty" json:"background,omitempty"`

	Pose        string `bson:"pose,omitempty" json:"pose,omitempty"`
	Instruction string `bson:"instruction,omitempty" json:"instruction,omitempty"`
	Aspect      string `bson:"aspect,omitempty" json:"aspect,omitempty"`
	Outputs     int    `bson:"outputs,omitempty" json:"outputs,omitempty"`

	// Fields holds the custom-field answers, keyed by CustomField.Key.
	Fields map[string]interface{} `bson:"fields,omitempty" json:"fields,omitempty"`
}

// TrendRunPerson is one resolved subject.
type TrendRunPerson struct {
	Source    string `bson:"source" json:"source"`
	PersonID  string `bson:"person_id,omitempty" json:"person_id,omitempty"`
	UploadID  string `bson:"upload_id,omitempty" json:"upload_id,omitempty"`
	ObjectKey string `bson:"object_key,omitempty" json:"object_key,omitempty"`
	Details   string `bson:"details,omitempty" json:"details,omitempty"`
}

// TrendRunGarment is one resolved wardrobe selection.
type TrendRunGarment struct {
	Slot       string   `bson:"slot,omitempty" json:"slot,omitempty"`
	ItemID     string   `bson:"item_id,omitempty" json:"item_id,omitempty"`
	UploadID   string   `bson:"upload_id,omitempty" json:"upload_id,omitempty"`
	ObjectKeys []string `bson:"object_keys,omitempty" json:"object_keys,omitempty"`
	Text       string   `bson:"text,omitempty" json:"text,omitempty"`
}

// TrendRunBackground is the resolved scene choice.
type TrendRunBackground struct {
	Mode      string `bson:"mode,omitempty" json:"mode,omitempty"`
	PresetID  string `bson:"preset_id,omitempty" json:"preset_id,omitempty"`
	UploadID  string `bson:"upload_id,omitempty" json:"upload_id,omitempty"`
	ObjectKey string `bson:"object_key,omitempty" json:"object_key,omitempty"`
	Text      string `bson:"text,omitempty" json:"text,omitempty"`
}

// TrendRunError is why a run failed, in the same vocabulary the try-on
// failure records use (utils.FailureReason).
type TrendRunError struct {
	Reason       string `bson:"reason,omitempty" json:"reason,omitempty"`
	Message      string `bson:"message,omitempty" json:"message,omitempty"`
	FinishReason string `bson:"finish_reason,omitempty" json:"finish_reason,omitempty"`
	Stage        string `bson:"stage,omitempty" json:"stage,omitempty"`
}

// Failure stages, mirroring the split the try-on post-mortems already use:
// what died before we spent anything, versus what died after.
const (
	TrendStageResolve  = "resolve"
	TrendStageGenerate = "generate"
	TrendStageStore    = "store"
)
