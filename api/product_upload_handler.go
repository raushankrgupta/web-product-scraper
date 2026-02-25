package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// UploadProductHandler handles the upload of product images by users
func UploadProductHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Upload Product API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userIdStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID", http.StatusUnauthorized)
		return
	}
	// userIdStr is used for logging/record keeping, but generally we might store it as string or ObjectID depending on model.
	// Product model uses UserID as string.

	// Parse multipart form (max 10MB)
	err = r.ParseMultipartForm(10 << 20)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Error parsing form data", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		utils.RespondError(w, &logMessageBuilder, "No images uploaded", http.StatusBadRequest)
		return
	}

	var imagePaths []string
	for _, fileHeader := range files {
		file, err := fileHeader.Open()
		if err != nil {
			continue
		}
		defer file.Close()

		filename := fmt.Sprintf("prod_%d_%s", time.Now().UnixNano(), fileHeader.Filename)
		objectKey := fmt.Sprintf("product_uploads/%s", filename)

		_, err = utils.UploadFileToS3(r.Context(), file, objectKey, fileHeader.Header.Get("Content-Type"))
		if err != nil {
			fmt.Printf("Failed to upload %s: %v\n", filename, err)
			continue
		}
		imagePaths = append(imagePaths, objectKey)
	}

	if len(imagePaths) == 0 {
		utils.RespondError(w, &logMessageBuilder, "Failed to upload any images", http.StatusInternalServerError)
		return
	}

	product := models.Product{
		UserID:      userIdStr,
		Source:      "user_upload",
		Title:       "User Uploaded Product",
		Images:      imagePaths,
		CreatedAt:   time.Now(),
		Description: "Uploaded by user",
		// Initialize other fields as empty/default to avoid nil pointer issues if used elsewhere
		Dimensions: "",
		Category:   "User Upload",
	}

	collection := utils.GetCollection("fitly", "products")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := collection.InsertOne(ctx, product)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Error saving to database: %v", err), http.StatusInternalServerError)
		return
	}
	product.ID = result.InsertedID.(primitive.ObjectID)

	// Presign URLs for immediate display
	product.Images = utils.PresignImageURLs(r.Context(), product.Images)

	utils.RespondJSON(w, http.StatusCreated, product)
}
