package utils

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// AllowedImageMIMEs defines the whitelist of valid image MIME types
var AllowedImageMIMEs = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// ValidateImageFile inspects the magic bytes of an uploaded file,
// checks against AllowedImageMIMEs, and generates a clean UUID-based S3 object key.
func ValidateImageFile(file io.ReadSeeker, prefix string) (cleanKey string, detectedMIME string, err error) {
	// Read first 512 bytes for MIME detection
	buf := make([]byte, 512)
	n, readErr := file.Read(buf)
	if readErr != nil && readErr != io.EOF {
		return "", "", fmt.Errorf("failed to read file header: %w", readErr)
	}

	detectedMIME = http.DetectContentType(buf[:n])
	ext, ok := AllowedImageMIMEs[detectedMIME]
	if !ok {
		return "", detectedMIME, fmt.Errorf("invalid image format %q: only JPEG, PNG, and WebP are allowed", detectedMIME)
	}

	// Rewind file pointer
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", detectedMIME, fmt.Errorf("failed to reset file reader: %w", err)
	}

	// Generate safe UUID object key
	cleanKey = fmt.Sprintf("%s/%s%s", strings.Trim(prefix, "/"), uuid.New().String(), ext)
	return cleanKey, detectedMIME, nil
}

// GeneratedImageName returns the file extension and Content-Type to store a
// model-generated image under, read from the bytes themselves.
//
// Generated images were previously stored as ".jpg" with a hardcoded
// "image/jpeg" regardless of what came back, and what comes back is not always
// JPEG: gemini-2.5-flash-image returns PNG. That put PNG bytes behind a .jpg
// key and, worse, served them from S3 with a Content-Type that contradicts
// them. Clients that sniff the body were unaffected, which is why it went
// unnoticed; anything trusting the declared type — a CDN transform, a browser
// download, an image pipeline — is not so forgiving.
//
// Falls back to JPEG for anything unrecognised, matching imageMIME.
func GeneratedImageName(data []byte) (ext, contentType string) {
	contentType = imageMIME(data)
	return extForMIME(contentType), contentType
}
