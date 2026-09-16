package models

import "time"

// AppUpdateSettingsID is the _id of the singleton document in app_settings
// that drives the in-app "Update Available" sheet.
const AppUpdateSettingsID = "app_update"

// Update modes.
const (
	// UpdateModeOff shows nothing, whatever the versions say. This is the
	// failure mode as well as a setting: a missing or unreadable document
	// resolves to off, so a database problem can never lock users out of the
	// app behind a forced update they cannot dismiss.
	UpdateModeOff = "off"
	// UpdateModeSoft prompts, dismissibly, when a newer version exists.
	UpdateModeSoft = "soft"
	// UpdateModeForce additionally blocks anyone below MinSupportedVersion.
	UpdateModeForce = "force"
)

// AppUpdateSettings is the admin-editable half of GET /app/config's
// app_update block.
//
// It lives in Mongo rather than in environment variables because it is
// flipped at the moment a Play release goes live — which is not a moment
// anyone wants to be redeploying a backend.
type AppUpdateSettings struct {
	ID string `bson:"_id" json:"-"`

	Mode string `bson:"mode" json:"mode"`

	// LatestVersion is what is live on the store right now. Anyone below it
	// gets the soft prompt.
	LatestVersion string `bson:"latest_version" json:"latest_version"`
	// MinSupportedVersion is the floor. Anyone below it is blocked, and only
	// when Mode is "force".
	MinSupportedVersion string `bson:"min_supported_version" json:"min_supported_version"`

	// IOSLatestVersion / IOSMinSupportedVersion are the same two levers for
	// iPhones, set separately on purpose. An iOS release goes live only after
	// App Review — a day or more after the Android one — so sharing one
	// version would prompt every iPhone user to install a build the App Store
	// does not have yet, and in force mode lock them out of the app. Empty
	// means "never prompt on iOS"; the Android fields are never used as a
	// fallback.
	IOSLatestVersion       string `bson:"ios_latest_version,omitempty" json:"ios_latest_version"`
	IOSMinSupportedVersion string `bson:"ios_min_supported_version,omitempty" json:"ios_min_supported_version"`

	AndroidStoreURL string `bson:"android_store_url,omitempty" json:"android_store_url"`
	IOSStoreURL     string `bson:"ios_store_url,omitempty" json:"ios_store_url"`

	Title        string   `bson:"title,omitempty" json:"title"`
	Message      string   `bson:"message,omitempty" json:"message"`
	Highlights   []string `bson:"highlights,omitempty" json:"highlights"`
	CTALabel     string   `bson:"cta_label,omitempty" json:"cta_label"`
	DismissLabel string   `bson:"dismiss_label,omitempty" json:"dismiss_label"`

	// RemindAfterHours is how long "Not now" suppresses the soft sheet.
	RemindAfterHours int `bson:"remind_after_hours,omitempty" json:"remind_after_hours"`

	// Revision re-prompts everyone who has already dismissed this version's
	// sheet. It is the lever for "this release actually matters" — bump it
	// and the snooze on every device is void.
	Revision int `bson:"revision,omitempty" json:"revision"`

	UpdatedAt time.Time `bson:"updated_at,omitempty" json:"-"`
	UpdatedBy string    `bson:"updated_by,omitempty" json:"-"`
}

// DefaultAppUpdateSettings is what an empty database, an unreachable one, or
// a malformed document all resolve to. Mode is off: silence is always the
// safe answer here.
func DefaultAppUpdateSettings() AppUpdateSettings {
	return AppUpdateSettings{
		ID:                  AppUpdateSettingsID,
		Mode:                UpdateModeOff,
		LatestVersion:       "",
		MinSupportedVersion: "",
		AndroidStoreURL:     "https://play.google.com/store/apps/details?id=com.raushan26.tryonfusion",
		Title:               "Update Available",
		Message:             "A new version of TryOnFusion is ready.",
		CTALabel:            "Update Now",
		DismissLabel:        "Not now",
		RemindAfterHours:    24,
		Revision:            1,
	}
}
