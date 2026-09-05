package utils

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// OpenAI image generation, used as the fallback vendor for a quality tier.
//
// A try-on is an *edit*, not a generation: we hand the model the customer's
// photo plus garment references and ask for one image of the former wearing
// the latter. That is /v1/images/edits, which for the GPT image models accepts
// several reference images in one request.
//
// The prompts are the same ones Gemini gets (individualTryOnPrompt /
// multiPersonTryOnPrompt). They were written to avoid image-model safety
// triggers rather than to suit one vendor, and keeping a single set means the
// two providers cannot drift into producing visibly different results for the
// same request.

const openAIEditsURL = "https://api.openai.com/v1/images/edits"

// openAIImageSize is the output size. Portrait, because a try-on is a picture
// of a person: the square default crops heads and hems, and OpenAI does not
// let the model choose its own aspect the way Gemini does.
const openAIImageSize = "1024x1536"

// OpenAIBreaker guards the OpenAI upstream.
//
// Deliberately a separate instance from GeminiBreaker. Sharing one would mean
// a Gemini outage opening the circuit on the provider that exists precisely to
// cover for a Gemini outage.
var OpenAIBreaker = NewBreaker("openai")

// openAIClient has no timeout of its own — the per-call context carries the
// budget, which for a fallback is whatever is left of the request rather than
// a fresh clock.
var openAIClient = &http.Client{}

// openAIError is a structured error from the API. Kept as a type rather than
// flattened to a string so FailureReason can classify on the code — the
// difference between "we are out of credit" and "the model refused this
// photo" decides whether a retry anywhere is worth attempting.
type openAIError struct {
	Status  int
	Code    string
	Type    string
	Message string
}

func (e *openAIError) Error() string {
	return fmt.Sprintf("openai %d %s/%s: %s", e.Status, e.Type, e.Code, e.Message)
}

// isModerationBlock reports whether OpenAI refused on content grounds.
//
// Worth naming because it is the failure this provider has *more* of than
// Gemini, not less: OpenAI's moderation is stricter about identifiable real
// people, which is the entire subject matter of this app.
func (e *openAIError) isModerationBlock() bool {
	c := strings.ToLower(e.Code + " " + e.Type + " " + e.Message)
	return strings.Contains(c, "moderation_blocked") ||
		strings.Contains(c, "content_policy") ||
		strings.Contains(c, "safety system")
}

