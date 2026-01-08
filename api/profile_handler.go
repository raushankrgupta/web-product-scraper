package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	DatabaseName   = "fitly"
	CollectionName = "person"
	UploadDir      = "user_images"
)

// PersonHandler handles CRUD operations for persons
func PersonHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		createPerson(w, r)
	case http.MethodGet:
		getPersons(w, r)
	case http.MethodDelete:
		deletePerson(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func createPerson(w http.ResponseWriter, r *http.Request) {
	userIdStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID, _ := primitive.ObjectIDFromHex(userIdStr)

	// Parse multipart form (max 10MB)
	err = r.ParseMultipartForm(10 << 20)
	if err != nil {
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

	if name == "" {
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
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				continue
			}
			defer file.Close()

			filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), fileHeader.Filename)
			objectKey := fmt.Sprintf("person_images/%s", filename)

			_, err = utils.UploadFileToS3(r.Context(), file, objectKey, fileHeader.Header.Get("Content-Type"))
			if err != nil {
				// Log error but continue with other files?
				fmt.Printf("Failed to upload %s: %v\n", filename, err)
				continue
			}

			imagePaths = append(imagePaths, objectKey)
		}
	}

	person := models.Person{
		UserID:     userID,
		Name:       name,
		Age:        age,
		Gender:     gender,
		Height:     height,
		Weight:     weight,
		Chest:      chest,
		Waist:      waist,
		Hips:       hips,
		ImagePaths: imagePaths,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	collection := utils.GetCollection(DatabaseName, CollectionName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := collection.InsertOne(ctx, person)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error saving to database: %v", err), http.StatusInternalServerError)
		return
	}
	person.ID = result.InsertedID.(primitive.ObjectID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(person)
}

func getPersons(w http.ResponseWriter, r *http.Request) {
	userIdStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID, _ := primitive.ObjectIDFromHex(userIdStr)

	// Check if requesting specific person
	pathParts := strings.Split(r.URL.Path, "/")
	// /persons -> ["", "persons"] -> len 2
	// /persons/{id} -> ["", "persons", "id"] -> len 3
	if len(pathParts) > 2 && pathParts[2] != "" {
		getPersonByID(w, r, pathParts[2], userID)
		return
	}

	collection := utils.GetCollection(DatabaseName, CollectionName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cursor, err := collection.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		http.Error(w, "Error fetching persons", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var persons []models.Person
	if err = cursor.All(ctx, &persons); err != nil {
		http.Error(w, "Error decoding persons", http.StatusInternalServerError)
		return
	}

	// Generate Presigned URLs
	for i := range persons {
		var presignedURLs []string
		for _, key := range persons[i].ImagePaths {
			if url, err := utils.GetPresignedURL(r.Context(), key); err == nil {
				presignedURLs = append(presignedURLs, url)
			} else {
				presignedURLs = append(presignedURLs, key) // Fallback or handle error
			}
		}
		persons[i].ImagePaths = presignedURLs
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(persons)
}

func getPersonByID(w http.ResponseWriter, r *http.Request, idStr string, userID primitive.ObjectID) {
	personID, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(DatabaseName, CollectionName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var person models.Person
	err = collection.FindOne(ctx, bson.M{"_id": personID, "user_id": userID}).Decode(&person)
	if err != nil {
		http.Error(w, "Person not found", http.StatusNotFound)
		return
	}

	// Generate Presigned URLs
	var presignedURLs []string
	for _, key := range person.ImagePaths {
		if url, err := utils.GetPresignedURL(r.Context(), key); err == nil {
			presignedURLs = append(presignedURLs, url)
		} else {
			presignedURLs = append(presignedURLs, key) // Fallback
		}
	}
	person.ImagePaths = presignedURLs

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(person)
}

func deletePerson(w http.ResponseWriter, r *http.Request) {
	userIdStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID, _ := primitive.ObjectIDFromHex(userIdStr)

	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 3 || pathParts[2] == "" {
		http.Error(w, "Person ID required", http.StatusBadRequest)
		return
	}
	personID, err := primitive.ObjectIDFromHex(pathParts[2])
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(DatabaseName, CollectionName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := collection.DeleteOne(ctx, bson.M{"_id": personID, "user_id": userID})
	if err != nil {
		http.Error(w, "Error deleting person", http.StatusInternalServerError)
		return
	}

	if result.DeletedCount == 0 {
		http.Error(w, "Person not found or unauthorized", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
