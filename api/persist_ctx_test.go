package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
)

// The regression this guards is the only HTTP 500 in the production log: a
// 666 KB image that Gemini spent 21.5s generating, thrown away by PutObject
// with "context canceled" because the caller had hung up and the upload was
// running on the request context.
func TestPersistCtxSurvivesAbandonedRequest(t *testing.T) {
	config.S3UploadTimeoutSecs = 30

	req := httptest.NewRequest("POST", "/try-on", nil)
	reqCtx, abandonRequest := context.WithCancel(req.Context())
	req = req.WithContext(reqCtx)

	uploadCtx, cancelUpload := persistCtx()
	defer cancelUpload()

	// The caller walks away mid-generation.
	abandonRequest()

	if req.Context().Err() == nil {
		t.Fatal("test setup: request context should be cancelled")
	}
	if err := uploadCtx.Err(); err != nil {
		t.Fatalf("upload context died with the request (%v) — the finished generation would be discarded", err)
	}

	// And it still carries a deadline of its own, so a hung S3 call cannot
	// leak the goroutine.
	deadline, ok := uploadCtx.Deadline()
	if !ok {
		t.Fatal("upload context has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 31*time.Second {
		t.Errorf("deadline is %v away, want ~30s", remaining)
	}
}

func TestPersistCtxUsesConfiguredTimeout(t *testing.T) {
	old := config.S3UploadTimeoutSecs
	defer func() { config.S3UploadTimeoutSecs = old }()

	config.S3UploadTimeoutSecs = 5
	ctx, cancel := persistCtx()
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("no deadline set")
	}
	if remaining := time.Until(deadline); remaining > 6*time.Second {
		t.Errorf("deadline is %v away, want ~5s", remaining)
	}
}
