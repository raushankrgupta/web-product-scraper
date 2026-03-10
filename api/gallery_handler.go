package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
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
// GalleryHandler handles fetching the user's generated images
func GalleryHandler(w http.ResponseWriter, r *http.Request) {
	// Check for POST sub-routes e.g., /gallery/:id/favorite
	if r.Method == http.MethodPost {
		pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// expected: ["gallery", "{id}", "action"]
		if len(pathParts) >= 3 {
			action := pathParts[2]
			switch action {
			case "favorite":
				toggleFavorite(w, r)
				return
			case "save":
				markSaved(w, r)
				return
			case "feedback":
				submitTryonFeedback(w, r)
				return
			}
		}
	}

	switch r.Method {
	case http.MethodGet:
		getGallery(w, r)
	case http.MethodDelete:
		deleteGalleryPhoto(w, r)
	default:
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getGallery handles fetching the user's generated images
func getGallery(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Get Gallery API]")

	// 1. Get User ID from Context
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID in context", http.StatusUnauthorized)
		return
	}

	// 2. Parse Pagination and Fillter Parameters
	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")
	filterParam := r.URL.Query().Get("filter")

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
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Fetching gallery. Page: %d, Limit: %d, Filter: %s", page, limit, filterParam))
	collection := utils.GetCollection("fitly", "tryons")

	// Filter now includes is_deleted check
	filter := bson.M{
		"user_id":    userID,
		"status":     "completed",
		"is_deleted": bson.M{"$ne": true},
	}

	if filterParam == "saved" {
		filter["is_saved"] = true
	} else if filterParam == "favorites" {
		filter["is_favorite"] = true
	} else if filterParam == "recent" {
		sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour)
		filter["created_at"] = bson.M{"$gte": sevenDaysAgo}
	}

	// Count total documents for pagination
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to count documents", http.StatusInternalServerError)
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
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Database query failed: %v", err), http.StatusInternalServerError)
		return
	}

	defer cursor.Close(ctx)

	var tryOns []models.TryOn
	if err = cursor.All(ctx, &tryOns); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to decode data", http.StatusInternalServerError)
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

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Found %d images", len(tryOns)))
	utils.RespondJSON(w, http.StatusOK, response)
}

// deleteGalleryPhoto handles soft deleting a photo
func deleteGalleryPhoto(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Delete Gallery Photo API]")

	// 1. Get User ID
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID in context", http.StatusUnauthorized)
		return
	}

	// 2. Get Photo ID from Path
	// Expected path: /gallery/{id} -> ["", "gallery", "id"]
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 3 || pathParts[2] == "" {
		utils.RespondError(w, &logMessageBuilder, "Photo ID required", http.StatusBadRequest)
		return
	}
	photoID, err := primitive.ObjectIDFromHex(pathParts[2])
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Photo ID", http.StatusBadRequest)
		return
	}

	// 3. Perform Soft Delete
	collection := utils.GetCollection("fitly", "tryons")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{
		"_id":     photoID,
		"user_id": userID,
	}
	update := bson.M{
		"$set": bson.M{"is_deleted": true},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to delete photo", http.StatusInternalServerError)
		return
	}

	if result.MatchedCount == 0 {
		utils.RespondError(w, &logMessageBuilder, "Photo not found or unauthorized", http.StatusNotFound)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Soft deleted photo: %s", photoID.Hex()))
	w.WriteHeader(http.StatusNoContent)
}

func toggleFavorite(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { fmt.Println(logMessageBuilder.String()) }()

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		utils.RespondError(w, &logMessageBuilder, "Photo ID required", http.StatusBadRequest)
		return
	}
	photoID, err := primitive.ObjectIDFromHex(pathParts[1])
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Photo ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "tryons")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var tryon models.TryOn
	err = collection.FindOne(ctx, bson.M{"_id": photoID, "user_id": userID}).Decode(&tryon)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Photo not found", http.StatusNotFound)
		return
	}

	newStatus := !tryon.IsFavorite
	_, err = collection.UpdateOne(ctx, bson.M{"_id": photoID}, bson.M{"$set": bson.M{"is_favorite": newStatus}})
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update favorite status", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"is_favorite": newStatus,
	})
}

func markSaved(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { fmt.Println(logMessageBuilder.String()) }()

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		utils.RespondError(w, &logMessageBuilder, "Photo ID required", http.StatusBadRequest)
		return
	}
	photoID, err := primitive.ObjectIDFromHex(pathParts[1])
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Photo ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "tryons")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = collection.UpdateOne(ctx, bson.M{"_id": photoID, "user_id": userID}, bson.M{"$set": bson.M{"is_saved": true}})
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update saved status", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"is_saved": true,
	})
}

type TryonFeedbackRequest struct {
	Rating  int    `json:"rating"`
	Comment string `json:"comment"`
}

func submitTryonFeedback(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { fmt.Println(logMessageBuilder.String()) }()

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		utils.RespondError(w, &logMessageBuilder, "Photo ID required", http.StatusBadRequest)
		return
	}
	photoID, err := primitive.ObjectIDFromHex(pathParts[1])
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Photo ID", http.StatusBadRequest)
		return
	}

	var req TryonFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "tryons")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = collection.UpdateOne(ctx, bson.M{"_id": photoID, "user_id": userID}, bson.M{"$set": bson.M{"rating": req.Rating, "comment": req.Comment}})
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to submit feedback", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Feedback submitted successfully",
	})
}
