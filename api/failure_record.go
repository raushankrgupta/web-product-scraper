package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// This file holds the non-generation halves of failure capture.
//
// respondGenError (tryon_handler.go) covers the expensive case — the upstream
// model was called and did not produce an image. Everything here covers the
// try-ons that never got that far, or that got further and still left the user
// with nothing:
//
//	failPrecheck — a lookup or validation failed before any spend.
//	recordGateRejection — the guard or billing middleware turned it away.
//	recordRefundFailure — stars were held and could not be returned.
//
// The unifying rule is that a user who wanted an image and did not get one
// should leave a row behind, whatever the reason. Only recording upstream
// failures answers "is Gemini healthy?"; recording all of them answers "how
// many people asked for a try-on today and did not get one?", which is the
// question the business actually has.

// failPrecheck responds with a user-safe message and records why, for the
// validation and lookup failures that happen before a generation is attempted.
//
// It replaces a bare utils.RespondError on the try-on paths. The `reason` is a
// stable code, not the message: the copy will be reworded and the code has to
// stay groupable. These rows are cheap and mostly boring, but they are the
// only way to see a class of bug that otherwise looks like nothing at all — a
// dangling wardrobe id, a person deleted on another device, a product whose
// scrape failed weeks ago — because each one is a single user hitting a
// single 404 and never mentioning it.
func failPrecheck(w http.ResponseWriter, r *http.Request, logger *strings.Builder,
	f models.TryOnFailure, reason, userMsg string, status int) {

	userID, _ := GetUserIDFromContext(r.Context())

	f.Stage = "precheck"
	f.UserID = userID
	f.IsGuest = IsGuestFromContext(r.Context())
	f.Reason = reason
	f.RawError = userMsg
	f.HTTPStatus = status
	f.RequestID = utils.RequestIDFromContext(r.Context())
	utils.RecordTryOnFailure(f)

	utils.RespondError(w, logger, userMsg, status)
}

// recordGateRejection notes a try-on that the guard or billing middleware
// turned away: a duplicate already in flight, a user inside the failure-loop
// throttle, or an empty balance.
//
// None of these are faults, which is exactly why they are worth keeping
// separately from the ones that are. `gate` is the bucket that says how often
// the paywall bites and how often people double-tap, and neither number is
// visible anywhere else — a 402 is not an error anybody alerts on, so today it
// leaves no trace at all once the access log rotates.
//
// It is also the highest-volume stage by a distance, which is why
// utils.FailureRetention exists.
func recordGateRejection(r *http.Request, reason string, status int, detail string) {
	userID, _ := GetUserIDFromContext(r.Context())
	tryOnType := tryOnTypeForPath(r.URL.Path)

	utils.RecordTryOnFailure(models.TryOnFailure{
		Stage:      "gate",
		UserID:     userID,
		IsGuest:    IsGuestFromContext(r.Context()),
		Route:      r.URL.Path,
		Type:       tryOnType,
		Quality:    GetQualityFromContext(r.Context()),
		Reason:     reason,
		RawError:   detail,
		HTTPStatus: status,
		RequestID:  utils.RequestIDFromContext(r.Context()),
	})
}

// recordRefundFailure notes stars that were held for a generation and could
// not be given back.
//
// This is the only stage where a row means a specific person is owed a
// specific number of stars. It was previously an ERROR log line reading
// "manual refund may be needed" — accurate, and completely useless a week
// later once the log had rotated, because nothing carried the hold id, the
// amount and the user together into anything queryable.
func recordRefundFailure(r *http.Request, userID string, res utils.Reservation, err error) {
	utils.RecordTryOnFailure(models.TryOnFailure{
		Stage:      "refund",
		UserID:     userID,
		IsGuest:    IsGuestFromContext(r.Context()),
		Route:      r.URL.Path,
		Type:       tryOnTypeForPath(r.URL.Path),
		Quality:    GetQualityFromContext(r.Context()),
		Reason:     "refund_failed",
		RawError:   fmt.Sprintf("hold_id=%s source=%s amount=%d: %v", res.HoldID, res.Source, res.Amount, err),
		HTTPStatus: http.StatusInternalServerError,
		RequestID:  utils.RequestIDFromContext(r.Context()),
	})
}
