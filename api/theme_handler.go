package api

import (
	"context"
	"net/http"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// GetThemesHandler fetches active themes from the database for the Daily Try-On carousel
func GetThemesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	themeCollection := utils.GetCollection(config.DBName, "themes")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Find all active themes
	themeType := r.URL.Query().Get("type")
	// Curated themes only. This response is served through
	// ImageCacheMiddleware with a *public* Cache-Control, so anything
	// user-specific that reached it would be cached and handed to the next
	// caller. `user_id: {$exists: false}` is what keeps uploaded backgrounds
	// out — they are served from GET /themes/custom, behind auth and uncached.
	filter := bson.M{"user_id": bson.M{"$exists": false}}
	if themeType != "" {
		filter["type"] = themeType
	}
	cursor, err := themeCollection.Find(ctx, filter)
	var themes []models.Theme

	if err == nil {
		if err = cursor.All(ctx, &themes); err != nil {
			utils.RespondError(w, nil, "Failed to decode themes", http.StatusInternalServerError)
			return
		}
	} else if err != mongo.ErrNoDocuments {
		utils.RespondError(w, nil, "Failed to fetch themes", http.StatusInternalServerError)
		return
	}

	// Signed for PresignCatalog, not the default hour: this response is
	// cached by the client, and a signature shorter than that cache is what
	// left the theme grid rendering blank tiles an hour after first load.
	presignThemes(r.Context(), themes)

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"themes": themes,
	})
}
