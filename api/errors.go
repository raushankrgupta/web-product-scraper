package api

import (
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/utils"
)

// classifyGenErr maps an image-generation failure onto an HTTP status and a
// user-facing message.
//
// Two things this fixes, both visible in the production log:
//
//  1. /try-on mapped quota errors to 429 while processMultiPersonTryOn mapped
//     *everything* to 500, so the mobile app could not build one error path.
//  2. The raw upstream error was concatenated into the response body, which
//     is how users ended up reading a Google AI Studio billing URL. The
//     detail belongs in the log and the alert; the client gets the friendly
//     string.
func classifyGenErr(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	s := strings.ToLower(err.Error())

	switch {
	case strings.Contains(s, "circuit open"), strings.Contains(s, "temporarily unavailable"):
		return http.StatusServiceUnavailable, "Try-on is temporarily unavailable. We're on it — please try again shortly."

	case utils.IsQuotaError(err):
		return http.StatusTooManyRequests, "Try-on is temporarily unavailable. Please try again shortly."

	case strings.Contains(s, "context deadline exceeded"),
		strings.Contains(s, "context canceled"),
		strings.Contains(s, "timeout"):
		return http.StatusGatewayTimeout, "Generation is taking longer than usual. Please try again."

	// The image-side refusals (finish_reason IMAGE_*) are the model declining
	// to draw a particular person or product, and they are sticky: the same
	// photo is refused every time. Saying "try a different photo or garment"
	// sends people round the same loop with the same inputs, so name the photo
	// — in practice it is the person image the model will not reproduce.
	case strings.Contains(s, "image_safety"),
		strings.Contains(s, "image_prohibited_content"),
		strings.Contains(s, "image_other"),
		strings.Contains(s, "image_recitation"):
		return http.StatusUnprocessableEntity, "The AI wouldn't generate a look from this photo. Please try a different photo of the person."

	case strings.Contains(s, "blocked"),
		strings.Contains(s, "no content generated"),
		strings.Contains(s, "returned text instead of an image"),
		strings.Contains(s, "safety"):
		return http.StatusUnprocessableEntity, "We couldn't generate this look. Try a different photo or garment."

	case strings.Contains(s, "not enough images"):
		return http.StatusBadRequest, "We couldn't load the photos for this try-on. Please re-add the item and try again."

	default:
		return http.StatusInternalServerError, "Failed to generate try-on image."
	}
}
