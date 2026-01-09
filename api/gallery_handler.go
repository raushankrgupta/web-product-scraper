package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GalleryResponse represents the response structure for the gallery API
type GalleryResponse struct {
	Images      []models.TryOn `json:"images"`
	Total       int64          `json:"total"`
	CurrentPage int            `json:"current_page"`
	TotalPages  int            `json:"total_pages"`
}

// GalleryHandler handles fetching the user's generated images
func GalleryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Get User ID from Context
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Parse Pagination Parameters
	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")

	page := 1
	limit := 10

	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	// 3. Query Database
	collection := utils.GetCollection("fitly", "tryons")

	filter := bson.M{"user_id": userID, "status": "completed"}

	// Count total documents for pagination
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		http.Error(w, "Failed to fetch data", http.StatusInternalServerError)
		return
	}

	// Calculate skip
	skip := (page - 1) * limit

	findOptions := options.Find()
	findOptions.SetSort(bson.D{{Key: "created_at", Value: -1}}) // Show latest first
	findOptions.SetSkip(int64(skip))
	findOptions.SetLimit(int64(limit))

	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		http.Error(w, "Failed to fetch data", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var tryOns []models.TryOn
	if err = cursor.All(ctx, &tryOns); err != nil {
		http.Error(w, "Failed to decode data", http.StatusInternalServerError)
		return
	}

	// 4. Generate Presigned URLs for images
	for i := range tryOns {
		// Generated Image
		if tryOns[i].GeneratedImageURL != "" {
			presignedURL, err := utils.GetPresignedURL(r.Context(), tryOns[i].GeneratedImageURL)
			if err == nil {
				tryOns[i].GeneratedImageURL = presignedURL
			}
		}

		// Also update PersonImageURL if it's stored as a key
		if tryOns[i].PersonImageURL != "" {
			presignedURL, err := utils.GetPresignedURL(r.Context(), tryOns[i].PersonImageURL)
			if err == nil {
				tryOns[i].PersonImageURL = presignedURL
			}
		}
	}

	// Ensure empty slice is returned as [] instead of null
	if tryOns == nil {
		tryOns = []models.TryOn{}
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	// 5. Return Response
	response := GalleryResponse{
		Images:      tryOns,
		Total:       total,
		CurrentPage: page,
		TotalPages:  totalPages,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
