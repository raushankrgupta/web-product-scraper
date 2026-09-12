package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Collections owned by the trends feature.
const (
	// CollTrends holds the admin-authored trend definitions. Written by the
	// admin backend, read (never written) by this one.
	CollTrends = "trends"
	// CollTrendRuns holds one row per generation attempt, successful or not.
	CollTrendRuns = "trend_runs"
	// CollTrendUploads tracks ad-hoc photos a user supplied for a trend that
	// did not come from one of their profiles.
	CollTrendUploads = "trend_uploads"
	// CollAppSettings holds singleton configuration documents edited from the
	// admin panel. Today that is one document, _id "app_update".
	CollAppSettings = "app_settings"
)

// Trend status values. A trend is only ever served to the app when it is
// active — draft is the authoring state and expired is the retirement state,
// and neither is distinguishable from the other by the client because neither
// is sent.
const (
	TrendStatusDraft   = "draft"
	TrendStatusActive  = "active"
	TrendStatusExpired = "expired"
)

// Where a subject photo may come from.
const (
	PhotoSourceProfile = "profile"
	PhotoSourceUpload  = "upload"
)

// Background modes an admin may offer.
const (
	BackgroundModeNone   = "none"
	BackgroundModePreset = "preset"
	BackgroundModeUpload = "upload"
	BackgroundModeText   = "text"
)

// Custom field types. Anything the structured blocks below cannot express is
// expressed with one of these, which is what lets a new kind of question ship
// without an app release.
const (
	FieldTypeText     = "text"
	FieldTypeTextarea = "textarea"
	FieldTypeSingle   = "single_choice"
	FieldTypeMulti    = "multi_choice"
	FieldTypeBoolean  = "boolean"
	FieldTypeNumber   = "number"
	FieldTypeImage    = "image"
)

// Pricing modes.
const (
	PricingFlat    = "flat"
	PricingDynamic = "dynamic"
)

