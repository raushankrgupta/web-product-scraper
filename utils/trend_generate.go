package utils

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
)

// Trend generation.
//
// Deliberately a thin layer over the machinery the try-on path already uses —
// the same client, the same permissive safety settings, the same breaker, the
// same safety-block retry, the same error classification. A trend is a
// different prompt and a different set of images; it is not a different
// relationship with the upstream, and giving it one would mean two places to
// fix the next time Gemini changes how it refuses something.

// TrendImageRef is one image to send, with the label that introduces it.
type TrendImageRef struct {
	// URL is presigned or public. Fetched, not passed through.
	URL string
	// Label is the text part emitted immediately before the image, e.g.
	// "Subject 1 (Gender: female) — the person who must appear in the result:".
	// Empty sends the image with no preamble.
	Label string
	// Essential marks an image the generation cannot proceed without. A
	// subject photo is essential; a style reference is not.
	Essential bool
	// Optional marks an image dropped from the terse retry. Style references
	// and background plates go first when a safety block forces a second,
	// smaller attempt.
	DropOnRetry bool
}

// TrendGenSpec is one fully resolved generation request.
type TrendGenSpec struct {
	// Label identifies this run in logs, alerts and breaker records.
	Label string
	// Prompt is the rendered template (see RenderTrendPrompt).
	Prompt string
	// TersePrompt is the shorter alternative used once after a safety block.
	// Empty means a generic fallback is derived from Prompt.
	TersePrompt string

	Images []TrendImageRef

	// Provider is the vendor the trend was authored for: "gemini" or
	// "openai". Empty means gemini.
	Provider string
	// Quality resolves the model through config/stars.json, exactly as a
	// try-on does, which is what keeps a trend's price tied to its cost.
	Quality string
	// ModelOverride pins a literal model id on the trend's own Provider. It is
	// never sent to the fallback vendor — a model id means nothing to the
	// other company's API.
	ModelOverride string

	// Outputs is how many images to produce. Each is a separate upstream call.
	Outputs int
}

// TrendGenResult is what a generation produced, and who produced it.
type TrendGenResult struct {
	Images [][]byte
	// Provider and Model name the vendor and model that served the first
	// image, which is not always the trend's configured one: a transport
	// failure on the primary falls back to the other vendor.
	Provider string
	Model    string
}

// Providers.
const (
	ProviderGemini = "gemini"
	ProviderOpenAI = "openai"
)

// maxTrendConcurrency bounds parallel generations inside one request.
//
// Two, not more: every extra concurrent call is another full-price image
// against the same deadline, and a trend that asks for four at once would
// four times the load one user can put on the upstream — which is exactly the
// shape that trips the shared rate limit and starts failing everyone else's
// try-ons.
const maxTrendConcurrency = 2

// ErrProviderUnavailable is a trend asking for a vendor this server cannot
// call. Classified as a misconfiguration so the admin sees it for what it is.
var ErrProviderUnavailable = fmt.Errorf("misconfigured: the trend's image provider is not enabled on this server")

// NormaliseTrendProvider maps whatever a trend document says onto a known
// provider, defaulting to gemini.
func NormaliseTrendProvider(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), ProviderOpenAI) {
		return ProviderOpenAI
	}
	return ProviderGemini
}

// TrendModelMismatch reports a model override that belongs to the other
// vendor — the exact mistake that sent "gpt-image-…" to Google and came back
// as a 404. Returns "" when the pairing is fine.
func TrendModelMismatch(provider, override string) string {
	model := strings.ToLower(strings.TrimSpace(override))
	if model == "" {
		return ""
	}
	openaiLooking := strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "dall-e") ||
		strings.HasPrefix(model, "chatgpt-")
	geminiLooking := strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "imagen-") ||
		strings.HasPrefix(model, "models/")

	switch NormaliseTrendProvider(provider) {
	case ProviderGemini:
		if openaiLooking {
			return fmt.Sprintf("%q is an OpenAI model but the trend's provider is Gemini", override)
		}
	case ProviderOpenAI:
		if geminiLooking {
			return fmt.Sprintf("%q is a Gemini model but the trend's provider is OpenAI", override)
		}
	}
	return ""
}

// providerAvailable reports whether this server can call a vendor at all.
func providerAvailable(provider string) bool {
	switch provider {
	case ProviderOpenAI:
		return config.OpenAIEnabled && config.OpenAIAPIKey != ""
	default:
		return config.GeminiAPIKey != ""
	}
}

