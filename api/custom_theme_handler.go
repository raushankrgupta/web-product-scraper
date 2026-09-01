package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Custom backgrounds: a user's own photo used as the try-on scene.
//
// These are Theme documents scoped by user_id, which is what makes them work
// end to end without touching generation: the try-on already hands
// theme_blank_image_url to the model as "use this as the background
// environment", so an uploaded image is a background the moment it is stored
// in that field.
//
// Kept on separate routes from GET /themes rather than folded into it,
// because that endpoint is deliberately public and shared-cacheable. Merging
// private data into a response with `Cache-Control: public` is how one user's
// upload gets served to another.

// maxCustomThemeUpload bounds a single upload. Backgrounds are scene photos,
// not scans; 12MB is generous for anything a phone camera produces after the
// client's own resize.
const maxCustomThemeUpload = 12 << 20

func customThemes() *mongo.Collection { return utils.GetCollection(config.DBName, "themes") }

// CustomThemeRoute dispatches the three verbs on /themes/custom.
//
// One route rather than three because expo-router's client sends the theme id
// as a query parameter on delete; a path-parameter split would buy nothing.
func CustomThemeRoute() http.Handler {
	return AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listCustomThemes(w, r)
		case http.MethodPost:
			uploadCustomTheme(w, r)
		case http.MethodDelete:
			deleteCustomTheme(w, r)
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
}

// customThemeCaller resolves the caller, rejecting guests.
//
// Guests are refused because a custom background is durable state attached to
// an account, and a guest token is not an account — the upload would be
// orphaned in S3 the moment the token expired.
func customThemeCaller(w http.ResponseWriter, r *http.Request) (primitive.ObjectID, bool) {
	userIDStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return primitive.NilObjectID, false
	}
	if IsGuestFromContext(r.Context()) {
		utils.RespondErrorReason(w, nil, "Sign in to use your own backgrounds",
			"guest_not_eligible", http.StatusForbidden)
		return primitive.NilObjectID, false
	}
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return primitive.NilObjectID, false
	}
	return userID, true
}

// listCustomThemes returns the caller's own backgrounds, newest first.
func listCustomThemes(w http.ResponseWriter, r *http.Request) {
	userID, ok := customThemeCaller(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"user_id": userID, "is_deleted": bson.M{"$ne": true}}
	if t := strings.TrimSpace(r.URL.Query().Get("type")); t != "" {
		// A background uploaded without a type is usable everywhere, so it has
		// to survive a filtered fetch — otherwise it would vanish from the
		// couple picker for no reason the user could see.
		filter["$or"] = []bson.M{
			{"type": t},
			{"type": ""},
			{"type": bson.M{"$exists": false}},
		}
	}

	cursor, err := customThemes().Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		utils.RespondInternalError(w, r, nil, "mongo",
			"Couldn't load your backgrounds", err, http.StatusInternalServerError)
		return
	}

	var themes []models.Theme
	if err := cursor.All(ctx, &themes); err != nil {
		utils.RespondInternalError(w, r, nil, "mongo",
			"Couldn't load your backgrounds", err, http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"themes": presignThemes(r.Context(), themes),
		"limit":  models.CustomThemeUploadLimit,
	})
}

