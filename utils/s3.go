package utils

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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

const (
	// CacheControlImmutable is for images that never change (products, generated try-ons, themes).
	CacheControlImmutable = "public, max-age=2592000, immutable"
	// CacheControlMutable is for images that can change (profile photos).
	CacheControlMutable = "public, max-age=86400"
)

// UploadFileToS3 uploads a file to S3 and returns the Object Key.
// An optional cacheControl parameter sets the Cache-Control header on the S3 object.
// If omitted, CacheControlImmutable is used by default.
func UploadFileToS3(ctx context.Context, file io.Reader, objectKey string, contentType string, cacheControl ...string) (string, error) {
	if S3Client == nil {
		if err := InitS3(); err != nil {
			return "", err
		}
	}

	cc := CacheControlImmutable
	if len(cacheControl) > 0 && cacheControl[0] != "" {
		cc = cacheControl[0]
	}

	_, err := S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(appConfig.AWSBucketName),
		Key:          aws.String(objectKey),
		Body:         file,
		ContentType:  aws.String(contentType),
		CacheControl: aws.String(cc),
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload file to S3: %v", err)
	}

	return objectKey, nil
}

// Presigned URL lifetimes.
//
// The rule these encode: a signature must outlive every cache that can hold
// the JSON carrying it. Get that backwards and the response survives longer
// than its own links — the client replays a perfectly valid cached payload
// full of URLs that S3 now answers with 403, and every image silently goes
// blank. That is exactly what happened to the theme grid, which was served
// with `Cache-Control: max-age=2592000, immutable` (30 days) while its URLs
// expired after an hour.
const (
	// PresignShort is for URLs consumed immediately by the server itself —
	// fetching a reference image for a generation, say. Nothing caches these.
	PresignShort = 1 * time.Hour

	// PresignCatalog is for URLs that travel to a client inside a cacheable
	// listing. AWS SigV4 caps a presigned URL at 7 days; 6 leaves a day of
	// headroom, and every listing that uses this must be cached for less.
	PresignCatalog = 6 * 24 * time.Hour
)

// GetPresignedURL generates a presigned URL valid for PresignShort.
func GetPresignedURL(ctx context.Context, objectKey string) (string, error) {
	return GetPresignedURLWithExpiry(ctx, objectKey, PresignShort)
}

// GetPresignedURLWithExpiry generates a presigned URL with an explicit
// lifetime. Callers whose response is cached must pass one that comfortably
// exceeds that cache's max-age — see the constants above.
func GetPresignedURLWithExpiry(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
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
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("failed to sign request: %v", err)
	}

	return request.URL, nil
}

// DeleteObjectsFromS3 removes objects by key, in batches of 1000 (the
// DeleteObjects API limit). It reports the number actually deleted.
//
// Deletion is idempotent on S3's side — removing a key that is already gone is
// not an error — so a retried purge is safe. Per-key failures are collected
// and returned rather than aborting the batch: a purge that removed 40 of 41
// objects is a much better outcome than one that stopped at the first error.
func DeleteObjectsFromS3(ctx context.Context, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	if S3Client == nil {
		if err := InitS3(); err != nil {
			return 0, err
		}
	}

	deleted := 0
	var failures []string

	for start := 0; start < len(keys); start += 1000 {
		end := start + 1000
		if end > len(keys) {
			end = len(keys)
		}

		ids := make([]types.ObjectIdentifier, 0, end-start)
		for _, k := range keys[start:end] {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(k)})
		}

		out, err := S3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(appConfig.AWSBucketName),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("batch at %d: %v", start, err))
			continue
		}
		deleted += end - start - len(out.Errors)
		for _, e := range out.Errors {
			failures = append(failures, fmt.Sprintf("%s: %s", aws.ToString(e.Key), aws.ToString(e.Message)))
		}
	}

	if len(failures) > 0 {
		return deleted, fmt.Errorf("deleted %d/%d objects; failures: %s",
			deleted, len(keys), strings.Join(failures, "; "))
	}
	return deleted, nil
}