// GenerateTrendImage runs one trend generation.
//
// The trend's own provider goes first, with its model override. The other
// vendor is tried only when the first failure was about reaching the model
// rather than about what was sent — the same rule, and the same function,
// the try-on fallback uses (shouldTryNextProvider) — and only on that vendor's
// default model for the tier.
//
// A partial result is a success: if three of four outputs come back, the user
// gets three images rather than an error, because they are charged per run and
// an error here would refund a generation that mostly worked.
func GenerateTrendImage(ctx context.Context, spec TrendGenSpec) (TrendGenResult, error) {
	var result TrendGenResult

	primary := NormaliseTrendProvider(spec.Provider)
	if !providerAvailable(primary) {
		// Deliberately no silent fallback: an admin who authored a trend for
		// one vendor's look should find out it is not running, not receive a
		// different look from the other vendor with no indication why.
		return result, fmt.Errorf("%w (%s)", ErrProviderUnavailable, primary)
	}
	if problem := TrendModelMismatch(primary, spec.ModelOverride); problem != "" {
		return result, fmt.Errorf("misconfigured: %s", problem)
	}

	providers := []string{primary}
	if secondary := otherProvider(primary); providerAvailable(secondary) {
		providers = append(providers, secondary)
	}

	fetched, err := fetchTrendImages(ctx, spec)
	if err != nil {
		return result, err
	}

	outputs := spec.Outputs
	if outputs < 1 {
		outputs = 1
	}

	type outcome struct {
		img      []byte
		provider string
		model    string
		err      error
	}
	outcomes := make([]outcome, outputs)

	sem := make(chan struct{}, maxTrendConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < outputs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			label := spec.Label
			if outputs > 1 {
				label = fmt.Sprintf("%s [%d/%d]", spec.Label, idx+1, outputs)
			}
			img, provider, model, err := generateTrendOnce(ctx, label, spec, fetched, providers)
			outcomes[idx] = outcome{img, provider, model, err}
		}(i)
	}
	wg.Wait()

	var firstErr error
	for _, o := range outcomes {
		if o.err != nil {
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		if len(o.img) == 0 {
			continue
		}
		if result.Provider == "" {
			result.Provider, result.Model = o.provider, o.model
		}
		result.Images = append(result.Images, o.img)
	}

	if len(result.Images) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no content generated")
		}
		return result, firstErr
	}
	if firstErr != nil {
		slog.Warn("trend generation partially failed",
			"label", spec.Label, "wanted", outputs, "got", len(result.Images), "error", firstErr.Error())
	}
	return result, nil
}

// generateTrendOnce produces one image, walking the provider list.
func generateTrendOnce(ctx context.Context, label string, spec TrendGenSpec,
	fetched []fetchedTrendImage, providers []string) ([]byte, string, string, error) {

	var attempts []models.TryOnAttempt
	var lastErr error

	for i, provider := range providers {
		if i > 0 {
			if !shouldTryNextProvider(lastErr) {
				break
			}
			if !hasBudgetForProvider(ctx, attempts) {
				slog.Warn("trend not falling back — not enough time left", "label", label, "skipped", provider)
				break
			}
			slog.Warn("trend falling back to the next provider",
				"label", label, "from", providers[i-1], "to", provider, "reason", FailureReason(lastErr))
		}

		// The override belongs to the trend's own vendor only.
		override := ""
		if i == 0 {
			override = spec.ModelOverride
		}

		start := time.Now()
		var (
			img   []byte
			model string
			err   error
		)
		switch provider {
		case ProviderOpenAI:
			img, model, err = trendViaOpenAI(ctx, label, spec, fetched, override)
		default:
			img, model, err = trendViaGemini(ctx, label, spec, fetched, override)
		}
		attempts = append(attempts, models.TryOnAttempt{
			Provider: provider, Model: model, DurationMS: time.Since(start).Milliseconds(),
		})
		if err == nil {
			return img, provider, model, nil
		}
		lastErr = err
	}
	return nil, "", "", lastErr
}

// trendViaGemini is one Gemini attempt, with the terse retry after a block.
func trendViaGemini(ctx context.Context, label string, spec TrendGenSpec,
	fetched []fetchedTrendImage, override string) ([]byte, string, error) {

	if err := guardBreaker(); err != nil {
		return nil, "", err
	}
	model, err := newTrendModel(ctx, spec.Quality, override)
	if err != nil {
		return nil, "", err
	}

	buildParts := func(terse bool) []genai.Part {
		prompt := spec.Prompt
		if terse {
			prompt = spec.TersePrompt
			if prompt == "" {
				prompt = terseFallbackPrompt(len(fetched))
			}
		}
		parts := []genai.Part{genai.Text(prompt)}
		for _, img := range fetched {
			if terse && img.ref.DropOnRetry {
				continue
			}
			if !terse && img.ref.Label != "" {
				parts = append(parts, genai.Text(img.ref.Label))
			}
			parts = append(parts, genai.ImageData(img.mime, img.data))
		}
		return parts
	}

	out, err := runGemini(ctx, model, label, buildParts(false), func() []genai.Part {
		return buildParts(true)
	})
	return out, model.Name, err
}

