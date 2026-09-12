// Command trend_upload_sweep deletes trend input images that S3 is still
// holding but nothing references any more.
//
// Two prefixes accumulate orphans, for different reasons:
//
//   - trend_inputs/{userID}/ — a photo staged by POST /trends/uploads. A
//     generation that used it clears the document's expires_at, which is what
//     promotes the object from "staged" to evidence attached to a run. One that
//     was never used leaves the Mongo document to its TTL index and the S3
//     object to this command.
//
//   - trend_previews/inputs/ — the sample photos an admin pasted into the Test
//     tab. They have no Mongo record at all and nothing refers to them once the
//     preview has run, so past the cutoff they are always orphans. Note that
//     the parent prefix trend_previews/ holds preview *outputs*, which run
//     records do reference — hence the narrower prefix here.
//
// Usage:
//
//	go run ./tools/trend_upload_sweep                  # dry run (default)
//	go run ./tools/trend_upload_sweep -dry-run=false   # actually delete
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

func main() {
	dryRun := flag.Bool("dry-run", true, "list candidates without deleting")
	olderThan := flag.Duration("older-than", 24*time.Hour, "sweep uploads older than this age")
	flag.Parse()

	config.LoadConfig()
	if err := utils.ConnectMongo(config.MongoURI); err != nil {
		log.Fatalf("failed to connect to mongo: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if utils.Client != nil {
			_ = utils.Client.Disconnect(ctx)
		}
	}()

	if err := utils.InitS3(); err != nil {
		log.Fatalf("failed to init s3: %v", err)
	}

	ctx := context.Background()
	bucket := config.AWSBucketName
	cutoff := time.Now().Add(-*olderThan)
	uploads := utils.GetCollection(config.DBName, models.CollTrendUploads)

	if *dryRun {
		fmt.Println("DRY RUN — pass -dry-run=false to actually delete.")
	}
	fmt.Printf("Cutoff: objects last modified before %s\n\n", cutoff.Format(time.RFC3339))

	var scanned, candidates, deleted int

	// Staged user uploads: keep anything a generation claimed.
	s, c, d := sweep(ctx, bucket, "trend_inputs/", cutoff, *dryRun, func(key string) bool {
		return isClaimedUpload(ctx, uploads, key)
	})
	scanned, candidates, deleted = scanned+s, candidates+c, deleted+d

	// Admin preview samples: never referenced, so nothing to keep.
	s, c, d = sweep(ctx, bucket, "trend_previews/inputs/", cutoff, *dryRun, func(string) bool {
		return false
	})
	scanned, candidates, deleted = scanned+s, candidates+c, deleted+d

	fmt.Printf("\nDone. Scanned: %d, Candidates: %d, Deleted: %d\n", scanned, candidates, deleted)
}

// sweep walks one prefix, offering every object past the cutoff to `keep`.
func sweep(ctx context.Context, bucket, prefix string, cutoff time.Time, dryRun bool,
	keep func(key string) bool) (scanned, candidates, deleted int) {

	fmt.Printf("Scanning s3://%s/%s\n", bucket, prefix)

	paginator := s3.NewListObjectsV2Paginator(utils.S3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			// Returning rather than exiting: the second prefix is still worth
			// sweeping if the first one's listing broke half way through.
			log.Printf("failed to list %s: %v", prefix, err)
			return
		}

		for _, obj := range page.Contents {
			scanned++
			if obj.LastModified == nil || obj.LastModified.After(cutoff) {
				continue
			}

			key := aws.ToString(obj.Key)
			if keep(key) {
				continue
			}

			candidates++
			fmt.Printf("  candidate: %s (age %v)\n", key, time.Since(*obj.LastModified).Round(time.Minute))

			if dryRun {
				continue
			}
			if _, err := utils.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(key),
			}); err != nil {
				log.Printf("  failed to delete %s: %v", key, err)
				continue
			}
			deleted++
		}
	}
	return
}

// isClaimedUpload reports whether a generation kept this object as evidence.
//
// The field is `object_key` — models.TrendUpload.ObjectKey. An earlier version
// of this command queried `s3_key`, which matches no document, so every count
// came back zero and every object past the cutoff looked like an orphan. Run
// for real, that would have deleted the input photos of every completed trend
// run and left the admin's "images sent to the model" panel permanently blank.
//
// A lookup that errors keeps the object. Holding an orphan costs a fraction of
// a cent; deleting evidence because Mongo blinked is not recoverable.
func isClaimedUpload(ctx context.Context, uploads *mongo.Collection, key string) bool {
	count, err := uploads.CountDocuments(ctx, bson.M{
		"object_key": key,
		"expires_at": bson.M{"$exists": false},
	})
	if err != nil {
		log.Printf("  mongo lookup failed for %s, keeping it: %v", key, err)
		return true
	}
	return count > 0
}
