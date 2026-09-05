package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxImageDownloadBytes bounds fetched images to prevent OOM / decompression bombs
const maxImageDownloadBytes = 25 << 20 // 25 MiB

// isPublicHTTPURL verifies that the URL has http/https scheme and does not resolve
// to private, loopback, or cloud-metadata IP addresses (SSRF prevention).
func isPublicHTTPURL(rawURL string) error {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q, only http and https are allowed", u.Scheme)
	}
	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("missing hostname")
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("failed to resolve host %q: %w", hostname, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no IP addresses resolved for %q", hostname)
	}

	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("request to internal or private network IP %s is prohibited", ip.String())
		}
	}
	return nil
}

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
				// Warn, not Info: the caller's fallback is to keep the
				// original retailer URL, which then gets persisted to the
				// wardrobe and 404s at try-on time, hours or days later.
				// This line is the only place that failure is visible.
				slog.Warn("image re-host failed, keeping remote URL",
					"url", url, "key", objectKey, "error", err.Error())
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
	if err := isPublicHTTPURL(url); err != nil {
		return fmt.Errorf("ssrf check blocked url %q: %w", url, err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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

	// Bound memory reading with LimitReader to avoid OOM crashes
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadBytes+1))
	if err != nil {
		return err
	}
	if len(bodyBytes) > maxImageDownloadBytes {
		return fmt.Errorf("image exceeds maximum allowed size of %d bytes", maxImageDownloadBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = UploadFileToS3(ctx, bytes.NewReader(bodyBytes), objectKey, contentType)
	return err
}
