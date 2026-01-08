package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UploadImagesToS3 downloads images from URLs and uploads them to S3
// Returns a map of Original URL -> S3 Object Key
func UploadImagesToS3(ctx context.Context, urls []string, folderPrefix string) (map[string]string, error) {
	urlToKey := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Limit concurrency
	semaphore := make(chan struct{}, 5)

	for i, url := range urls {
		if url == "" {
			continue
		}
		wg.Add(1)
		go func(i int, url string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Generate S3 Key
			filename := filepath.Base(url)
			if strings.Contains(filename, "?") {
				filename = strings.Split(filename, "?")[0]
			}
			if filename == "" || len(filename) > 255 {
				filename = fmt.Sprintf("image_%d.jpg", i)
			}
			// ensure unique names
			filename = fmt.Sprintf("%d_%s", time.Now().UnixNano(), filename)
			objectKey := fmt.Sprintf("%s/%s", folderPrefix, filename)

			// Download and Upload
			if err := downloadAndUpload(ctx, url, objectKey); err != nil {
				fmt.Printf("Failed to process %s: %v\n", url, err)
				return
			}

			mu.Lock()
			urlToKey[url] = objectKey
			mu.Unlock()
		}(i, url)
	}

	wg.Wait()
	return urlToKey, nil
}

func downloadAndUpload(ctx context.Context, url, objectKey string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (macOS) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Read into buffer to upload (S3 UploadFileToS3 takes io.Reader, but PutObject requires seekable or known length?
	// aws-sdk-go-v2 PutObject body is io.Reader. But helper might need length.
	// We read to bytes to be safe and set Content-Type if possible.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = UploadFileToS3(ctx, bytes.NewReader(bodyBytes), objectKey, contentType)
	return err
}
