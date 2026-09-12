package utils

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
)

func TestNormaliseTrendProvider(t *testing.T) {
	for in, want := range map[string]string{
		"openai": ProviderOpenAI, " OpenAI ": ProviderOpenAI,
		"gemini": ProviderGemini, "": ProviderGemini, "anthropic": ProviderGemini,
	} {
		if got := NormaliseTrendProvider(in); got != want {
			t.Errorf("NormaliseTrendProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrendModelMismatch(t *testing.T) {
	// The exact configuration that produced the 404: an OpenAI model id on a
	// trend whose provider was still Gemini.
	if TrendModelMismatch("gemini", "gpt-image-2.5-sunburst") == "" {
		t.Error("an OpenAI model on a Gemini trend must be reported")
	}
	if TrendModelMismatch("openai", "gemini-3-pro-image-preview") == "" {
		t.Error("a Gemini model on an OpenAI trend must be reported")
	}
	for _, ok := range [][2]string{
		{"openai", "gpt-image-2.5-sunburst"}, {"gemini", "gemini-2.5-flash-image"},
		{"gemini", ""}, {"openai", ""},
	} {
		if msg := TrendModelMismatch(ok[0], ok[1]); msg != "" {
			t.Errorf("TrendModelMismatch(%q, %q) = %q, want no complaint", ok[0], ok[1], msg)
		}
	}
}

func TestFailureReason_UnknownModelIsMisconfigured(t *testing.T) {
	// Must not be "upstream_error": that reason triggers the fallback to the
	// other vendor, which would hide a mistyped model id behind a result from
	// a different company's model.
	google := errors.New("failed to generate content: googleapi: Error 404: models/gpt-image-2.5-sunburst " +
		"is not found for API version v1beta, or is not supported for generateContent.")
	if got := FailureReason(google); got != "misconfigured" {
		t.Errorf("Google unknown-model 404 classified %q, want misconfigured", got)
	}
	if shouldTryNextProvider(google) {
		t.Error("an unknown model must not fall back to the other vendor")
	}

	openai := &openAIError{Status: 400, Type: "invalid_request_error", Code: "invalid_value",
		Message: "Invalid value: 'gpt-image-9'. Supported values are: 'gpt-image-1' … for parameter 'model'."}
	if got := FailureReason(openai); got != "misconfigured" {
		t.Errorf("OpenAI invalid-model 400 classified %q, want misconfigured", got)
	}

	notFound := &openAIError{Status: 404, Code: "model_not_found", Message: "The model `gpt-x` does not exist."}
	if got := FailureReason(notFound); got != "misconfigured" {
		t.Errorf("OpenAI model_not_found classified %q, want misconfigured", got)
	}

	// And a genuine transport failure still falls back.
	if got := FailureReason(&openAIError{Status: 503, Message: "overloaded"}); got != "upstream_error" {
		t.Errorf("a 503 classified %q, want upstream_error", got)
	}
}

func TestGenerateTrendImage_RefusesADisabledProvider(t *testing.T) {
	// No silent swap to the other vendor: a trend authored for OpenAI on a
	// server with OpenAI switched off must say so, not return a Gemini image.
	prevEnabled, prevKey := config.OpenAIEnabled, config.OpenAIAPIKey
	defer func() { config.OpenAIEnabled, config.OpenAIAPIKey = prevEnabled, prevKey }()
	config.OpenAIEnabled, config.OpenAIAPIKey = false, ""

	_, err := GenerateTrendImage(context.Background(), TrendGenSpec{Provider: "openai", Prompt: "x"})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if FailureReason(err) != "misconfigured" {
		t.Errorf("classified %q, want misconfigured", FailureReason(err))
	}
}

func TestGenerateTrendImage_RefusesAMismatchedOverrideBeforeCalling(t *testing.T) {
	prevKey := config.GeminiAPIKey
	defer func() { config.GeminiAPIKey = prevKey }()
	config.GeminiAPIKey = "test-key"

	// Images is empty, so if validation did not stop it first this would fail
	// later for an unrelated reason — the error text pins which check fired.
	_, err := GenerateTrendImage(context.Background(), TrendGenSpec{
		Provider: "gemini", ModelOverride: "gpt-image-2.5-sunburst", Prompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "OpenAI model") {
		t.Fatalf("err = %v, want the provider/model mismatch", err)
	}
}
