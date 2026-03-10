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

// WardrobeResponse represents the response structure for the wardrobe API
type WardrobeResponse struct {
	Items       []models.WardrobeItem `json:"items"`
	Total       int64                 `json:"total"`
	CurrentPage int                   `json:"current_page"`
	TotalPages  int                   `json:"total_pages"`
}

// SaveProductRequest represents the payload for saving a product
type SaveProductRequest struct {
	Category  string   `json:"category"`
	Images    []string `json:"images"`
	SourceURL string   `json:"source_url,omitempty"`
}

type UpdateProductRequest struct {
	Category string `json:"category"`
}

// WardrobeHandler handles requests to /wardrobe
func WardrobeHandler(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	if len(pathParts) > 1 {
		// e.g. /wardrobe/:id or /wardrobe/:id/favorite
		itemIDHex := pathParts[1]

		if len(pathParts) > 2 && pathParts[2] == "favorite" {
			if r.Method == http.MethodPost {
				toggleWardrobeFavorite(w, r, itemIDHex)
				return
			}
		} else {
			if r.Method == http.MethodDelete {
				removeProduct(w, r, itemIDHex)
				return
			} else if r.Method == http.MethodPut {
				updateProductCategory(w, r, itemIDHex)
				return
			}
		}
	} else {
		// e.g. /wardrobe
		switch r.Method {
		case http.MethodGet:
			getWardrobe(w, r)
			return
		case http.MethodPost:
			saveProduct(w, r)
			return
		}
	}

	utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
}

// getWardrobe handles fetching the user's saved products
func getWardrobe(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Get Wardrobe API]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")
	categoryFilter := r.URL.Query().Get("category")

	page := 1
	limit := 10

	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
		limit = l
	}

	collection := utils.GetCollection("fitly", "wardrobe")

	filter := bson.M{"user_id": userID}
	if categoryFilter != "" && categoryFilter != "All" {
		filter["category"] = categoryFilter
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to count documents", http.StatusInternalServerError)
		return
	}

	skip := (page - 1) * limit
	findOptions := options.Find().SetSort(bson.D{{Key: "saved_at", Value: -1}}).SetSkip(int64(skip)).SetLimit(int64(limit))

	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Database query failed", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var items []models.WardrobeItem
	if err = cursor.All(ctx, &items); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to decode data", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []models.WardrobeItem{}
	}

	// Presign product images if they are S3 keys
	for i := range items {
		for j, img := range items[i].Images {
			if img != "" && !strings.HasPrefix(img, "http") {
				presigned, err := utils.GetPresignedURL(ctx, img)
				if err == nil {
					items[i].Images[j] = presigned
				}
			}
		}
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	response := WardrobeResponse{
		Items:       items,
		Total:       total,
		CurrentPage: page,
		TotalPages:  totalPages,
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Found %d items", len(items)))
	utils.RespondJSON(w, http.StatusOK, response)
}

// saveProduct handles saving a product to the wardrobe
func saveProduct(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Save Wardrobe Product API]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req SaveProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Images) == 0 {
		utils.RespondError(w, &logMessageBuilder, "Images are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wardrobeCollection := utils.GetCollection("fitly", "wardrobe")

	wardrobeItem := models.WardrobeItem{
		ID:         primitive.NewObjectID(),
		UserID:     userID,
		Category:   req.Category,
		Images:     req.Images,
		SourceURL:  req.SourceURL,
		IsFavorite: false,
		SavedAt:    time.Now(),
	}

	_, err = wardrobeCollection.InsertOne(ctx, wardrobeItem)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to save product", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Product saved to wardrobe")
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Product saved successfully",
		"item":    wardrobeItem,
	})
}

// removeProduct handles removing a product from the wardrobe
func removeProduct(w http.ResponseWriter, r *http.Request, itemIDHex string) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Remove Wardrobe Product API]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	itemID, err := primitive.ObjectIDFromHex(itemIDHex)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Item ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "wardrobe")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := collection.DeleteOne(ctx, bson.M{"_id": itemID, "user_id": userID})
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to remove product", http.StatusInternalServerError)
		return
	}

	if res.DeletedCount == 0 {
		utils.RespondError(w, &logMessageBuilder, "Item not found or unauthorized", http.StatusNotFound)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Product removed from wardrobe")
	w.WriteHeader(http.StatusNoContent)
}

// updateProductCategory handles editing a wardrobe item's category
func updateProductCategory(w http.ResponseWriter, r *http.Request, itemIDHex string) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Update Wardrobe Category API]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	itemID, err := primitive.ObjectIDFromHex(itemIDHex)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Item ID", http.StatusBadRequest)
		return
	}

	var req UpdateProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "wardrobe")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	update := bson.M{"$set": bson.M{"category": req.Category}}
	res, err := collection.UpdateOne(ctx, bson.M{"_id": itemID, "user_id": userID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update category", http.StatusInternalServerError)
		return
	}

	if res.MatchedCount == 0 {
		utils.RespondError(w, &logMessageBuilder, "Item not found or unauthorized", http.StatusNotFound)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message":  "Category updated successfully",
		"category": req.Category,
	})
}

// toggleWardrobeFavorite handles toggling the is_favorite state of a wardrobe item
func toggleWardrobeFavorite(w http.ResponseWriter, r *http.Request, itemIDHex string) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Toggle Wardrobe Favorite API]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	itemID, err := primitive.ObjectIDFromHex(itemIDHex)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid Item ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "wardrobe")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var item models.WardrobeItem
	err = collection.FindOne(ctx, bson.M{"_id": itemID, "user_id": userID}).Decode(&item)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Item not found or unauthorized", http.StatusNotFound)
		return
	}

	newStatus := !item.IsFavorite
	update := bson.M{"$set": bson.M{"is_favorite": newStatus}}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": itemID, "user_id": userID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update favorite status", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message":     "Favorite status updated",
		"is_favorite": newStatus,
	})
}
