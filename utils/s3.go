package utils

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	appConfig "github.com/raushankrgupta/web-product-scraper/config"
)

var (
	S3Client      *s3.Client
	PresignClient *s3.PresignClient
)

// InitS3 initializes the S3 client
func InitS3() error {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(appConfig.AWSRegion),
	)
	if err != nil {
		return fmt.Errorf("unable to load SDK config, %v", err)
	}

	S3Client = s3.NewFromConfig(cfg)
	PresignClient = s3.NewPresignClient(S3Client)
	log.Println("S3 Client Initialized")
	return nil
}

// UploadFileToS3 uploads a file to S3 and returns the Object Key
func UploadFileToS3(ctx context.Context, file io.Reader, objectKey string, contentType string) (string, error) {
	if S3Client == nil {
		if err := InitS3(); err != nil {
			return "", err
		}
	}

	_, err := S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(appConfig.AWSBucketName),
		Key:         aws.String(objectKey),
		Body:        file,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload file to S3: %v", err)
	}

	return objectKey, nil
}

// GetPresignedURL generates a presigned URL for an object
func GetPresignedURL(ctx context.Context, objectKey string) (string, error) {
	if PresignClient == nil {
		if err := InitS3(); err != nil {
			return "", err
		}
	}

	// Determine if input is a full URL or just a key
	// If it's already a URL (e.g. from scraping fallback), logic elsewhere might need handling.
	// We assume objectKey is the S3 key.

	request, err := PresignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(appConfig.AWSBucketName),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(1*time.Hour))
	if err != nil {
		return "", fmt.Errorf("failed to sign request: %v", err)
	}

	return request.URL, nil
}
