package api

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifyGenErr(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			// Verbatim from the production log.
			name:       "gemini quota exhausted",
			err:        errors.New("failed to generate content: googleapi: Error 429: You exceeded your current quota... your prepayment credits are depleted"),
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "generation timeout",
			err:        errors.New("failed to generate content: context deadline exceeded"),
			wantStatus: http.StatusGatewayTimeout,
		},
		{
			name:       "safety block with finish reason",
			err:        errors.New("no content generated (blocked, finish_reason=IMAGE_SAFETY)"),
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "prompt block",
			err:        errors.New("no content generated (prompt blocked: OTHER)"),
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "text instead of image",
			err:        errors.New("model returned text instead of an image: I'm sorry"),
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "circuit breaker open",
			err:        errors.New("try-on is temporarily unavailable (upstream circuit open)"),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "images could not be fetched",
			err:        errors.New("not enough images fetched (person=true, garments=0)"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "anything else",
			err:        errors.New("some unexpected failure"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := classifyGenErr(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if msg == "" {
				t.Error("a user-facing message is required")
			}
		})
	}
}

// The client must never see the upstream error text — that is how users ended
// up reading a Google AI Studio billing URL.
func TestClassifyGenErrDoesNotLeakUpstreamDetail(t *testing.T) {
	raw := "googleapi: Error 429: quota exceeded, see https://aistudio.google.com/app/billing?project=secret-123"
	_, msg := classifyGenErr(errors.New(raw))

	for _, leak := range []string{"aistudio.google.com", "googleapi", "secret-123", "429"} {
		if contains(msg, leak) {
			t.Errorf("user-facing message leaked %q: %s", leak, msg)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

func TestClassifyGenErrNil(t *testing.T) {
	status, msg := classifyGenErr(nil)
	if status != http.StatusOK || msg != "" {
		t.Errorf("classifyGenErr(nil) = (%d, %q), want (200, \"\")", status, msg)
	}
}
