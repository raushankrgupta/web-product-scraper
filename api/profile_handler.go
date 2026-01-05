package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

const (
	DatabaseName   = "fitly"
	CollectionName = "person"
	UploadDir      = "user_images"
)

func CreateProfileHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Create Profile API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (max 10MB)
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error parsing form data: %v", err))
		http.Error(w, "Error parsing form data", http.StatusBadRequest)
		return
	}

	// Extract fields
	name := r.FormValue("name")
	ageStr := r.FormValue("age")
	gender := r.FormValue("gender")
	heightStr := r.FormValue("height")
	weightStr := r.FormValue("weight")
	chestStr := r.FormValue("chest")
	waistStr := r.FormValue("waist")
	hipsStr := r.FormValue("hips")

	// Validate required fields (basic validation)
	if name == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Name is required")
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	// Parse numeric values
	age, _ := strconv.Atoi(ageStr)
	height, _ := strconv.ParseFloat(heightStr, 64)
	weight, _ := strconv.ParseFloat(weightStr, 64)
	chest, _ := strconv.ParseFloat(chestStr, 64)
	waist, _ := strconv.ParseFloat(waistStr, 64)
	hips, _ := strconv.ParseFloat(hipsStr, 64)

	// Handle file uploads
	files := r.MultipartForm.File["images"]
	var imagePaths []string

	if len(files) > 0 {
		// Create upload directory if it doesn't exist
		if _, err := os.Stat(UploadDir); os.IsNotExist(err) {
			os.Mkdir(UploadDir, 0755)
		}

		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error retrieving file: %v", err))
				http.Error(w, "Error retrieving file", http.StatusInternalServerError)
				return
			}
			defer file.Close()

			// Create a unique filename
			filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), fileHeader.Filename)
			filePath := filepath.Join(UploadDir, filename)

			// Create destination file
			dst, err := os.Create(filePath)
			if err != nil {
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error saving file: %v", err))
				http.Error(w, "Error saving file", http.StatusInternalServerError)
				return
			}
			defer dst.Close()

			// Copy buffer
			if _, err := io.Copy(dst, file); err != nil {
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error saving file content: %v", err))
				http.Error(w, "Error saving file content", http.StatusInternalServerError)
				return
			}

			// Store absolute path or relative path? User asked for "file paths".
			// Let's store the relative path for portability, or absolute if needed.
			// The prompt said "Add all the image filepaths".
			// I'll store the relative path from the project root.
			imagePaths = append(imagePaths, filePath)
		}
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Processed %d images", len(imagePaths)))

	// Create Person object
	now := time.Now()
	person := models.Person{
		Name:       name,
		Age:        age,
		Gender:     gender,
		Height:     height,
		Weight:     weight,
		Chest:      chest,
		Waist:      waist,
		Hips:       hips,
		ImagePaths: imagePaths,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// Save to MongoDB
	collection := utils.GetCollection(DatabaseName, CollectionName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := collection.InsertOne(ctx, person)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error saving to database: %v", err))
		http.Error(w, fmt.Sprintf("Error saving to database: %v", err), http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Profile created successfully")

	// Return success response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Profile created successfully",
		"id":      result.InsertedID,
		"person":  person,
	})
}
