package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var jobChan chan<- string

func SetJobChan(ch chan<- string) {
	jobChan = ch
}

func uploadImageToS3(r *http.Request, fieldName, s3Key string) error {
	f, fh, err := r.FormFile(fieldName)
	if err != nil {
		return fmt.Errorf("missing required field %q: %w", fieldName, err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("cannot read %q: %w", fieldName, err)
	}

	if err := utils.InitS3(); err != nil {
		return fmt.Errorf("S3 init error: %w", err)
	}

	bucket := os.Getenv("AWS_BUCKET_NAME")
	if bucket == "" {
		bucket = "tryonfusion"
	}

	_, err = utils.S3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(s3Key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(fh.Header.Get("Content-Type")),
	})
	if err != nil {
		return fmt.Errorf("S3 upload error for %q: %w", fieldName, err)
	}
	return nil
}

func CreateProfileJobHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		utils.RespondError(w, nil, "Failed to parse form", http.StatusBadRequest)
		return
	}

	userID := r.FormValue("user_id")
	if userID == "" {
		utils.RespondError(w, nil, "user_id is required", http.StatusBadRequest)
		return
	}

	jobID := uuid.New().String()
	prefix := fmt.Sprintf("3d-profile/%s/", jobID)

	frontKey := prefix + "front.jpg"
	backKey := prefix + "back.jpg"
	sideKey := prefix + "side.jpg"

	for _, upload := range []struct {
		field, key string
	}{
		{"front_image", frontKey},
		{"back_image", backKey},
		{"side_image", sideKey},
	} {
		if err := uploadImageToS3(r, upload.field, upload.key); err != nil {
			utils.RespondError(w, nil, err.Error(), http.StatusBadRequest)
			return
		}
	}

	var height float64
	if h := r.FormValue("height_cm"); h != "" {
		fmt.Sscanf(h, "%f", &height)
	} else if h := r.FormValue("height"); h != "" {
		fmt.Sscanf(h, "%f", &height)
	}

	now := time.Now().UTC()
	job := ProfileJob{
		JobID:     jobID,
		UserID:    userID,
		Status:    JobStatusPending,
		Progress:  0,
		Height:    height,
		FrontKey:  frontKey,
		BackKey:   backKey,
		SideKey:   sideKey,
		CreatedAt: now,
		UpdatedAt: now,
	}

	db := utils.GetCollection("fitly", "profile_jobs").Database()
	if _, err := db.Collection("profile_jobs").InsertOne(context.Background(), job); err != nil {
		utils.RespondError(w, nil, "Failed to create job", http.StatusInternalServerError)
		return
	}

	if jobChan != nil {
		select {
		case jobChan <- jobID:
		default:
		}
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"job_id": jobID,
		"status": string(JobStatusPending),
	})
}

func GetJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/3d-profile/jobs/{job_id}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		utils.RespondError(w, nil, "job_id is required", http.StatusBadRequest)
		return
	}
	jobID := parts[4]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var job ProfileJob
	db := utils.GetCollection("fitly", "profile_jobs").Database()
	err := db.Collection("profile_jobs").FindOne(ctx, bson.M{"job_id": jobID}).Decode(&job)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.RespondError(w, nil, "Job not found", http.StatusNotFound)
			return
		}
		utils.RespondError(w, nil, "Error fetching job", http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"job_id":   job.JobID,
		"status":   string(job.Status),
		"progress": job.Progress,
	}
	if job.Error != "" {
		resp["error"] = job.Error
	}
	if job.Status == JobStatusCompleted {
		glbURL, _ := utils.GetPresignedURL(ctx, job.Result.GLBURL)
		usdzURL, _ := utils.GetPresignedURL(ctx, job.Result.USDZURL)
		previewURL, _ := utils.GetPresignedURL(ctx, job.Result.PreviewImage)
		resp["result"] = map[string]interface{}{
			"glb_url":       glbURL,
			"usdz_url":      usdzURL,
			"preview_image": previewURL,
		}
	}

	utils.RespondJSON(w, http.StatusOK, resp)
}

func GetUserProfileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/users/{user_id}/3d-profile
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		utils.RespondError(w, nil, "user_id is required", http.StatusBadRequest)
		return
	}
	userID := parts[3]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := utils.GetCollection("fitly", "profiles_3d").Database()
	opts := options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}})
	var profile Profile3D
	err := db.Collection("profiles_3d").FindOne(ctx, bson.M{"user_id": userID}, opts).Decode(&profile)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.RespondError(w, nil, "No 3D profile found for this user", http.StatusNotFound)
			return
		}
		utils.RespondError(w, nil, "Error fetching profile", http.StatusInternalServerError)
		return
	}

	glbURL, _ := utils.GetPresignedURL(ctx, profile.GLBURL)
	usdzURL, _ := utils.GetPresignedURL(ctx, profile.USDZURL)
	previewURL, _ := utils.GetPresignedURL(ctx, profile.PreviewImage)

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"glb_url":       glbURL,
		"usdz_url":      usdzURL,
		"preview_image": previewURL,
		"version":       profile.Version,
		"created_at":    profile.CreatedAt,
	})
}