// trendViaOpenAI is one OpenAI attempt.
//
// OpenAI's edits endpoint takes images and one prompt, with no text between
// the images. The per-image labels Gemini receives inline ("Style reference —
// match this look…") are therefore folded into the prompt as a numbered list,
// in the order the images are attached, so the model still knows which
// picture is the person and which is only a style plate.
func trendViaOpenAI(ctx context.Context, label string, spec TrendGenSpec,
	fetched []fetchedTrendImage, override string) ([]byte, string, error) {

	quality := config.Stars.NormaliseQuality(spec.Quality)
	model, openaiQuality, ok := config.Stars.OpenAIModelFor(quality)
	if override != "" {
		model = override
	} else if !ok {
		return nil, "", fmt.Errorf("misconfigured: no openai model configured for quality %q", quality)
	}

	budget := time.Duration(config.OpenAITimeoutSecs) * time.Second
	if deadline, has := ctx.Deadline(); has {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return nil, model, context.DeadlineExceeded
	}
	callCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	images := make([]openAIInputImage, 0, len(fetched))
	var legend strings.Builder
	for i, img := range fetched {
		images = append(images, openAIInputImage{
			img:  fetchedImage{mime: img.mime, data: img.data},
			name: fmt.Sprintf("ref%d", i+1),
		})
		if img.ref.Label != "" {
			fmt.Fprintf(&legend, "\nImage %d: %s", i+1, strings.TrimSuffix(img.ref.Label, ":"))
		}
	}
	prompt := spec.Prompt
	if legend.Len() > 0 {
		prompt += "\n\nREFERENCE IMAGES, in the order attached:" + legend.String()
	}

	out, err := openaiEdit(callCtx, label, prompt, model, openaiQuality, quality, images)
	return out, model, err
}

func otherProvider(provider string) string {
	if provider == ProviderOpenAI {
		return ProviderGemini
	}
	return ProviderOpenAI
}

// newTrendModel is newImageModel with an escape hatch for a per-trend model id.
//
// It still goes through permissiveSafetySettings — every path to the upstream
// must, which is the whole reason newImageModel is a funnel — and the resolved
// quality is still recorded, so the run says which tier was billed even when
// the model was not the tier's usual one.
func newTrendModel(ctx context.Context, quality, override string) (*imageModel, error) {
	if override == "" {
		return newImageModel(ctx, quality)
	}
	client, err := getGeminiClient(ctx)
	if err != nil {
		return nil, err
	}
	quality = config.Stars.NormaliseQuality(quality)
	m := client.GenerativeModel(override)
	m.SafetySettings = permissiveSafetySettings()
	return &imageModel{GenerativeModel: m, Name: override, Quality: quality}, nil
}

type fetchedTrendImage struct {
	ref  TrendImageRef
	data []byte
	mime string
}

// fetchTrendImages downloads everything the generation needs, once.
//
// A non-essential image that fails to fetch is dropped with a warning rather
// than failing the run: a style reference whose S3 signature has expired
// should not sink a generation that still has the user's photo. An essential
// one failing is fatal, and fatal *here* — before the upstream call — so the
// star hold is refunded without us paying for an image we could not assemble.
func fetchTrendImages(ctx context.Context, spec TrendGenSpec) ([]fetchedTrendImage, error) {
	out := make([]fetchedTrendImage, 0, len(spec.Images))
	for _, ref := range spec.Images {
		if ref.URL == "" {
			if ref.Essential {
				return nil, fmt.Errorf("required image is missing")
			}
			continue
		}
		data, mime, err := fetchImageLogged(ctx, spec.Label, ref.URL)
		if err != nil {
			if ref.Essential {
				return nil, fmt.Errorf("could not fetch a required image: %w", err)
			}
			continue
		}
		out = append(out, fetchedTrendImage{ref: ref, data: data, mime: mime})
	}
	return out, nil
}

// terseFallbackPrompt is the stripped-down retry for a trend whose admin did
// not write one.
//
// Fewer words mean fewer trigger surfaces, which is the entire reason the
// retry exists — the try-on path learned the same lesson. It says nothing
// about style because it cannot know the trend's style; it only asks for the
// one thing every trend needs, which is often enough to get through.
func terseFallbackPrompt(images int) string {
	if images <= 1 {
		return "Create a single stylised artistic portrait based on the reference photograph. " +
			"Keep the person's face and features recognisable."
	}
	return fmt.Sprintf("Create a single stylised artistic image based on the %d reference photographs. "+
		"Keep each person's face and features recognisable.", images)
}

// TrendTimeout is how long one trend generation may take.
//
// Falls back to the quality tier's multi-person budget rather than its single
// budget: a trend routinely sends a subject photo, a background plate and one
// or two style references, which is the heavier shape.
func TrendTimeout(quality string, configured int) time.Duration {
	if configured > 0 {
		return time.Duration(configured) * time.Second
	}
	if m, ok := config.Stars.Model(quality); ok && m.MultiTimeoutSecs > 0 {
		return time.Duration(m.MultiTimeoutSecs) * time.Second
	}
	return time.Duration(config.GeminiMultiTimeoutSecs) * time.Second
}
