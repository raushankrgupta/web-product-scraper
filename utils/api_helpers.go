package utils

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// RespondJSON sends a JSON response with the given status code and payload.
func RespondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// Fallback error logging if encoding fails, though we can't write to w anymore if headers sent
		slog.Info(fmt.Sprintf("Error encoding JSON response: %v", err))
	}
}

// RespondJSONWithETag sends a JSON response with an ETag header derived from the payload.
// If the client sends a matching If-None-Match header, it returns 304 Not Modified.
//
// Cache-Control is set to `private, no-cache` so clients always revalidate
// against the server (using the ETag) before serving from cache. This is what
// makes mutations like soft-delete visible on the very next read — without it,
// any upstream `Cache-Control: public, max-age=...` header would let the
// client serve a stale payload without ever asking the server. `private` also
// blocks shared caches (CDN/proxies) from storing per-user data.
func RespondJSONWithETag(w http.ResponseWriter, r *http.Request, status int, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Info(fmt.Sprintf("Error encoding JSON response: %v", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	etag := fmt.Sprintf(`"%x"`, md5.Sum(data))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Type", "application/json")

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(status)
	w.Write(data)
	w.Write([]byte("\n"))
}

// RespondError sends a JSON error response and records the message.
//
// When a handler buffer is supplied the message joins that request's
// narrative; when it is nil the message is emitted as its own structured
// record. It used to be a bare fmt.Println, which is why `[Error] Quota
// exceeded` appeared *before* the `[Virtual Try-On API]` header of its own
// request in the production log — the direct print raced the deferred buffer
// flush, and the two could not be correlated.
//
// NOTE: `message` is sent to the client verbatim. Never pass an upstream
// error into it; use a classified, user-safe string and log the detail
// separately.
func RespondError(w http.ResponseWriter, logger *strings.Builder, message string, status int) {
	if logger != nil {
		AddToLogMessage(logger, message)
	} else {
		slog.Warn(message, "status", status)
	}
	RespondJSON(w, status, map[string]string{"error": message})
}

// RespondInternalError handles a server-side failure: the real error is
// logged and alerted, and the client gets `publicMsg` only.
//
// This exists because infrastructure errors are *verbose and specific* —
// during smoke testing a failed upload returned an AWS access key id, a
// bucket request id and an S3 host id straight to the client, and the
// production log shows users being shown a Google AI Studio billing URL the
// same way. The detail belongs in the log; the user needs a next action.
func RespondInternalError(w http.ResponseWriter, r *http.Request, logger *strings.Builder,
	component, publicMsg string, err error, status int) {

	if logger != nil {
		AddToLogMessage(logger, fmt.Sprintf("%s: %v", publicMsg, err))
	}

	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	L(ctx).Error(publicMsg, "component", component, "error", err.Error())
	alert.Errorf(component, publicMsg, err)

	RespondJSON(w, status, map[string]string{"error": publicMsg})
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
