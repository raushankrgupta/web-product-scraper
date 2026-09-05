package utils

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
)

// The provider dispatcher: which vendor renders a try-on, and what happens
// when one of them says no.
//
// The tier is the product. A customer buys "Standard" or "Pro" and is charged
// the tier price; which vendor answers is an operational detail configured
// with OPENAI_ENABLED and IMAGE_PROVIDER_PREFERENCE. That is only safe because
// config/stars.json prices every tier so that *either* vendor clears the
// margin floor, and tools/stars_check fails the build otherwise — otherwise a
// fallback would be a loss-making generation waiting for an outage.

// GenerationError carries the per-provider history of a failed generation.
//
// A single error cannot express "Gemini refused on safety, then OpenAI timed
// out", and that sequence is the entire point of having a fallback — so it has
// to be the thing the post-mortem records and someone can count later.
//
// It unwraps to the last error, so classifyGenErr, FailureReason and every
// errors.As on the way out keep working unchanged.
type GenerationError struct {
	Attempts []models.TryOnAttempt
	Err      error
}

func (e *GenerationError) Error() string { return e.Err.Error() }
func (e *GenerationError) Unwrap() error { return e.Err }

// AttemptsOf returns the per-provider history behind an error, or nil when the
// failure happened before any provider was called.
func AttemptsOf(err error) []models.TryOnAttempt {
	var ge *GenerationError
	if errors.As(err, &ge) {
		return ge.Attempts
	}
	return nil
}

// providerOrder is which vendors to try, in order.
//
// OpenAI is absent entirely unless it is switched on and has a key, so a
// misconfigured preference can never produce a request to an endpoint we
// cannot authenticate to. config.LoadConfig already downgrades
// IMAGE_PROVIDER_PREFERENCE=openai to gemini when OpenAI is off, and this is
// the second gate on the same rule.
func providerOrder() []string {
	if !config.OpenAIEnabled || config.OpenAIAPIKey == "" {
		return []string{"gemini"}
	}
	if config.ImageProviderPreference == "openai" {
		return []string{"openai", "gemini"}
	}
	return []string{"gemini", "openai"}
}

// shouldTryNextProvider decides whether a different vendor could plausibly
// succeed where this one failed.
//
// This is the function that decides whether the fallback pays for itself.
// Trying the second vendor on everything would roughly double the cost and the
// latency of a failing try-on for almost no extra successes, because the
// largest category of failure is a content refusal — and a content refusal is
// about the customer's photo, not about the vendor. OpenAI's moderation is
// *stricter* than Gemini's on identifiable real people, so falling back on
// image_safety buys two refusals, two bills and forty seconds instead of one
// honest 422 in twenty.
//
// The rule is therefore: fall back when the failure was about *us reaching the
// model*, never when it was about *what we sent it*.
func shouldTryNextProvider(err error) bool {
	switch FailureReason(err) {
	case "quota_exhausted", // our billing with that vendor is dead; the other one isn't
		"circuit_open",          // we have already decided that vendor is down
		"upstream_error",        // transport-level: a different vendor is a genuinely different roll
		"timeout",               // subject to the budget check below
		"text_instead_of_image": // a model quirk, not a policy decision
		return true

	// Content refusals and our own bugs. Explicit rather than a default so
	// that adding a new FailureReason code forces a decision here instead of
	// silently inheriting "retry on another vendor's bill".
	case "safety", "image_safety", "prohibited_content", "recitation", "spii",
		"blocked_other", "no_image", "image_other", "max_tokens",
		"insufficient_input_images", "misconfigured":
		return false

	default:
		return false
	}
}

