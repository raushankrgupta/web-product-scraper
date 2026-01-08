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
		utils.AddToLogMessage(&logMessageBuilder, "URL parameter missing")
		http.Error(w, "Please provide a 'url' query parameter or JSON body", http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Scraping URL: %s", productURL))

	scraper, err := scrapers.GetScraper(productURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error finding scraper: %v", err), http.StatusBadRequest)
		return
	}

	product, err := scraper.ScrapeProduct(productURL)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Scraping failed: %v", err))
		http.Error(w, fmt.Sprintf("Scraping failed: %v", err), http.StatusInternalServerError)
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

	// Download images
	// Upload images to S3
	folderName := "product_images"
	urlToKey, err := utils.UploadImagesToS3(r.Context(), dedupedImages, folderName)
	if err != nil {
		fmt.Printf("Error uploading images: %v\n", err)
	}

	// Update product with S3 keys (stored in database)
	// We also want to return Presigned URLs in response, so we'll need a way to map Key -> URL.
	// For simplicity, we update the product struct to have Keys for DB,
	// AND we clone or modifying it again for response?
	// The variable 'product' is what is saved AND returned.
	// If we save URLs (presigned), they will expire. We MUST save KEYS in DB.
	// So:
	// 1. Update 'product' to have KEYS.
	// 2. Save 'product' to DB.
	// 3. Update 'product' (in memory) to have PRESIGNED URLs.
	// 4. Return 'product'.

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
		// Log error but proceed? Or fail?
		// Since route is protected now, this should theoretically not happen if middleware works.
		// But let's handle it safely.
		utils.AddToLogMessage(&logMessageBuilder, "Warning: UserID not found in context")
	}

	// Save to MongoDB
	product.ID = primitive.NewObjectID()
	product.UserID = userID
	product.CreatedAt = time.Now()

	collection := utils.GetCollection("fitly", "products")
	ctx := r.Context()
	_, err = collection.InsertOne(ctx, product)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save product to MongoDB: %v", err))
		// We might still want to return the product even if DB save fails, or error out.
		// Let's log and proceed for now, or maybe add a warning to response.
	} else {
		utils.AddToLogMessage(&logMessageBuilder, "Product saved to MongoDB")
	}

	// Generate Presigned URLs for response
	// Note: We modifying 'product' in place, which is fine since it's already saved.

	// Process Main Images
	var presignedMainURLs []string
	for _, key := range product.Images {
		if url, err := utils.GetPresignedURL(r.Context(), key); err == nil {
			presignedMainURLs = append(presignedMainURLs, url)
		} else {
			presignedMainURLs = append(presignedMainURLs, key)
		}
	}
	product.Images = presignedMainURLs

	// Process Variant Images
	for i := range product.Variants {
		var presignedVarURLs []string
		for _, key := range product.Variants[i].Images {
			if url, err := utils.GetPresignedURL(r.Context(), key); err == nil {
				presignedVarURLs = append(presignedVarURLs, url)
			} else {
				presignedVarURLs = append(presignedVarURLs, key)
			}
		}
		product.Variants[i].Images = presignedVarURLs
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(product); err != nil {
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
		return
	}
}
