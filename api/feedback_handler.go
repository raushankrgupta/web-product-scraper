package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// FeedbackHandler handles feedback submission
func FeedbackHandler(w http.ResponseWriter, r *http.Request) {
	logMessageBuilder := strings.Builder{}
	utils.AddToLogMessage(&logMessageBuilder, "FEEDBACK_SUBMISSION")
	defer func() {
		// Log the built message if needed
		fmt.Println(logMessageBuilder.String())
	}()

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get user ID from context
	userIDStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}

	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid user ID", http.StatusUnauthorized)
		return
	}

	// Parse multipart form
	err = r.ParseMultipartForm(10 << 20) // 10 MB limit
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Error parsing form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	message := r.FormValue("message")
	countryCode := r.FormValue("country_code")
	mobileNumber := r.FormValue("mobile_number")
	contactBackStr := r.FormValue("contact_back")
	contactBack := contactBackStr == "true"

	if name == "" || email == "" || message == "" {
		utils.RespondError(w, &logMessageBuilder, "Name, email, and message are required", http.StatusBadRequest)
		return
	}

	// Handle file uploads
	var filePaths []string
	files := r.MultipartForm.File["files"]
	for _, file := range files {
		f, err := file.Open()
		if err != nil {
			utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Error opening file %s", file.Filename), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		ext := filepath.Ext(file.Filename)
		objectKey := fmt.Sprintf("feedback/%s/%s%s", userIDStr, uuid.New().String(), ext)

		path, err := utils.UploadFileToS3(context.TODO(), f, objectKey, file.Header.Get("Content-Type"))
		if err != nil {
			utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Error uploading file %s", file.Filename), http.StatusInternalServerError)
			return
		}
		filePaths = append(filePaths, path)
	}

	// Save to MongoDB
	feedback := models.Feedback{
		ID:           primitive.NewObjectID(),
		UserID:       userID,
		Name:         name,
		Email:        email,
		CountryCode:  countryCode,
		MobileNumber: mobileNumber,
		Message:      message,
		ContactBack:  contactBack,
		FilePaths:    filePaths,
		CreatedAt:    time.Now(),
	}

	collection := utils.GetCollection(config.DBName, "feedbacks")
	_, err = collection.InsertOne(context.TODO(), feedback)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Error saving feedback", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusCreated, map[string]string{"message": "Feedback submitted successfully"})
}
