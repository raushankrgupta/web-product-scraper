package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CollTryOnFailures holds one row per generation that did not produce an
// image the user could keep.
//
// It is deliberately a *separate* collection from "tryons" rather than a
// status on it. The gallery reads `tryons` and filters on
// status == "completed", which means a failed row there is one forgotten
// filter away from appearing in someone's feed as a broken tile. Keeping
// failures out of the collection entirely makes that impossible rather than
// merely unlikely, and nothing reads this collection on a user-facing path.
const CollTryOnFailures = "tryon_failures"

// TryOnFailure is an internal post-mortem record: everything needed to
// reproduce a failed generation by hand, plus the decoded reason it failed.
//
// It exists because a failed try-on is the one event where we lose money and
// learn nothing — the image is not generated, the stars are refunded, and
// until now the only trace was a log line that rotates away. The interesting
// question ("which garment photos does the model refuse, and why?") can only
// be answered from the inputs, so the inputs are what we keep.
//
// NEVER serialise this to a client. RawError holds the verbatim upstream
// message, which in production has included Google AI Studio billing URLs and
// S3/IAM detail. There is no JSON tag on that field for exactly that reason.
type TryOnFailure struct {
	ID      primitive.ObjectID `bson:"_id,omitempty"`
	UserID  string             `bson:"user_id"`
	IsGuest bool               `bson:"is_guest"`

	Route string `bson:"route"` // e.g. /try-on/couple
	Type  string `bson:"type"`  // individual | couple | group | legacy | guest

	// Stage is where in the request the try-on died. The five are deliberately
	// coarse, because the question they answer is "who owns this?":
	//
	//	precheck — a lookup or validation failed before we spent anything.
	//	           Ours: a dangling wardrobe id, a product that never scraped.
	//	gate     — rejected by TryOnGuardMiddleware or StarGateMiddleware:
	//	           duplicate in flight, failure-loop throttle, no stars. Not a
	//	           fault at all, but it is a try-on the user did not get, and
	//	           the shape of this bucket is the shape of the paywall.
	//	generate — the upstream model did not return an image. The expensive one.
	//	store    — an image existed and S3 would not take it. The worst one:
	//	           we paid, the user watched it work, and got nothing.
	//	refund   — stars were held and could not be returned. The only stage
	//	           where a row means someone is owed money.
	Stage string `bson:"stage"`

	// Provider/Model/Quality identify what we actually paid for. With two
	// quality tiers and two vendors in play, "generation failed" is not
	// actionable without them.
	Provider string `bson:"provider"`
	Model    string `bson:"model"`
	Quality  string `bson:"quality"`

	// Attempts records every provider we tried, in order, with the reason each
	// one gave. A single Reason field cannot express "Gemini refused on
	// safety, then OpenAI timed out" — and that sequence is the whole point of
	// having a fallback, so it has to be the thing we can count.
	Attempts []TryOnAttempt `bson:"attempts,omitempty"`

	// Reason is the stable machine code (utils.FailureReason). Group by it.
	// FinishReason is the decoded upstream enum, e.g. "IMAGE_SAFETY(11)",
	// present only for content refusals.
	Reason       string `bson:"reason"`
	FinishReason string `bson:"finish_reason,omitempty"`
	// RawError is the verbatim upstream error. Internal only — see above.
	RawError   string `bson:"raw_error"`
	HTTPStatus int    `bson:"http_status"`

	RequestID  string `bson:"request_id,omitempty"`
	DurationMS int64  `bson:"duration_ms"`

	Inputs    TryOnFailureInputs `bson:"inputs"`
	CreatedAt time.Time          `bson:"created_at"`

	// ExpiresAt drives a TTL index. Post-mortems are for the investigation
	// happening now and the trend over a couple of months; keeping every
	// failed try-on for the life of the product just grows a collection
	// nobody queries past its first month.
	ExpiresAt time.Time `bson:"expires_at"`
}

// TryOnAttempt is one provider's shot at a generation.
type TryOnAttempt struct {
	Provider     string `bson:"provider"`
	Model        string `bson:"model"`
	Reason       string `bson:"reason"`
	FinishReason string `bson:"finish_reason,omitempty"`
	RawError     string `bson:"raw_error,omitempty"`
	DurationMS   int64  `bson:"duration_ms"`
}

// TryOnFailureInputs is the reproduction recipe.
//
// Image references are stored as S3 *keys*, never as the presigned URLs the
// generator was handed: a presigned URL expires within the hour and is
// useless to whoever picks the row up next week, while the key still resolves.
type TryOnFailureInputs struct {
	People         []TryOnPerson `bson:"people,omitempty"`
	PersonDetails  []string      `bson:"person_details,omitempty"`
	PersonKeys     []string      `bson:"person_keys,omitempty"`
	GarmentKeys    []string      `bson:"garment_keys,omitempty"`
	ThemeID        string        `bson:"theme_id,omitempty"`
	UseTheme       bool          `bson:"use_theme,omitempty"`
	ProductID      string        `bson:"product_id,omitempty"`
	ProductURL     string        `bson:"product_url,omitempty"`
	SpecialRequest string        `bson:"special_request,omitempty"`
}
