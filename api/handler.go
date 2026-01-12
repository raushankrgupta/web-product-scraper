package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/scrapers"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ScrapeHandler handles the scraping request
// ScrapeHandler handles the scraping request
func ScrapeHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Scrape API]")

	// Support both Query Params and JSON Body
	productURL := r.URL.Query().Get("url")
	if productURL == "" {
		// Try JSON body
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			productURL = req.URL
		}
	}

	if productURL == "" {
		utils.RespondError(w, &logMessageBuilder, "Please provide a 'url' query parameter or JSON body", http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Scraping URL: %s", productURL))

	scraper, err := scrapers.GetScraper(productURL)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Error finding scraper: %v", err), http.StatusBadRequest)
		return
	}

	product, err := scraper.ScrapeProduct(productURL)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Scraping failed: %v", err), http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Scraping successful")

	// Collect all images
	var allImages []string
	allImages = append(allImages, product.Images...)
	if product.CurrentSelection != nil {
		allImages = append(allImages, product.CurrentSelection.Images...)
	}
	for _, v := range product.Variants {
		allImages = append(allImages, v.Images...)
	}

	// Deduplicate
	uniqueImages := make(map[string]bool)
	var dedupedImages []string
	for _, img := range allImages {
		if _, exists := uniqueImages[img]; !exists {
			uniqueImages[img] = true
			dedupedImages = append(dedupedImages, img)
		}
	}

	// Upload images to S3
	folderName := "product_images"
	urlToKey, err := utils.UploadImagesToS3(r.Context(), dedupedImages, folderName)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error uploading images: %v", err))
		// We continue even if some uploads fail, relying on what succeeded or original URLs
	}

	// Update product with S3 keys (stored in database)
	// We map the scraped image URLs to the S3 keys we just got.
	var localMainKeys []string
	for _, img := range product.Images {
		if key, ok := urlToKey[img]; ok {
			localMainKeys = append(localMainKeys, key)
		} else {
			localMainKeys = append(localMainKeys, img) // Fallback
		}
	}
	product.Images = localMainKeys

	for i := range product.Variants {
		var localVarKeys []string
		for _, img := range product.Variants[i].Images {
			if key, ok := urlToKey[img]; ok {
				localVarKeys = append(localVarKeys, key)
			} else {
				localVarKeys = append(localVarKeys, img)
			}
		}
		product.Variants[i].Images = localVarKeys
	}

	// Capture UserID
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Warning: UserID not found in context")
	}

	// Save to MongoDB
	product.ID = primitive.NewObjectID()
	product.UserID = userID
	product.URL = productURL
	product.CreatedAt = time.Now()

	collection := utils.GetCollection("fitly", "products")
	ctx := r.Context()
	_, err = collection.InsertOne(ctx, product)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save product to MongoDB: %v", err))
	} else {
		utils.AddToLogMessage(&logMessageBuilder, "Product saved to MongoDB")
	}

	// Generate Presigned URLs for response
	product.Images = utils.PresignImageURLs(r.Context(), product.Images)
	for i := range product.Variants {
		product.Variants[i].Images = utils.PresignImageURLs(r.Context(), product.Variants[i].Images)
	}

	utils.RespondJSON(w, http.StatusOK, product)
}