// Trend is one admin-authored AI generation flow.
//
// Three separable things live in this document and only two of them ever
// travel to a device:
//
//   - Inputs describes what the user is asked for. The app renders it; there
//     is no screen per trend, only a renderer for this schema.
//   - Pricing describes what it costs. The client gets it so a price can be
//     shown live as selections change, but the server recomputes from this
//     same block and is the only thing that charges.
//   - Generation describes the model and the prompt. It is stripped before
//     serialisation (see PublicTrend) and must never reach a client — the
//     prompt is the product.
type Trend struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Slug string             `bson:"slug" json:"slug"`

	// Version is bumped on every admin save and stamped onto every run, so a
	// result generated before an edit can still be explained afterwards.
	Version int `bson:"version" json:"version"`

	// --- Presentation ---
	Title           string   `bson:"title" json:"title"`
	Subtitle        string   `bson:"subtitle,omitempty" json:"subtitle,omitempty"`
	Description     string   `bson:"description,omitempty" json:"description,omitempty"`
	CoverImageURL   string   `bson:"cover_image_url,omitempty" json:"cover_image_url,omitempty"`
	SampleImageURLs []string `bson:"sample_image_urls,omitempty" json:"sample_image_urls,omitempty"`
	Badge           string   `bson:"badge,omitempty" json:"badge,omitempty"`
	AccentColor     string   `bson:"accent_color,omitempty" json:"accent_color,omitempty"`
	Category        string   `bson:"category,omitempty" json:"category,omitempty"`
	SortOrder       int      `bson:"sort_order" json:"sort_order"`

	// --- Availability ---
	Status       string     `bson:"status" json:"status"`
	Environments []string   `bson:"environments,omitempty" json:"environments,omitempty"`
	StartsAt     *time.Time `bson:"starts_at,omitempty" json:"starts_at,omitempty"`
	EndsAt       *time.Time `bson:"ends_at,omitempty" json:"ends_at,omitempty"`
	// MinAppVersion travels to the client, which hides what it cannot render.
	// Filtering here instead would make the response vary per app version and
	// cost the shared cache that every cold start benefits from.
	MinAppVersion string `bson:"min_app_version,omitempty" json:"min_app_version,omitempty"`

	Inputs     TrendInputs     `bson:"inputs" json:"inputs"`
	Generation TrendGeneration `bson:"generation" json:"-"`
	Pricing    TrendPricing    `bson:"pricing" json:"pricing"`

	RunCount  int        `bson:"run_count" json:"run_count"`
	LastRunAt *time.Time `bson:"last_run_at,omitempty" json:"last_run_at,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
	CreatedBy string    `bson:"created_by,omitempty" json:"created_by,omitempty"`
}

// TrendInputs is the whole question set a trend asks.
//
// Structured blocks cover the things the app renders with a purpose-built
// control — a person picker is not a text box and never will be. Fields
// covers everything else.
type TrendInputs struct {
	People      PeopleInput     `bson:"people" json:"people"`
	Wardrobe    WardrobeInput   `bson:"wardrobe" json:"wardrobe"`
	Background  BackgroundInput `bson:"background" json:"background"`
	Pose        TextInput       `bson:"pose" json:"pose"`
	Instruction TextInput       `bson:"instruction" json:"instruction"`
	Quality     QualityInput    `bson:"quality" json:"quality"`
	Outputs     CountInput      `bson:"outputs" json:"outputs"`
	Aspect      ChoiceInput     `bson:"aspect" json:"aspect"`
	Fields      []CustomField   `bson:"fields,omitempty" json:"fields,omitempty"`
}

// PeopleInput configures the subject picker.
type PeopleInput struct {
	Enabled bool `bson:"enabled" json:"enabled"`
	// Sources is any of "profile" and "upload". Empty means profile only.
	Sources []string `bson:"sources,omitempty" json:"sources,omitempty"`
	Min     int      `bson:"min" json:"min"`
	Max     int      `bson:"max" json:"max"`
	// AllowAddPerson surfaces "add a new person" inside the picker, so a
	// trend needing two faces is not a dead end for a one-profile account.
	AllowAddPerson bool   `bson:"allow_add_person" json:"allow_add_person"`
	RequireFace    bool   `bson:"require_face" json:"require_face"`
	Label          string `bson:"label,omitempty" json:"label,omitempty"`
	Help           string `bson:"help,omitempty" json:"help,omitempty"`
	// RoleLabels names each slot ("Lead star", "Co-star"). Shorter than Max
	// is fine; the app falls back to a number.
	RoleLabels []string `bson:"role_labels,omitempty" json:"role_labels,omitempty"`
}

// WardrobeInput configures optional outfit selection.
type WardrobeInput struct {
	Enabled bool `bson:"enabled" json:"enabled"`
	// Sources is any of "wardrobe", "upload", "text".
	Sources []string `bson:"sources,omitempty" json:"sources,omitempty"`
	// Slots is any of "top", "bottom", "dress", "accessory".
	Slots        []string `bson:"slots,omitempty" json:"slots,omitempty"`
	Min          int      `bson:"min" json:"min"`
	Max          int      `bson:"max" json:"max"`
	TextMaxChars int      `bson:"text_max_chars,omitempty" json:"text_max_chars,omitempty"`
	Label        string   `bson:"label,omitempty" json:"label,omitempty"`
	Help         string   `bson:"help,omitempty" json:"help,omitempty"`
}

// BackgroundPreset is one curated background an admin offers on a trend.
type BackgroundPreset struct {
	ID             string `bson:"id" json:"id"`
	Title          string `bson:"title" json:"title"`
	ImageKey       string `bson:"image_key,omitempty" json:"image_key,omitempty"`
	PromptFragment string `bson:"prompt_fragment,omitempty" json:"prompt_fragment,omitempty"`
}

// BackgroundInput configures the scene chooser.
type BackgroundInput struct {
	Enabled bool `bson:"enabled" json:"enabled"`
	// Modes is any of "none", "preset", "upload", "text".
	Modes        []string           `bson:"modes,omitempty" json:"modes,omitempty"`
	DefaultMode  string             `bson:"default_mode,omitempty" json:"default_mode,omitempty"`
	Presets      []BackgroundPreset `bson:"presets,omitempty" json:"presets,omitempty"`
	TextMaxChars int                `bson:"text_max_chars,omitempty" json:"text_max_chars,omitempty"`
	Label        string             `bson:"label,omitempty" json:"label,omitempty"`
	Help         string             `bson:"help,omitempty" json:"help,omitempty"`
}

// TextInput is a single free-text question (pose, instruction).
type TextInput struct {
	Enabled     bool   `bson:"enabled" json:"enabled"`
	Label       string `bson:"label,omitempty" json:"label,omitempty"`
	Help        string `bson:"help,omitempty" json:"help,omitempty"`
	Placeholder string `bson:"placeholder,omitempty" json:"placeholder,omitempty"`
	MaxChars    int    `bson:"max_chars,omitempty" json:"max_chars,omitempty"`
	Required    bool   `bson:"required,omitempty" json:"required,omitempty"`
	// Presets are one-tap suggestions that fill the box.
	Presets []string `bson:"presets,omitempty" json:"presets,omitempty"`
}

// QualityInput says which generation tiers a trend offers.
type QualityInput struct {
	Enabled bool     `bson:"enabled" json:"enabled"`
	Allowed []string `bson:"allowed,omitempty" json:"allowed,omitempty"`
	Default string   `bson:"default,omitempty" json:"default,omitempty"`
	Label   string   `bson:"label,omitempty" json:"label,omitempty"`
}

// CountInput is a bounded integer question (how many images to generate).
type CountInput struct {
	Enabled bool   `bson:"enabled" json:"enabled"`
	Min     int    `bson:"min" json:"min"`
	Max     int    `bson:"max" json:"max"`
	Default int    `bson:"default" json:"default"`
	Label   string `bson:"label,omitempty" json:"label,omitempty"`
	Help    string `bson:"help,omitempty" json:"help,omitempty"`
}

// FieldChoice is one option of a choice field or a ChoiceInput.
type FieldChoice struct {
	Value string `bson:"value" json:"value"`
	Label string `bson:"label" json:"label"`
	// PromptFragment is admin-authored and therefore trusted: it is inlined
	// into the prompt verbatim when this choice is selected.
	PromptFragment string `bson:"prompt_fragment,omitempty" json:"prompt_fragment,omitempty"`
	ImageKey       string `bson:"image_key,omitempty" json:"image_key,omitempty"`
}

// ChoiceInput is a fixed single-select rendered as chips (aspect ratio).
type ChoiceInput struct {
	Enabled bool          `bson:"enabled" json:"enabled"`
	Choices []FieldChoice `bson:"choices,omitempty" json:"choices,omitempty"`
	Default string        `bson:"default,omitempty" json:"default,omitempty"`
	Label   string        `bson:"label,omitempty" json:"label,omitempty"`
}

// CustomField is an arbitrary extra question.
//
// This is the extensibility escape hatch, and the reason a trend like
// "1980s Retro Movie Star" can ask for a movie title — a question no fixed
// schema would have predicted — without shipping an app release.
type CustomField struct {
	// Key is how the prompt template refers to the answer: {{fields.<key>}}.
	Key      string        `bson:"key" json:"key"`
	Label    string        `bson:"label" json:"label"`
	Help     string        `bson:"help,omitempty" json:"help,omitempty"`
	Type     string        `bson:"type" json:"type"`
	Required bool          `bson:"required,omitempty" json:"required,omitempty"`
	Default  interface{}   `bson:"default,omitempty" json:"default,omitempty"`
	MaxChars int           `bson:"max_chars,omitempty" json:"max_chars,omitempty"`
	Min      float64       `bson:"min,omitempty" json:"min,omitempty"`
	Max      float64       `bson:"max,omitempty" json:"max,omitempty"`
	Step     float64       `bson:"step,omitempty" json:"step,omitempty"`
	Choices  []FieldChoice `bson:"choices,omitempty" json:"choices,omitempty"`
	// PromptFragment is emitted when the field holds a value, with {{value}}
	// replaced. Boolean fields emit it when true and nothing when false.
	PromptFragment string `bson:"prompt_fragment,omitempty" json:"prompt_fragment,omitempty"`
	Placeholder    string `bson:"placeholder,omitempty" json:"placeholder,omitempty"`
	Order          int    `bson:"order" json:"order"`
}

// TrendGeneration is the server-only half: which model runs, and what it is
// told. Never serialised to a client — note the `json:"-"` on Trend.
type TrendGeneration struct {
	// Provider is "gemini" today. OpenAI is reachable through the existing
	// fallback machinery but is not selectable per trend yet.
	Provider string `bson:"provider,omitempty" json:"provider,omitempty"`
	// QualityTier resolves the model and the cost floor through
	// config/stars.json, exactly as a try-on does, which is what keeps a
	// trend's price connected to what it costs us to serve.
	QualityTier string `bson:"quality_tier,omitempty" json:"quality_tier,omitempty"`
	// ModelOverride pins a literal model id. Honoured, but flagged in admin:
	// a model outside stars.json cannot be margin-checked.
	ModelOverride  string   `bson:"model_override,omitempty" json:"model_override,omitempty"`
	PromptTemplate string   `bson:"prompt_template" json:"prompt_template"`
	TersePrompt    string   `bson:"terse_prompt,omitempty" json:"terse_prompt,omitempty"`
	NegativePrompt string   `bson:"negative_prompt,omitempty" json:"negative_prompt,omitempty"`
	ReferenceKeys  []string `bson:"reference_image_keys,omitempty" json:"reference_image_keys,omitempty"`
	// IdentityGuard appends the "this is a real person, keep their face"
	// clause. On by default for anything using a subject photo.
	IdentityGuard bool `bson:"identity_guard" json:"identity_guard"`
	MaxImages     int  `bson:"max_images,omitempty" json:"max_images,omitempty"`
	TimeoutSecs   int  `bson:"timeout_secs,omitempty" json:"timeout_secs,omitempty"`
}

// TrendPricing is the cost formula. Shipped to the client so a price can be
// rendered live, and recomputed server-side so the client's copy can never be
// the thing that charges.
type TrendPricing struct {
	Mode      string `bson:"mode" json:"mode"`
	FlatStars int    `bson:"flat_stars,omitempty" json:"flat_stars,omitempty"`

	BaseStars           int `bson:"base_stars,omitempty" json:"base_stars,omitempty"`
	IncludedPeople      int `bson:"included_people,omitempty" json:"included_people,omitempty"`
	PerPersonStars      int `bson:"per_person_stars,omitempty" json:"per_person_stars,omitempty"`
	PerExtraImageStars  int `bson:"per_extra_image_stars,omitempty" json:"per_extra_image_stars,omitempty"`
	PerExtraOutputStars int `bson:"per_extra_output_stars,omitempty" json:"per_extra_output_stars,omitempty"`

	QualityMultiplier map[string]float64 `bson:"quality_multiplier,omitempty" json:"quality_multiplier,omitempty"`

	MinStars int `bson:"min_stars,omitempty" json:"min_stars,omitempty"`
	// MaxStars is a safety rail, not a product decision: a mistyped
	// per_person_stars should cost one confused user, not a whole balance.
	MaxStars int `bson:"max_stars,omitempty" json:"max_stars,omitempty"`

	// FreeEligible lets the daily free generation cover this trend. Off by
	// default — these are premium flows.
	FreeEligible bool `bson:"free_eligible,omitempty" json:"free_eligible,omitempty"`
}

// TrendUploadTTL is how long an ad-hoc photo survives if it is never used.
// Cleared on the uploads a successful generation actually consumed, so those
// persist as part of that run's evidence.
const TrendUploadTTL = 24 * time.Hour

// TrendUpload is one ad-hoc photo staged for a trend generation.
type TrendUpload struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    string             `bson:"user_id" json:"user_id"`
	ObjectKey string             `bson:"object_key" json:"object_key"`
	Mime      string             `bson:"mime,omitempty" json:"mime,omitempty"`
	Bytes     int64              `bson:"bytes,omitempty" json:"bytes,omitempty"`
	// ExpiresAt is unset once a generation has used this upload, which is what
	// promotes it from "staged" to "evidence" without moving the object.
	ExpiresAt *time.Time `bson:"expires_at,omitempty" json:"expires_at,omitempty"`
	CreatedAt time.Time  `bson:"created_at" json:"created_at"`
}
