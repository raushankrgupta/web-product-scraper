package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
// VirtualTryOnHandler handles the virtual try-on request
func VirtualTryOnHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Virtual Try-On API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TryOnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate input
	if req.ProductID == "" || req.PersonID == "" {
		utils.RespondError(w, &logMessageBuilder, "product_id and person_id are required", http.StatusBadRequest)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Try-On Request: PersonID=%s, ProductID=%s", req.PersonID, req.ProductID))

	// 1. Fetch Person Data
	personObjID, err := primitive.ObjectIDFromHex(req.PersonID)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid person ID", http.StatusBadRequest)
		return
	}

	personCollection := utils.GetCollection("fitly", "person")
	var person models.Person
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = personCollection.FindOne(ctx, bson.M{"_id": personObjID}).Decode(&person)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Person not found", http.StatusNotFound)
		return
	}

	if len(person.ImagePaths) == 0 {
		utils.RespondError(w, &logMessageBuilder, "Person has no images", http.StatusBadRequest)
		return
	}

	// 2. Get Product Data (from DB)
	var product models.Product
	// Fetch from database
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Fetching product from DB: %s", req.ProductID))
	productObjID, err := primitive.ObjectIDFromHex(req.ProductID)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid product ID", http.StatusBadRequest)
		return
	}

	productCollection := utils.GetCollection("fitly", "products")
	err = productCollection.FindOne(ctx, bson.M{"_id": productObjID}).Decode(&product)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Product not found", http.StatusNotFound)
		return
	}
	utils.AddToLogMessage(&logMessageBuilder, "Product fetched from database")

	// Pre-process Product Images: Ensure they are accessible URLs
	// We use our helper which handles checking if it's already a URL or needs presigning
	product.Images = utils.PresignImageURLs(r.Context(), product.Images)

	// 3. Call Gemini API
	// Construct person details string
	personDetails := fmt.Sprintf("Gender: %s, Height: %.2f cm, Weight: %.2f kg, Chest: %.2f, Waist: %.2f, Hips: %.2f",
		person.Gender, person.Height, person.Weight, person.Chest, person.Waist, person.Hips)

	// Use the first image of the person
	personImageKey := person.ImagePaths[0]
	personImageURL, err := utils.GetPresignedURL(r.Context(), personImageKey)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Failed to get presigned URL for person image: %v", err), http.StatusInternalServerError)
		return
	}

	// Use a background context with timeout for the heavy Gemini call
	geminiCtx, cancelGemini := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelGemini()

	generatedContent, err := utils.GenerateTryOnImage(geminiCtx, personImageURL, product.Images, product.Dimensions, personDetails)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to generate try-on image: %v", err))
		if strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "quota") {
			utils.RespondError(w, nil, "Quota exceeded. Please try again later.", http.StatusTooManyRequests)
		} else {
			utils.RespondError(w, nil, "Failed to generate try-on image: "+err.Error(), http.StatusInternalServerError)
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
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Failed to upload generated image: %v", err), http.StatusInternalServerError)
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
		ProductURL:        product.URL,
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
	response := map[string]interface{}{
		"result":        tryOnRecord.GeneratedImageURL,
		"tryon_details": tryOnRecord,
	}
	utils.RespondJSON(w, http.StatusOK, response)
}
