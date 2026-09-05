package utils

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
)

func TestShouldTryNextProvider(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Reaching the model failed. A different vendor is a different roll.
		{"quota exhausted", errors.New("Error 429: RESOURCE_EXHAUSTED"), true},
		{"circuit open", ErrUpstreamUnavailable, true},
		{"transport error", errors.New("connection reset by peer"), true},
		{"timeout", context.DeadlineExceeded, true},
		{"text instead of image", errors.New("model returned text instead of an image: sorry"), true},

		// What we sent was refused. This is the expensive mistake to get
		// wrong: OpenAI's moderation is stricter than Gemini's on photos of
		// real people, so a fallback here buys two refusals and two bills.
		{"image safety", &blockError{Reason: 11}, false},
		{"prohibited content", &blockError{Reason: 14}, false},
		{"recitation", &blockError{Reason: 17}, false},
		{"openai moderation", &openAIError{Status: 400, Code: "moderation_blocked"}, false},

		// Our own bug. A second vendor cannot fetch an image we failed to fetch.
		{"missing inputs", errors.New("not enough images fetched (photos=0, garments=0)"), false},
		{"misconfigured", errors.New("GEMINI_API_KEY is not set"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldTryNextProvider(c.err); got != c.want {
				t.Errorf("shouldTryNextProvider(%v) = %v, want %v (reason %q)",
					c.err, got, c.want, FailureReason(c.err))
			}
		})
	}
}

// A content refusal must never trigger a second paid call, whichever vendor
// produced it and whichever direction the preference points.
func TestContentRefusalsNeverFallBack(t *testing.T) {
	refusals := []error{
		&blockError{Reason: 3},  // SAFETY
		&blockError{Reason: 8},  // PROHIBITED_CONTENT
		&blockError{Reason: 9},  // SPII
		&blockError{Reason: 11}, // IMAGE_SAFETY
		&blockError{Reason: 14}, // IMAGE_PROHIBITED_CONTENT
		&openAIError{Status: 400, Message: "Your request was rejected by our safety system"},
		&openAIError{Status: 400, Code: "content_policy_violation"},
	}
	for _, err := range refusals {
		if shouldTryNextProvider(err) {
			t.Errorf("would pay a second provider for a content refusal: %v (reason %q)",
				err, FailureReason(err))
		}
	}
}

func TestProviderOrder(t *testing.T) {
	origEnabled, origKey, origPref := config.OpenAIEnabled, config.OpenAIAPIKey, config.ImageProviderPreference
	t.Cleanup(func() {
		config.OpenAIEnabled, config.OpenAIAPIKey, config.ImageProviderPreference = origEnabled, origKey, origPref
	})

	cases := []struct {
		name    string
		enabled bool
		key     string
		pref    string
		want    []string
	}{
		{"disabled", false, "sk-test", "gemini", []string{"gemini"}},
		{"enabled without a key", true, "", "gemini", []string{"gemini"}},
		{"enabled, prefer gemini", true, "sk-test", "gemini", []string{"gemini", "openai"}},
		{"enabled, prefer openai", true, "sk-test", "openai", []string{"openai", "gemini"}},
		// A preference for a disabled provider must not produce a call we
		// cannot authenticate. LoadConfig downgrades it; this is the second gate.
		{"prefer openai while disabled", false, "sk-test", "openai", []string{"gemini"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config.OpenAIEnabled, config.OpenAIAPIKey, config.ImageProviderPreference = c.enabled, c.key, c.pref
			got := providerOrder()
			if len(got) != len(c.want) {
				t.Fatalf("providerOrder() = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("providerOrder() = %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestHasBudgetForProvider(t *testing.T) {
	attempts := []models.TryOnAttempt{{DurationMS: 20_000}}

	t.Run("no deadline", func(t *testing.T) {
		if !hasBudgetForProvider(context.Background(), attempts) {
			t.Error("a context with no deadline should always have room")
		}
	})

	t.Run("plenty of time", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if !hasBudgetForProvider(ctx, attempts) {
			t.Error("60s left, 20s attempt: should have room")
		}
	})

	// The important one. Starting a fallback we cannot finish turns an honest
	// 422 into a 504 and bills for the privilege.
	t.Run("not enough time", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if hasBudgetForProvider(ctx, attempts) {
			t.Error("5s left, 20s attempt: should refuse to start")
		}
	})
}

func TestGenerationErrorUnwrapsForClassification(t *testing.T) {
	inner := &blockError{Reason: 11}
	err := &GenerationError{
		Attempts: []models.TryOnAttempt{{Provider: "gemini", Reason: "image_safety"}},
		Err:      inner,
	}

	// Wrapping must not blind the classifier, or every fallback failure would
	// be reported to the user as a generic 500.
	if got := FailureReason(err); got != "image_safety" {
		t.Errorf("FailureReason through GenerationError = %q, want image_safety", got)
	}
	if got := FinishReasonOf(err); got != "IMAGE_SAFETY(11)" {
		t.Errorf("FinishReasonOf through GenerationError = %q, want IMAGE_SAFETY(11)", got)
	}
	if !isSafetyBlock(err) {
		t.Error("isSafetyBlock did not see through GenerationError")
	}
	if n := len(AttemptsOf(err)); n != 1 {
		t.Errorf("AttemptsOf returned %d attempts, want 1", n)
	}
}

func TestAttemptsOfPlainError(t *testing.T) {
	if got := AttemptsOf(errors.New("boom")); got != nil {
		t.Errorf("AttemptsOf(plain error) = %v, want nil", got)
	}
}

func TestOpenAIErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  *openAIError
		want string
	}{
		{"moderation", &openAIError{Status: 400, Code: "moderation_blocked"}, "prohibited_content"},
		{"safety prose", &openAIError{Status: 400, Message: "rejected by our safety system"}, "prohibited_content"},
		{"rate limited", &openAIError{Status: 429, Code: "rate_limit_exceeded", Message: "429 too many requests"}, "quota_exhausted"},
		{"server error", &openAIError{Status: http.StatusBadGateway, Message: "bad gateway"}, "upstream_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FailureReason(c.err); got != c.want {
				t.Errorf("FailureReason(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// Both tiers must have a fallback that clears the hard margin floor on its
// own, because the customer is charged the tier price whichever vendor
// answers. tools/stars_check enforces this in CI; this is the unit-level
// guard so a stars.json edit fails here first.
func TestConfiguredFallbacksClearTheMarginFloor(t *testing.T) {
	if err := config.LoadStars(); err != nil {
		t.Fatalf("load stars: %v", err)
	}
	seen := 0
	for _, m := range config.Stars.Margins() {
		if m.Provider != "openai" {
			continue
		}
		seen++
		if m.BelowMin {
			t.Errorf("%s/%s on openai (%s) is below the margin floor: %d stars, needs %d",
				m.Type, m.Quality, m.Model, m.Stars, m.MinStars)
		}
	}
	if seen == 0 {
		t.Skip("no openai fallback configured for any tier")
	}
}
