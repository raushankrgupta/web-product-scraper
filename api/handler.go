package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/scrapers"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// ScrapeHandler handles the scraping request
func ScrapeHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Scrape API]")

	productURL := r.URL.Query().Get("url")
	if productURL == "" {
		utils.AddToLogMessage(&logMessageBuilder, "URL parameter missing")
		http.Error(w, "Please provide a 'url' query parameter", http.StatusBadRequest)
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

	// Optional: Download images (preserving original functionality)
	// In a real API, we might just return the URLs, but the user asked for "expected output"
	// and the original code downloaded images.
	// We will do it asynchronously or just do it here.
	// For now, let's do it here to match original behavior, but maybe we should just return URLs.
	// The original main.go printed JSON and saved to file.
	// Here we return JSON.

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
	folderName := "product_images"
	urlToPath, err := utils.DownloadImages(dedupedImages, folderName)
	if err != nil {
		fmt.Printf("Error downloading images: %v\n", err)
		// We don't fail the request if image download fails, just log it
	}

	// Update product with local paths if needed.
	// The original code updated the product struct with local paths.
	// If we want to return the local paths in the JSON response, we should update it.

	var localMainImages []string
	for _, img := range product.Images {
		if path, ok := urlToPath[img]; ok {
			localMainImages = append(localMainImages, path)
		} else {
			localMainImages = append(localMainImages, img) // Fallback to URL
		}
	}
	product.Images = localMainImages

	for i := range product.Variants {
		var localVarImages []string
		for _, img := range product.Variants[i].Images {
			if path, ok := urlToPath[img]; ok {
				localVarImages = append(localVarImages, path)
			} else {
				localVarImages = append(localVarImages, img)
			}
		}
		product.Variants[i].Images = localVarImages
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(product); err != nil {
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
		return
	}
}
