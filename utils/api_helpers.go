package utils

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RespondJSON sends a JSON response with the given status code and payload.
func RespondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// Fallback error logging if encoding fails, though we can't write to w anymore if headers sent
		fmt.Printf("Error encoding JSON response: %v\n", err)
	}
}

// RespondJSONWithETag sends a JSON response with an ETag header derived from the payload.
// If the client sends a matching If-None-Match header, it returns 304 Not Modified.
func RespondJSONWithETag(w http.ResponseWriter, r *http.Request, status int, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Error encoding JSON response: %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	etag := fmt.Sprintf(`"%x"`, md5.Sum(data))
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(status)
	w.Write(data)
	w.Write([]byte("\n"))
}

// RespondError sends a JSON error response and logs the error to the provided logger or stdout.
// If logger is nil, it prints to stdout using fmt.Println.
func RespondError(w http.ResponseWriter, logger *strings.Builder, message string, status int) {
	if logger != nil {
		AddToLogMessage(logger, message)
	} else {
		fmt.Println("[Error]", message)
	}
	RespondJSON(w, status, map[string]string{"error": message})
}

// PresignImageURLs generates presigned URLs for a slice of image keys/URLs.
// If a URL is already http/https, it's kept as is.
// If it's a key, it attempts to presign it. S3 failures result in the original key being returned as fallback.
func PresignImageURLs(ctx context.Context, images []string) []string {
	var presignedURLs []string
	for _, img := range images {
		if img == "" {
			continue
		}
		if strings.Contains(img, "amazonaws.com/") {
			parts := strings.SplitN(img, "amazonaws.com/", 2)
			if len(parts) == 2 {
				key := strings.SplitN(parts[1], "?", 2)[0]
				if presigned, err := GetPresignedURL(ctx, key); err == nil {
					presignedURLs = append(presignedURLs, presigned)
					continue
				}
			}
			presignedURLs = append(presignedURLs, img)
		} else if strings.HasPrefix(img, "http") {
			presignedURLs = append(presignedURLs, img)
		} else {
			if url, err := GetPresignedURL(ctx, img); err == nil {
				presignedURLs = append(presignedURLs, url)
			} else {
				presignedURLs = append(presignedURLs, img)
			}
		}
	}
	return presignedURLs
}

// LatencyMiddleware logs the duration of each request
func LatencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		fmt.Printf("[LATENCY] %s %s - %v\n", r.Method, r.URL.Path, duration)
	})
}
