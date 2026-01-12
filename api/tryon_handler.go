package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TryOnRequest represents the request body for virtual try-on
type TryOnRequest struct {
	ProductID string `json:"product_id"`
	PersonID  string `json:"person_id"`
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

	// Debug: Print raw body
	bodyBytes, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // Restore body for decoder
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Received Body: %s", string(bodyBytes)))

	var req TryOnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate input
	if req.ProductID == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Missing product_id")
		http.Error(w, "product_id is required", http.StatusBadRequest)
		return
	}

	if req.PersonID == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Missing person_id")
		http.Error(w, "person_id is required", http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Try-On Request: PersonID=%s, ProductID=%s", req.PersonID, req.ProductID))

	// 1. Fetch Person Data
	personObjID, err := primitive.ObjectIDFromHex(req.PersonID)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid person ID")
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	personCollection := utils.GetCollection("fitly", "person")
	var person models.Person
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = personCollection.FindOne(ctx, bson.M{"_id": personObjID}).Decode(&person)
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

	// 2. Get Product Data (from DB)
	var product models.Product
	var productURL string

	// Fetch from database
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Fetching product from DB: %s", req.ProductID))
	productObjID, err := primitive.ObjectIDFromHex(req.ProductID)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid product ID")
		http.Error(w, "Invalid product ID", http.StatusBadRequest)
		return
	}

	productCollection := utils.GetCollection("fitly", "products")
	err = productCollection.FindOne(ctx, bson.M{"_id": productObjID}).Decode(&product)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Product not found in DB: %v", err))
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	}
	productURL = product.URL // Get URL from database
	utils.AddToLogMessage(&logMessageBuilder, "Product fetched from database")

	// 3. Call Gemini API
	// Construct person details string
	personDetails := fmt.Sprintf("Gender: %s, Height: %.2f cm, Weight: %.2f kg, Chest: %.2f, Waist: %.2f, Hips: %.2f",
		person.Gender, person.Height, person.Weight, person.Chest, person.Waist, person.Hips)

	// Use the first image of the person and product for now
	// Person Image Path is now an S3 Key, so we need to generate a Presigned URL (or read it and pass bytes, but existing helper fetches from URL)
	personImageKey := person.ImagePaths[0]
	personImageURL, err := utils.GetPresignedURL(r.Context(), personImageKey)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to get presigned URL for person image: %v", err))
		http.Error(w, "Failed to get person image", http.StatusInternalServerError)
		return
	}

	// Use a background context with timeout for the heavy Gemini call
	// This prevents the operation from being aborted if the client disconnects/times out
	geminiCtx, cancelGemini := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelGemini()

	generatedContent, err := utils.GenerateTryOnImage(geminiCtx, personImageURL, product.Images, product.Dimensions, personDetails)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to generate try-on image: %v", err))
		if strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "quota") {
			http.Error(w, "Quota exceeded. Please try again later.", http.StatusTooManyRequests)
		} else {
			http.Error(w, "Failed to generate try-on image: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// 4. Save Try-On Record
	// Upload generated image to S3
	fileName := fmt.Sprintf("generated_tryon_%d.jpg", time.Now().UnixNano())
	objectKey := fmt.Sprintf("generated_images/%s", fileName)

	// generatedContent is []byte
	_, err = utils.UploadFileToS3(r.Context(), bytes.NewReader(generatedContent), objectKey, "image/jpeg")
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to upload generated image: %v", err))
		http.Error(w, "Failed to upload generated image", http.StatusInternalServerError)
		return
	}

	// Capture UserID
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Warning: UserID not found in context")
	}

	tryOnRecord := models.TryOn{
		ID:                primitive.NewObjectID(),
		UserID:            userID,
		PersonID:          req.PersonID,
		ProductURL:        productURL,
		ProductID:         req.ProductID,
		PersonImageURL:    personImageKey, // Store Key
		GeneratedImageURL: objectKey,      // Store Key
		Status:            "completed",
		CreatedAt:         time.Now(),
	}

	tryOnCollection := utils.GetCollection("fitly", "tryons")
	_, err = tryOnCollection.InsertOne(context.Background(), tryOnRecord)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save try-on record: %v", err))
		// We proceed to return the response even if DB save fails
	}

	// Generate Presigned URL for response
	presignedGeneratedURL, _ := utils.GetPresignedURL(r.Context(), objectKey)
	tryOnRecord.GeneratedImageURL = presignedGeneratedURL

	// 5. Return Response
	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"result":        tryOnRecord.GeneratedImageURL,
		"tryon_details": tryOnRecord,
	}
	json.NewEncoder(w).Encode(response)
}