// uploadCustomTheme stores one image and registers it as a theme.
func uploadCustomTheme(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { utils.FlushLog(r.Context(), &logMessageBuilder) }()
	utils.AddToLogMessage(&logMessageBuilder, "[Custom Theme Upload]")

	userID, ok := customThemeCaller(w, r)
	if !ok {
		return
	}

	if err := r.ParseMultipartForm(maxCustomThemeUpload); err != nil {
		utils.RespondErrorReason(w, &logMessageBuilder,
			"That image is too large. Please pick a smaller one.",
			"file_too_large", http.StatusRequestEntityTooLarge)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		utils.RespondErrorReason(w, &logMessageBuilder, "No image uploaded",
			"missing_image", http.StatusBadRequest)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		utils.RespondErrorReason(w, &logMessageBuilder,
			"That file isn't an image.", "not_an_image", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The cap is checked before the upload, not after: rejecting a user once
	// their image is already sitting in S3 leaves us paying for a file nobody
	// can see.
	count, err := customThemes().CountDocuments(ctx, bson.M{
		"user_id": userID, "is_deleted": bson.M{"$ne": true},
	})
	if err == nil && count >= int64(models.CustomThemeUploadLimit) {
		utils.RespondErrorReason(w, &logMessageBuilder,
			fmt.Sprintf("You can keep up to %d backgrounds. Delete one to add another.",
				models.CustomThemeUploadLimit),
			"limit_reached", http.StatusConflict)
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" || len(ext) > 5 {
		ext = ".jpg"
	}
	// Keyed under the user id so an account's uploads are one prefix — which
	// is what makes them findable, and deletable, without a database scan.
	objectKey := fmt.Sprintf("custom_themes/%s/%d%s", userID.Hex(), time.Now().UnixNano(), ext)

	if _, err := utils.UploadFileToS3(r.Context(), file, objectKey, contentType); err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "s3",
			"We couldn't save that background. Please try again.", err,
			http.StatusInternalServerError)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = "My background"
	}
	if len(title) > 60 {
		title = title[:60]
	}

	theme := models.Theme{
		Title: title,
		// Description feeds the text prompt. Left empty deliberately: the
		// image itself is the instruction, and inventing a description of a
		// photo we haven't looked at would fight with what the model sees.
		Description: strings.TrimSpace(r.FormValue("description")),
		// Both fields point at the same object. ThemeBlankImageURL is the one
		// generation reads as the background reference; ThemeImageURL is what
		// the picker renders as the tile.
		ThemeImageURL:      objectKey,
		ThemeBlankImageURL: objectKey,
		Type:               strings.TrimSpace(r.FormValue("type")),
		CreatedAt:          time.Now(),
		IsActive:           true,
		UserID:             userID,
		IsCustom:           true,
	}

	result, err := customThemes().InsertOne(ctx, theme)
	if err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
			"We couldn't save that background. Please try again.", err,
			http.StatusInternalServerError)
		return
	}
	theme.ID = result.InsertedID.(primitive.ObjectID)

	utils.AddToLogMessage(&logMessageBuilder,
		fmt.Sprintf("custom theme saved: %s", theme.ID.Hex()))

	utils.RespondJSON(w, http.StatusCreated, map[string]interface{}{
		"theme": presignThemes(r.Context(), []models.Theme{theme})[0],
	})
}

// deleteCustomTheme soft-deletes one of the caller's backgrounds.
func deleteCustomTheme(w http.ResponseWriter, r *http.Request) {
	userID, ok := customThemeCaller(w, r)
	if !ok {
		return
	}

	id := strings.TrimSpace(r.URL.Query().Get("id"))
	themeID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		utils.RespondErrorReason(w, nil, "Invalid theme id", "bad_request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// user_id is part of the filter, not checked afterwards: an ownership test
	// that runs after the write has already happened isn't an ownership test.
	res, err := customThemes().UpdateOne(ctx,
		bson.M{"_id": themeID, "user_id": userID},
		bson.M{"$set": bson.M{"is_deleted": true, "is_active": false}},
	)
	if err != nil {
		utils.RespondInternalError(w, r, nil, "mongo",
			"Couldn't remove that background", err, http.StatusInternalServerError)
		return
	}
	if res.MatchedCount == 0 {
		utils.RespondError(w, nil, "Background not found", http.StatusNotFound)
		return
	}

	// The S3 object is deliberately left in place. Past try-on records point
	// at this theme, and the generated images are already stored separately —
	// deleting the source would break history to save a few kilobytes.
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Background removed",
	})
}

// presignThemes swaps stored object keys for URLs the client can actually
// load. Keys that are already absolute URLs are left alone — seeded themes
// sometimes point at external images.
func presignThemes(ctx context.Context, themes []models.Theme) []models.Theme {
	for i := range themes {
		if themes[i].ThemeImageURL != "" && !strings.HasPrefix(themes[i].ThemeImageURL, "http") {
			if url, err := utils.GetPresignedURLWithExpiry(ctx, themes[i].ThemeImageURL, utils.PresignCatalog); err == nil {
				themes[i].ThemeImageURL = url
			}
		}
		if themes[i].ThemeBlankImageURL != "" && !strings.HasPrefix(themes[i].ThemeBlankImageURL, "http") {
			if url, err := utils.GetPresignedURLWithExpiry(ctx, themes[i].ThemeBlankImageURL, utils.PresignCatalog); err == nil {
				themes[i].ThemeBlankImageURL = url
			}
		}
	}
	return themes
}
