package utils

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateImageFile(t *testing.T) {
	// 1. Valid JPEG header
	jpegHeader := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	key, mime, err := ValidateImageFile(bytes.NewReader(jpegHeader), "test_uploads")
	if err != nil {
		t.Fatalf("expected valid JPEG, got error: %v", err)
	}
	if mime != "image/jpeg" {
		t.Errorf("expected mime image/jpeg, got %s", mime)
	}
	if !strings.HasPrefix(key, "test_uploads/") || !strings.HasSuffix(key, ".jpg") {
		t.Errorf("unexpected key format: %s", key)
	}

	// 2. Valid PNG header
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	key, mime, err = ValidateImageFile(bytes.NewReader(pngHeader), "test_uploads")
	if err != nil {
		t.Fatalf("expected valid PNG, got error: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("expected mime image/png, got %s", mime)
	}
	if !strings.HasSuffix(key, ".png") {
		t.Errorf("unexpected key format: %s", key)
	}

	// 3. Malicious HTML disguised as image
	htmlPayload := []byte("<html><script>alert('xss')</script></html>")
	_, _, err = ValidateImageFile(bytes.NewReader(htmlPayload), "test_uploads")
	if err == nil {
		t.Fatalf("expected HTML upload to be rejected, but it succeeded")
	}

	// 4. Executable binary disguised as image
	binPayload := []byte{0x7F, 0x45, 0x4C, 0x46, 0x02, 0x01, 0x01, 0x00}
	_, _, err = ValidateImageFile(bytes.NewReader(binPayload), "test_uploads")
	if err == nil {
		t.Fatalf("expected binary upload to be rejected, but it succeeded")
	}
}
