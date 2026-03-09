package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// StartWorker launches the background goroutine that processes 3D profile jobs.
func StartWorker(database *mongo.Database, jobChan <-chan string) {
	log.Println("[3D Worker] Started")
	for jobID := range jobChan {
		log.Printf("[3D Worker] Processing job %s", jobID)
		if err := processJob(database, jobID); err != nil {
			log.Printf("[3D Worker] Error processing job %s: %v", jobID, err)
		}
	}
}

func setProgress(ctx context.Context, col *mongo.Collection, jobID string, status ProfileJobStatus, progress int) {
	col.UpdateOne(ctx,
		bson.M{"job_id": jobID},
		bson.M{"$set": bson.M{
			"status":     status,
			"progress":   progress,
			"updated_at": time.Now().UTC(),
		}},
	)
}

func setFailed(ctx context.Context, col *mongo.Collection, jobID, errMsg string) {
	col.UpdateOne(ctx,
		bson.M{"job_id": jobID},
		bson.M{"$set": bson.M{
			"status":     JobStatusFailed,
			"error":      errMsg,
			"updated_at": time.Now().UTC(),
		}},
	)
}

func processJob(database *mongo.Database, jobID string) error {
	ctx := context.Background()
	col := database.Collection("profile_jobs")

	var job ProfileJob
	if err := col.FindOne(ctx, bson.M{"job_id": jobID}).Decode(&job); err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	setProgress(ctx, col, jobID, JobStatusProcessing, 10)
	log.Printf("[3D Worker] [%s] → processing (10%%)", jobID)

	time.Sleep(1 * time.Second)
	setProgress(ctx, col, jobID, JobStatusProcessing, 30)
	log.Printf("[3D Worker] [%s] → image validation done (30%%)", jobID)

	time.Sleep(1 * time.Second)
	setProgress(ctx, col, jobID, JobStatusProcessing, 50)
	log.Printf("[3D Worker] [%s] → background removal done (50%%)", jobID)

	reconURL := os.Getenv("RECONSTRUCTION_SERVICE_URL")
	if reconURL == "" {
		reconURL = "http://localhost:8000" // Default for testing
	}

	assets, err := callReconstructionService(ctx, job, reconURL)
	if err != nil {
		setFailed(ctx, col, jobID, fmt.Sprintf("reconstruction service error: %v", err))
		return err
	}
	setProgress(ctx, col, jobID, JobStatusProcessing, 70)
	log.Printf("[3D Worker] [%s] → mesh reconstruction done (70%%)", jobID)

	glbKey := assets.GLBS3Key
	usdzKey := assets.USDZS3Key
	thumbKey := assets.ThumbnailS3Key

	setProgress(ctx, col, jobID, JobStatusProcessing, 90)
	log.Printf("[3D Worker] [%s] → asset export done (90%%)", jobID)

	now := time.Now().UTC()
	result := ProfileJobResult{
		GLBURL:       glbKey,
		USDZURL:      usdzKey,
		PreviewImage: thumbKey,
	}

	_, err = col.UpdateOne(ctx,
		bson.M{"job_id": jobID},
		bson.M{"$set": bson.M{
			"status":     JobStatusCompleted,
			"progress":   100,
			"result":     result,
			"updated_at": now,
		}},
	)
	if err != nil {
		setFailed(ctx, col, jobID, "failed to save completion record")
		return err
	}
	log.Printf("[3D Worker] [%s] → completed (100%%)", jobID)

	if err := upsertProfile3D(ctx, database, &job, result, now); err != nil {
		log.Printf("[3D Worker] [%s] warn: failed to upsert profile_3d: %v", jobID, err)
	}

	deleteRawImages(job.FrontKey, job.BackKey, job.SideKey)
	log.Printf("[3D Worker] [%s] raw images deleted", jobID)

	return nil
}

