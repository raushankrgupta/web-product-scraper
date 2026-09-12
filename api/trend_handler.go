package api

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// trendListMaxAge is how long a client and a shared cache may hold the list.
//
// Five minutes, matching /app/config: a trend published now should be on the
// home screen within a coffee break, and the payload is identical for every
// user, so it is worth caching publicly rather than per-account.
const trendListMaxAge = 300 * time.Second

// TrendsHandler serves GET /trends and GET /trends/{id}.
//
// Unauthenticated on purpose. The list is the same for everyone, it carries
// nothing private, and the home screen fetches it on cold start — putting it
// behind auth would cost the shared cache and buy nothing, since the prompts
// (the only part worth protecting) never leave the server.
func TrendsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/trends")
	id = strings.Trim(id, "/")

	var payload interface{}
	if id != "" {
		trend, ok := findTrend(r.Context(), id)
		if !ok || !trendVisibleNow(trend, time.Now()) {
			utils.RespondError(w, nil, "That trend is no longer available", http.StatusNotFound)
			return
		}
		normaliseTrendForClient(&trend)
		presignTrend(r.Context(), &trend)
		payload = map[string]interface{}{"trend": trend}
	} else {
		// Deep-copied before presigning: activeTrends hands back the cached
		// slice itself, and signing in place would rewrite the cache's S3
		// keys into URLs that later expire. See copyTrendsForResponse — a
		// shallow copy is not enough, because the nested slices still share
		// the cache's backing arrays.
		listed := copyTrendsForResponse(activeTrends(r.Context()))
		for i := range listed {
			normaliseTrendForClient(&listed[i])
			presignTrend(r.Context(), &listed[i])
		}
		payload = map[string]interface{}{"trends": listed, "schema": 1}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		utils.RespondError(w, nil, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	etag := fmt.Sprintf(`"%x"`, md5.Sum(data))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(trendListMaxAge.Seconds())))
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
	w.Write([]byte("\n"))
}

// maxTrendUpload bounds one staged photo. The client resizes to 1280px before
// sending, so this is generous for anything it can legitimately produce.
const maxTrendUpload = 12 << 20

// TrendUploadHandler serves POST /trends/uploads.
//
// One image per call, returning an id the generate request refers to. A
// separate step rather than fields on the generate request, so that the
// generate body stays JSON — which is what lets it keep the body-fingerprint
// idempotency the billing layer relies on — and so a flaky photo upload
// cannot cost a star hold.
func TrendUploadHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { utils.FlushLog(r.Context(), &logMessageBuilder) }()
	utils.AddToLogMessage(&logMessageBuilder, "[Trend Upload]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if IsGuestFromContext(r.Context()) {
		// A staged upload is durable state attached to an account, and a
		// guest token is not an account — the object would be orphaned in S3
		// the moment the token expired.
		utils.RespondErrorReason(w, &logMessageBuilder, "Sign in to use your own photos here",
			"guest_not_eligible", http.StatusForbidden)
		return
	}

	if err := r.ParseMultipartForm(maxTrendUpload); err != nil {
		utils.RespondErrorReason(w, &logMessageBuilder,
			"That image is too large. Please pick a smaller one.",
			"file_too_large", http.StatusRequestEntityTooLarge)
		return
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		utils.RespondErrorReason(w, &logMessageBuilder, "No image uploaded",
			"missing_image", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Magic bytes, not the declared Content-Type: the header is whatever the
	// client felt like sending.
	objectKey, mime, err := utils.ValidateImageFile(file, "trend_inputs/"+userID)
	if err != nil {
		utils.RespondErrorReason(w, &logMessageBuilder,
			"That file isn't an image we can use. Try a JPEG or PNG.",
			"not_an_image", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	if _, err := utils.UploadFileToS3(ctx, file, objectKey, mime); err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "s3",
			"We couldn't save that photo. Please try again.", err, http.StatusInternalServerError)
		return
	}

	expires := time.Now().Add(models.TrendUploadTTL)
	upload := models.TrendUpload{
		ID:        primitive.NewObjectID(),
		UserID:    userID,
		ObjectKey: objectKey,
		Mime:      mime,
		ExpiresAt: &expires,
		CreatedAt: time.Now(),
	}
	if _, err := utils.GetCollection(config.DBName, models.CollTrendUploads).InsertOne(ctx, upload); err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
			"We couldn't save that photo. Please try again.", err, http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder,
		fmt.Sprintf("Staged upload OK: key=%s mime=%s", objectKey, mime))

	utils.RespondJSON(w, http.StatusCreated, map[string]interface{}{
		"upload_id":  upload.ID.Hex(),
		"expires_at": expires,
	})
}

// TrendUploadRoute wires the upload endpoint behind auth.
func TrendUploadRoute() http.Handler {
	return AuthMiddleware(http.HandlerFunc(TrendUploadHandler))
}