// openaiGenerate runs one try-on against OpenAI.
//
// There is no equivalent of the Gemini safety-block retry here. That retry
// re-sends a stripped-down prompt on the theory that the wording tripped a
// classifier; OpenAI's refusals are moderation decisions about the images,
// and a second call with fewer words buys another bill and another refusal.
func openaiGenerate(ctx context.Context, r *resolvedTryOn, quality string) ([]byte, error) {
	if config.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	model, openaiQuality, ok := config.Stars.OpenAIModelFor(quality)
	if !ok {
		return nil, fmt.Errorf("no openai model configured for quality %q", quality)
	}
	if allowed, st := OpenAIBreaker.Allow(); !allowed {
		slog.Warn("openai call rejected locally: circuit is not closed", "state", string(st))
		return nil, fmt.Errorf("openai temporarily unavailable (upstream circuit open)")
	}

	prompt := r.prompt(false)

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	fields := [][2]string{
		{"model", model},
		{"prompt", prompt},
		{"n", "1"},
		{"size", openAIImageSize},
		{"quality", openaiQuality},
		// The least restrictive setting the API offers. The default ("auto")
		// refuses noticeably more try-ons, and every refusal is a paid call
		// that produced nothing.
		{"moderation", "low"},
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
	}

	// Order matters and mirrors the prompt: every customer photo first in
	// person order, then that person's garment references. `image[]` is the
	// repeated-file field the GPT image models take for multi-reference edits.
	//
	// Note there is no input_fidelity field: gpt-image-2 processes every input
	// at high fidelity and rejects the parameter outright.
	idx := 0
	addImage := func(img fetchedImage, name string) error {
		part, err := mw.CreateFormFile("image[]", fmt.Sprintf("%s_%d%s", name, idx, extForMIME(img.mime)))
		if err != nil {
			return err
		}
		idx++
		_, err = part.Write(img.data)
		return err
	}
	for i, p := range r.People {
		if p.Photo != nil {
			if err := addImage(*p.Photo, fmt.Sprintf("person%d", i+1)); err != nil {
				return nil, fmt.Errorf("build request: %w", err)
			}
		}
		for _, g := range p.Garments {
			if err := addImage(g, fmt.Sprintf("garment%d", i+1)); err != nil {
				return nil, fmt.Errorf("build request: %w", err)
			}
		}
	}
	if r.Theme != nil {
		if err := addImage(*r.Theme, "background"); err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIEditsURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+config.OpenAIAPIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	slog.Info("openai request built",
		"label", r.Label, "model", model, "quality", openaiQuality, "images", idx)

	start := time.Now()
	resp, err := openAIClient.Do(req)
	took := time.Since(start)
	if err != nil {
		OpenAIBreaker.RecordFailure()
		alert.Errorf("openai", "request failed", err, "label", r.Label, "model", model)
		return nil, err
	}
	defer resp.Body.Close()

	// 25 MiB matches maxImageBytes: a base64 image is bigger than its bytes,
	// but this is a bound against a broken upstream, not a precise limit.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*maxImageBytes))
	if err != nil {
		OpenAIBreaker.RecordFailure()
		return nil, fmt.Errorf("read openai response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		oerr := parseOpenAIError(resp.StatusCode, raw)
		reportOpenAIError(r.Label, model, quality, oerr, took)
		return nil, oerr
	}

	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		OpenAIBreaker.RecordFailure()
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if len(out.Data) == 0 || out.Data[0].B64JSON == "" {
		OpenAIBreaker.RecordFailure()
		return nil, fmt.Errorf("openai returned no image")
	}

	img, err := base64.StdEncoding.DecodeString(out.Data[0].B64JSON)
	if err != nil {
		OpenAIBreaker.RecordFailure()
		return nil, fmt.Errorf("decode openai image: %w", err)
	}

	// input_tokens is logged on every success on purpose. It is the one number
	// in docs/OPENAI_IMAGE_FALLBACK.md that was an estimate, and it decides
	// whether these tiers are priced correctly — so the first real call should
	// settle it rather than leaving it to be guessed at again.
	slog.Info("openai returned an image",
		"label", r.Label, "model", model, "quality", openaiQuality,
		"bytes", len(img),
		"input_tokens", out.Usage.InputTokens, "output_tokens", out.Usage.OutputTokens,
		"duration_ms", float64(took.Microseconds())/1000)

	OpenAIBreaker.RecordSuccess()
	return img, nil
}

// parseOpenAIError turns an error response body into a typed error, falling
// back to the raw body when it is not the shape we expect (a proxy's HTML
// error page, most often).
func parseOpenAIError(status int, raw []byte) *openAIError {
	var wire struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &wire); err == nil && wire.Error.Message != "" {
		return &openAIError{
			Status:  status,
			Code:    wire.Error.Code,
			Type:    wire.Error.Type,
			Message: wire.Error.Message,
		}
	}
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &openAIError{Status: status, Message: msg}
}

// reportOpenAIError alerts and moves the breaker, mirroring
// reportUpstreamError's split: running out of credit is a different incident
// from a model refusing one photo, and only one of them is an outage.
func reportOpenAIError(label, model, quality string, err *openAIError, took time.Duration) {
	switch {
	case err.isModerationBlock():
		// Content refusals are not an outage and must not open the circuit —
		// doing so would take the provider down for everyone because one
		// person's photo was refused.
		slog.Warn("openai refused on content grounds",
			"label", label, "model", model, "code", err.Code, "message", err.Message)
		alert.Report(alert.Event{
			Level: alert.LevelWarn, Component: "openai",
			Title: "generation refused (moderation)", Err: err, Latency: took,
			Fields: map[string]string{"label": label, "model": model, "quality": quality},
		})
	case IsQuotaError(err):
		OpenAIBreaker.RecordQuotaFailure()
		alert.Report(alert.Event{
			Level: alert.LevelFatal, Component: "openai",
			Title: "quota / credits exhausted", Err: err, Latency: took,
			Fields: map[string]string{"label": label, "model": model, "quality": quality},
		})
	default:
		OpenAIBreaker.RecordFailure()
		alert.Errorf("openai", "generation failed", err, "label", label, "model", model)
	}
}

// extForMIME maps a sniffed MIME type to a file extension. OpenAI reads the
// upload's type from the filename, so "image.bin" is rejected as unsupported.
func extForMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}
