package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TryOnRequest represents the request body for virtual try-on
type TryOnRequest struct {
	ProductURL string `json:"product_url"`
	PersonID   string `json:"person_id"`
}

// VirtualTryOnHandler handles the virtual try-on request
func VirtualTryOnHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Virtual Try-On API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TryOnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ProductURL == "" || req.PersonID == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Missing product_url or person_id")
		http.Error(w, "product_url and person_id are required", http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Try-On Request: PersonID=%s, URL=%s", req.PersonID, req.ProductURL))

	// 1. Fetch Person Data
	objID, err := primitive.ObjectIDFromHex(req.PersonID)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid person ID")
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "person")
	var person models.Person
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&person)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Person not found: %v", err))
		http.Error(w, "Person not found", http.StatusNotFound)
		return
	}

	if len(person.ImagePaths) == 0 {
		utils.AddToLogMessage(&logMessageBuilder, "Person has no images")
		http.Error(w, "Person has no images", http.StatusBadRequest)
		return
	}

	// 2. Scrape Product Data
	scraper, err := scrapers.GetScraper(req.ProductURL)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to get scraper: %v", err))
		http.Error(w, "Failed to get scraper: "+err.Error(), http.StatusBadRequest)
		return
	}

	product, err := scraper.ScrapeProduct(req.ProductURL)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to scrape product: %v", err))
		http.Error(w, "Failed to scrape product: "+err.Error(), http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Product scraped successfully")

	// 3. Call Gemini API
	// Construct person details string
	personDetails := fmt.Sprintf("Gender: %s, Height: %.2f cm, Weight: %.2f kg, Chest: %.2f, Waist: %.2f, Hips: %.2f",
		person.Gender, person.Height, person.Weight, person.Chest, person.Waist, person.Hips)

	// Use the first image of the person and product for now
	// In a real scenario, we might want to let the user choose or send multiple
	personImageURL := person.ImagePaths[0] // Assuming these are accessible URLs or local paths we can serve/read
	// If they are local paths, we need to make sure utils.GenerateTryOnImage can handle them or we serve them.
	// For this implementation, we'll assume they are accessible URLs or we might need to adjust fetchImage in utils.
	// Given I don't know the exact format, I'll proceed.

	generatedContent, err := utils.GenerateTryOnImage(r.Context(), personImageURL, product.Images, product.Dimensions, personDetails)
	if err != nil {
		http.Error(w, "Failed to generate try-on image: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 4. Return Response
	w.Header().Set("Content-Type", "application/json") // Or image/jpeg if it's raw bytes
	// If generatedContent is text (URL), return JSON.
	// If it's image bytes, return image.
	// For now, let's wrap it in JSON.
	response := map[string]string{
		"result": string(generatedContent),
	}
	json.NewEncoder(w).Encode(response)
}