// generateTryOn resolves the images once, then walks the provider list.
func generateTryOn(ctx context.Context, label string, scene TryOnScene, people []PersonTryOnData, quality string) ([]byte, error) {
	quality = config.Stars.NormaliseQuality(quality)

	// Resolved before any provider is chosen: a fallback must not re-download
	// the customer photo and every garment reference out of a budget that is
	// already nearly spent.
	resolved, err := resolveTryOn(ctx, label, scene, people)
	if err != nil {
		return nil, err
	}

	providers := providerOrder()
	var attempts []models.TryOnAttempt
	var lastErr error

	for i, provider := range providers {
		if i > 0 {
			// Every check below is about the *second* attempt only; the first
			// one always runs.
			if !shouldTryNextProvider(lastErr) {
				slog.Info("not falling back — the failure is not one a different vendor fixes",
					"label", label, "reason", FailureReason(lastErr), "skipped", provider)
				break
			}
			if !hasBudgetForProvider(ctx, attempts) {
				// Starting a fallback we cannot finish turns an honest 422
				// into a 504 and bills for the privilege.
				slog.Warn("not falling back — not enough time left in the budget",
					"label", label, "skipped", provider)
				break
			}
			slog.Warn("falling back to the next provider",
				"label", label, "from", providers[i-1], "to", provider, "reason", FailureReason(lastErr))
		}

		model := modelIDFor(provider, quality)
		start := time.Now()
		img, genErr := runProvider(ctx, provider, resolved, quality)
		took := time.Since(start)

		attempt := models.TryOnAttempt{
			Provider:   provider,
			Model:      model,
			DurationMS: took.Milliseconds(),
		}
		if genErr == nil {
			attempt.Reason = "ok"
			attempts = append(attempts, attempt)
			if len(attempts) > 1 {
				slog.Info("try-on succeeded on a fallback provider",
					"label", label, "provider", provider, "attempts", len(attempts))
			}
			return img, nil
		}

		attempt.Reason = FailureReason(genErr)
		attempt.FinishReason = FinishReasonOf(genErr)
		attempt.RawError = genErr.Error()
		attempts = append(attempts, attempt)
		lastErr = genErr
	}

	return nil, &GenerationError{Attempts: attempts, Err: lastErr}
}

// runProvider dispatches to one vendor.
func runProvider(ctx context.Context, provider string, r *resolvedTryOn, quality string) ([]byte, error) {
	switch provider {
	case "gemini":
		return geminiGenerate(ctx, r, quality)
	case "openai":
		// Its own budget, but never more than what is left of the caller's.
		// A fallback runs on the remainder of a request the first provider
		// has already spent most of, so OPENAI_TIMEOUT_SECS is a ceiling
		// rather than an allowance.
		budget := time.Duration(config.OpenAITimeoutSecs) * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < budget {
				budget = remaining
			}
		}
		if budget <= 0 {
			return nil, context.DeadlineExceeded
		}
		callCtx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		return openaiGenerate(callCtx, r, quality)
	default:
		return nil, fmt.Errorf("unknown image provider %q", provider)
	}
}

// modelIDFor is the literal model id a provider would use for a tier, for the
// failure record. "" when that provider has nothing configured for the tier.
func modelIDFor(provider, quality string) string {
	switch provider {
	case "gemini":
		return config.Stars.GeminiModelFor(quality)
	case "openai":
		model, openaiQuality, ok := config.Stars.OpenAIModelFor(quality)
		if !ok {
			return ""
		}
		return model + " (" + openaiQuality + ")"
	}
	return ""
}

// hasBudgetForProvider reports whether the context has room for another
// attempt about as long as the longest one so far.
//
// Same reasoning as hasBudgetForRetry, one level up: the client is waiting on
// a deadline of its own, and a fallback that overruns it converts a failure we
// could explain into a timeout we cannot.
func hasBudgetForProvider(ctx context.Context, attempts []models.TryOnAttempt) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true // no deadline: only ever the case in tests
	}
	longest := time.Duration(0)
	for _, a := range attempts {
		if d := time.Duration(a.DurationMS) * time.Millisecond; d > longest {
			longest = d
		}
	}
	return time.Until(deadline) > longest+2*time.Second
}
