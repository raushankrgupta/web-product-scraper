package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
)

// GetThemesHandler fetches active themes from the database for the Daily Try-On carousel
func GetThemesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	themeCollection := utils.GetCollection("fitly", "themes")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Find all active themes
	themeType := r.URL.Query().Get("type")
	filter := bson.M{}
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
	} else if err.Error() != "mongo: no documents in result" {
		utils.RespondError(w, nil, "Failed to fetch themes", http.StatusInternalServerError)
		return
	}

	for i := range themes {
		if themes[i].ThemeImageURL != "" && !strings.HasPrefix(themes[i].ThemeImageURL, "http") {
			if url, err := utils.GetPresignedURL(r.Context(), themes[i].ThemeImageURL); err == nil {
				themes[i].ThemeImageURL = url
			}
		}
		if themes[i].ThemeBlankImageURL != "" && !strings.HasPrefix(themes[i].ThemeBlankImageURL, "http") {
			if url, err := utils.GetPresignedURL(r.Context(), themes[i].ThemeBlankImageURL); err == nil {
				themes[i].ThemeBlankImageURL = url
			}
		}
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"themes": themes,
	})
}
