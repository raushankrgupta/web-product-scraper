package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
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
		utils.RespondError(w, &logMessageBuilder, "Please provide a 'url' query parameter or JSON body", http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Scraping URL query: %s", productURL))

	userID, _ := GetUserIDFromContext(r.Context())
	collection := utils.GetCollection(config.DBName, "products")

	saveFailedScrape := func(resolvedURL, scrapeErr string) {
		failedProduct := models.Product{
			ID:          primitive.NewObjectID(),
			UserID:      userID,
			URL:         productURL,
			ResolvedURL: resolvedURL,
			Status:      "failed",
			ScrapeError: scrapeErr,
			Source:      "link",
			CreatedAt:   time.Now(),
		}
		if _, dbErr := collection.InsertOne(r.Context(), failedProduct); dbErr != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save failed scrape record: %v", dbErr))
		} else {
			utils.AddToLogMessage(&logMessageBuilder, "Failed scrape record saved to MongoDB for debugging")
		}
	}

	// GetScraper returns the scraper and the resolved URL (e.g. after following short links)
	scraper, resolvedURL, err := scrapers.GetScraper(productURL)
	if err != nil {
		saveFailedScrape("", fmt.Sprintf("scraper_not_found: %v", err))
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Error finding scraper: %v", err), http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Resolved URL: %s", resolvedURL))

	product, err := scraper.ScrapeProduct(resolvedURL)
	if err != nil {
		saveFailedScrape(resolvedURL, fmt.Sprintf("scrape_failed: %v", err))
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
	}

	var localMainKeys []string
	for _, img := range product.Images {
		if key, ok := urlToKey[img]; ok {
			localMainKeys = append(localMainKeys, key)
		} else {
			localMainKeys = append(localMainKeys, img)
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

	// Save to MongoDB
	product.ID = primitive.NewObjectID()
	product.UserID = userID
	product.URL = productURL
	product.ResolvedURL = resolvedURL
	product.Status = "success"
	product.CreatedAt = time.Now()

	_, err = collection.InsertOne(r.Context(), product)
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