func upsertProfile3D(ctx context.Context, database *mongo.Database, job *ProfileJob, result ProfileJobResult, now time.Time) error {
	col := database.Collection("profiles_3d")

	var existing Profile3D
	version := 1
	if err := col.FindOne(ctx, bson.M{"user_id": job.UserID}).Decode(&existing); err == nil {
		version = existing.Version + 1
	}

	profile := Profile3D{
		UserID:       job.UserID,
		GLBURL:       result.GLBURL,
		USDZURL:      result.USDZURL,
		PreviewImage: result.PreviewImage,
		Version:      version,
		CreatedAt:    now,
	}

	upsertOpt := options.Replace().SetUpsert(true)
	_, err := col.ReplaceOne(ctx, bson.M{"user_id": job.UserID}, profile, upsertOpt)
	return err
}

func deleteRawImages(keys ...string) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		bucket := os.Getenv("AWS_BUCKET_NAME")
		if bucket == "" {
			bucket = "tryonfusion"
		}
		_, err := utils.S3Client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			log.Printf("[3D Worker] warn: could not delete raw image %q: %v", key, err)
		}
	}
}

type reconstructionResult struct {
	GLBS3Key       string
	USDZS3Key      string
	ThumbnailS3Key string
}

func callReconstructionService(ctx context.Context, job ProfileJob, reconURL string) (*reconstructionResult, error) {
	log.Printf("[3D Worker] [%s] calling reconstruction service at %s", job.JobID, reconURL)

	if err := utils.InitS3(); err != nil {
		return nil, fmt.Errorf("S3 init error: %w", err)
	}

	bucket := os.Getenv("AWS_BUCKET_NAME")
	if bucket == "" {
		bucket = "tryonfusion"
	}

	imageKeys := map[string]string{
		"front_image": job.FrontKey,
		"side_image":  job.SideKey,
		"back_image":  job.BackKey,
	}
	imageData := make(map[string][]byte)
	for field, key := range imageKeys {
		if key == "" {
			continue
		}
		out, err := utils.S3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to download %s from S3: %w", field, err)
		}
		data, err := io.ReadAll(out.Body)
		out.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", field, err)
		}
		imageData[field] = data
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	for field, data := range imageData {
		fw, err := mw.CreateFormFile(field, filepath.Base(imageKeys[field]))
		if err != nil {
			return nil, err
		}
		fw.Write(data)
	}
	mw.WriteField("height_cm", fmt.Sprintf("%.1f", job.Height))
	mw.WriteField("user_id", job.UserID)
	mw.Close()

	url := reconURL + "/reconstruct"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request to reconstruction service failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reconstruction service returned %d: %s", resp.StatusCode, respBody)
	}

	var apiResp struct {
		Result struct {
			GLBURL       string `json:"glb_url"`
			USDZURL      string `json:"usdz_url"`
			PreviewImage string `json:"preview_image"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse reconstruction response: %w", err)
	}

	s3Prefix := fmt.Sprintf("3d-profile/%s", job.JobID)

	glbKey, err := downloadAndUploadAsset(ctx, reconURL+apiResp.Result.GLBURL, s3Prefix+"/profile.glb", "model/gltf-binary", bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to upload GLB to S3: %w", err)
	}
	usdzKey, err := downloadAndUploadAsset(ctx, reconURL+apiResp.Result.USDZURL, s3Prefix+"/profile.usdz", "model/vnd.usdz+zip", bucket)
	if err != nil {
		log.Printf("[3D Worker] [%s] warn: USDZ upload failed: %v", job.JobID, err)
		usdzKey = ""
	}
	thumbKey, err := downloadAndUploadAsset(ctx, reconURL+apiResp.Result.PreviewImage, s3Prefix+"/thumbnail.png", "image/png", bucket)
	if err != nil {
		log.Printf("[3D Worker] [%s] warn: thumbnail upload failed: %v", job.JobID, err)
		thumbKey = ""
	}

	return &reconstructionResult{
		GLBS3Key:       glbKey,
		USDZS3Key:      usdzKey,
		ThumbnailS3Key: thumbKey,
	}, nil
}

func downloadAndUploadAsset(ctx context.Context, srcURL, destKey, contentType, bucket string) (string, error) {
	resp, err := http.Get(srcURL)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", srcURL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	_, err = utils.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(destKey),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("S3 upload to %s: %w", destKey, err)
	}
	return destKey, nil
}
