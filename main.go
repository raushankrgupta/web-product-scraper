package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <amazon_product_url>")
		os.Exit(1)
	}

	url := os.Args[1]
	fmt.Printf("Scraping URL: %s\n", url)

	scraper := NewScraper()
	product, err := scraper.ScrapeProduct(url)
	if err != nil {
		fmt.Printf("Error scraping product: %v\n", err)
		os.Exit(1)
	}

	// Collect all images including variants
	var allImages []string
	allImages = append(allImages, product.Images...)
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

	folderName := "product_images"

	// Download all images map: URL -> LocalPath
	urlToPath, err := downloadImages(dedupedImages, folderName)
	if err != nil {
		fmt.Printf("Error downloading images: %v\n", err)
	}

	// Update product.Images with local paths
	var localMainImages []string
	for _, img := range product.Images {
		if path, ok := urlToPath[img]; ok {
			localMainImages = append(localMainImages, path)
		}
	}
	product.Images = localMainImages

	// Update variants with local paths
	for i := range product.Variants {
		var localVarImages []string
		for _, img := range product.Variants[i].Images {
			if path, ok := urlToPath[img]; ok {
				localVarImages = append(localVarImages, path)
			}
		}
		product.Variants[i].Images = localVarImages
	}

	// Pretty print JSON
	jsonData, err := json.MarshalIndent(product, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(jsonData))

	err = os.WriteFile("output.json", jsonData, 0644)
	if err != nil {
		fmt.Printf("Error writing output file: %v\n", err)
	}
}
