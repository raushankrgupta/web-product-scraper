package utils

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"
	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
	"google.golang.org/api/option"
)

// imageModel pairs a configured Gemini model with the name it was built from.
// genai.GenerativeModel keeps its model name unexported, and every log line,
// alert and breaker record here needs to say which model produced the result —
// with two quality tiers in play, "generation failed" is not actionable
// without knowing whether it was the cheap one or the expensive one.
type imageModel struct {
	*genai.GenerativeModel
	Name    string // literal Gemini model id, e.g. gemini-2.5-flash-image
	Quality string // config key, e.g. flash | pro
}

var (
	geminiOnce   sync.Once
	geminiClient *genai.Client
	geminiErr    error
)

// getGeminiClient returns a process-wide genai client. It used to be
// constructed and Close()d on every single request, which rebuilt the TLS
// handshake and the auth exchange per try-on. genai.Client is safe for
// concurrent use, so one instance for the process is both correct and
// materially cheaper.
//
// The context passed here is only used for client construction; per-call
// deadlines come from the ctx handed to GenerateContent.
func getGeminiClient(ctx context.Context) (*genai.Client, error) {
	geminiOnce.Do(func() {
		if config.GeminiAPIKey == "" {
			geminiErr = fmt.Errorf("GEMINI_API_KEY is not set")
			return
		}
		// Deliberately not the request context: the singleton outlives the
		// request that happened to construct it, and a cancelled parent
		// would poison the client for every later caller.
		geminiClient, geminiErr = genai.NewClient(context.Background(), option.WithAPIKey(config.GeminiAPIKey))
	})
	return geminiClient, geminiErr
}

// newImageModel returns a configured image-generation model. Every caller
// goes through here so nobody can accidentally ship a path without
// SafetySettings again — the legacy /try-on generator did exactly that, and
// all four "no content generated" failures in the production log came from it.
func newImageModel(ctx context.Context, quality string) (*imageModel, error) {
	client, err := getGeminiClient(ctx)
	if err != nil {
		return nil, err
	}
	// Resolving through the star config is what keeps the model a pricing
	// decision rather than a source-code constant: config/stars.json maps a
	// quality tier to both its star cost and its model id, so the two can
	// never drift apart. An unrecognised quality resolves down to the default
	// tier, never up — a bad request must not silently buy the expensive model.
	quality = config.Stars.NormaliseQuality(quality)
	name := config.Stars.GeminiModelFor(quality)

	m := client.GenerativeModel(name)
	m.SafetySettings = permissiveSafetySettings()
	return &imageModel{GenerativeModel: m, Name: name, Quality: quality}, nil
}

// CloseGemini releases the shared client on shutdown.
func CloseGemini() {
	if geminiClient != nil {
		_ = geminiClient.Close()
	}
}

// imageFetchClient replaces the bare http.Get that fetchImage used to call.
// http.Get uses http.DefaultClient, which has NO timeout at all — one stalled
// S3 or CDN connection would hang a try-on request forever, independent of
// the Gemini deadline.
var imageFetchClient = &http.Client{Timeout: 15 * time.Second}

// permissiveSafetySettings lowers the threshold for every tunable harm
// category to "only block on HIGH probability". This doesn't affect
// BlockReasonOther (the image-gen model's internal anti-misuse policy is
// not user-tunable) but it does help with the regular safety categories
// that occasionally trip on body/clothing references in try-on prompts.
func permissiveSafetySettings() []*genai.SafetySetting {
	cats := []genai.HarmCategory{
		genai.HarmCategoryDangerousContent,
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
	}
	out := make([]*genai.SafetySetting, 0, len(cats))
	for _, c := range cats {
		out = append(out, &genai.SafetySetting{Category: c, Threshold: genai.HarmBlockOnlyHigh})
	}
	return out
}

// runGemini calls model.GenerateContent and extracts the first usable part
// from the response. If the call is blocked (typically BlockReasonOther on
// the image-gen model — non-deterministic, and not affected by SafetySettings
// because it comes from a separate internal classifier) and a retryParts
// callback is supplied, we automatically retry once with the stripped-down
// alternative prompt. The retry exists because, in practice, the same input
// often passes on a second attempt with slightly different framing.
//
// The error message preserves "blocked" so callers can branch on it for
// user-facing messages.
func runGemini(ctx context.Context, model *imageModel, label string, parts []genai.Part, retryParts func() []genai.Part) ([]byte, error) {
	out, err := callGemini(ctx, model, label, parts)
	if err == nil {
		return out, nil
	}
	if retryParts == nil || !isSafetyBlock(err) {
		return nil, err
	}
	slog.Warn("gemini retrying with stripped-down prompt after safety block", "label", label)
	return callGemini(ctx, model, label+" (retry)", retryParts())
}

// formatRatings renders a SafetyRating slice as a compact, loggable string.
func formatRatings(ratings []*genai.SafetyRating) string {
	if len(ratings) == 0 {
		return ""
	}
	out := make([]string, 0, len(ratings))
	for _, r := range ratings {
		if r == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s=%s(blocked=%v)", r.Category, r.Probability, r.Blocked))
	}
	return strings.Join(out, ", ")
}

func callGemini(ctx context.Context, model *imageModel, label string, parts []genai.Part) ([]byte, error) {
	start := time.Now()
	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		var be *genai.BlockedError
		if errors.As(err, &be) {
			blockReason, ratings, finish := "", "", ""
			if be.PromptFeedback != nil {
				blockReason = be.PromptFeedback.BlockReason.String()
				ratings = formatRatings(be.PromptFeedback.SafetyRatings)
				slog.Warn("gemini prompt blocked by safety filter",
					"label", label, "block_reason", blockReason, "safety", ratings)
			}
			if be.Candidate != nil {
				finish = be.Candidate.FinishReason.String()
				slog.Warn("gemini candidate blocked", "label", label, "finish_reason", finish)
			}
			alert.Errorf("gemini", "generation blocked", err,
				"label", label, "model", model.Name,
				"block_reason", blockReason, "finish_reason", finish, "safety", ratings)
			return nil, fmt.Errorf("failed to generate content: %v", err)
		}

		reportUpstreamError(label, model.Name, model.Quality, err, time.Since(start))
		return nil, fmt.Errorf("failed to generate content: %v", err)
	}

	out, err := extractImage(resp, label, model.Name, time.Since(start))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// extractImage pulls the generated image out of a Gemini response, or returns
// an error that says exactly why there isn't one.
//
// Split out from callGemini so the blocked-response shapes — which are the
// hard ones to reproduce against the live API — are unit-testable.
func extractImage(resp *genai.GenerateContentResponse, label, modelName string, took time.Duration) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("no content generated (nil response)")
	}

	// A prompt-level block returns zero candidates. Everything that explains
	// *why* lives on PromptFeedback — the old code threw it away and returned
	// the bare string "no content generated", which is exactly why four
	// production failures were undiagnosable.
	if len(resp.Candidates) == 0 {
		reason, ratings := "", ""
		if resp.PromptFeedback != nil {
			reason = resp.PromptFeedback.BlockReason.String()
			ratings = formatRatings(resp.PromptFeedback.SafetyRatings)
		}
		slog.Error("gemini prompt blocked — no candidates",
			"label", label, "block_reason", reason, "safety", ratings)
		alert.Errorf("gemini", "prompt blocked — no candidates", nil,
			"label", label, "model", modelName,
			"block_reason", reason, "safety", ratings)
		return nil, fmt.Errorf("no content generated (prompt blocked: %s)", reason)
	}

	// genai.Candidate.Content is a *Content, and a safety-blocked candidate
	// arrives with Content == nil. The previous
	//   len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0
	// short-circuits on the left, so the right-hand side dereferenced a nil
	// pointer and panicked the process. This checks Content explicitly.
	cand := resp.Candidates[0]
	if cand == nil || cand.Content == nil || len(cand.Content.Parts) == 0 {
		finish, ratings := "", ""
		if cand != nil {
			finish = cand.FinishReason.String()
			ratings = formatRatings(cand.SafetyRatings)
		}
		slog.Error("gemini candidate blocked — no parts",
			"label", label, "finish_reason", finish, "safety", ratings)
		alert.Errorf("gemini", "candidate blocked — no parts", nil,
			"label", label, "model", modelName,
			"finish_reason", finish, "safety", ratings)
		return nil, fmt.Errorf("no content generated (blocked, finish_reason=%s)", finish)
	}

	for _, part := range cand.Content.Parts {
		switch p := part.(type) {
		case genai.Blob:
			slog.Info("gemini returned an image",
				"label", label, "bytes", len(p.Data), "mime", p.MIMEType,
				"duration_ms", float64(took.Microseconds())/1000)
			return p.Data, nil

		case genai.Text:
			// This is an image-generation call. Returning text as if it were
			// image bytes is what produced "JPEGs" on S3 that were actually
			// an English sentence, handed to the user as a presigned URL.
			preview := strings.TrimSpace(string(p))
			if len(preview) > 200 {
				preview = preview[:200]
			}
			slog.Error("gemini returned TEXT, expected an image", "label", label, "preview", preview)
			alert.Errorf("gemini", "model returned TEXT, expected image", nil,
				"label", label, "model", modelName, "preview", preview)
			return nil, fmt.Errorf("model returned text instead of an image: %s", preview)

		default:
			// Never coerce an unknown part into image bytes.
			slog.Error("gemini returned an unsupported part type", "label", label, "part_type", fmt.Sprintf("%T", p))
			alert.Errorf("gemini", "unsupported response part", nil,
				"label", label, "part_type", fmt.Sprintf("%T", p))
			return nil, fmt.Errorf("unexpected response part type %T", p)
		}
	}

	return nil, fmt.Errorf("unexpected response format (empty content)")
}

// reportUpstreamError classifies a transport-level Gemini failure and alerts
// accordingly. Quota/credit exhaustion is FATAL (it means every try-on is
// dead until someone pays) and also trips the circuit breaker so we stop
// paying for calls that cannot succeed.
func reportUpstreamError(label, modelName, quality string, err error, took time.Duration) {
	switch {
	case IsQuotaError(err):
		GeminiBreaker.RecordQuotaFailure()
		alert.Report(alert.Event{
			Level:     alert.LevelFatal,
			Component: "gemini",
			Title:     "quota / credits exhausted",
			Err:       err,
			Latency:   took,
			Fields:    map[string]string{"label": label, "model": modelName, "quality": quality},
		})
	case isTimeoutError(err):
		alert.Errorf("gemini", "generation timed out", err,
			"label", label, "model", modelName, "took", took.Round(time.Millisecond).String())
	default:
		GeminiBreaker.RecordFailure()
		alert.Errorf("gemini", "generation failed", err,
			"label", label, "model", modelName)
	}
}

// ErrUpstreamUnavailable is returned when the circuit breaker is open, i.e.
// the upstream is known-dead and we are deliberately not paying for a call
// that cannot succeed.
var ErrUpstreamUnavailable = errors.New("try-on is temporarily unavailable (upstream circuit open)")

// guardBreaker fails fast when the Gemini breaker is open.
func guardBreaker() error {
	if ok, st := GeminiBreaker.Allow(); !ok {
		slog.Warn("try-on rejected locally: upstream circuit is not closed", "state", string(st))
		return ErrUpstreamUnavailable
	}
	return nil
}

// IsQuotaError matches the upstream billing/quota failures seen in
// production: HTTP 429, RESOURCE_EXHAUSTED, and the Google AI Studio
// "prepayment credits are depleted" message.
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") ||
		strings.Contains(s, "quota") ||
		// gRPC renders the status as "ResourceExhausted"; the REST transport
		// uses "RESOURCE_EXHAUSTED". Match both after lowercasing.
		strings.Contains(s, "resource_exhausted") ||
		strings.Contains(s, "resourceexhausted") ||
		strings.Contains(s, "credits are depleted") ||
		strings.Contains(s, "billing")
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context deadline exceeded") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "deadlineexceeded")
}

func isSafetyBlock(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "blocked") || strings.Contains(s, "blockreason") || strings.Contains(s, "block_reason")
}

// individualTryOnPrompt is the prompt used by the individual try-on
// generator. The wording deliberately avoids two common image-gen safety
// triggers:
//
//  1. The previous "Remove all original clothing from the person and dress
//     them ONLY..." pattern matches Gemini's anti-undressing classifier and
//     reliably trips BlockReasonOther when combined with photos of real
//     people (e.g. Amazon model shots).
//  2. Sending the customer photo alongside reference photos of a *different*
//     person looks like an identity-swap attempt unless we explicitly tell
//     the model how to disambiguate.
//
// The "fashion stylist" framing is a legitimate use-case label that pairs
// well with the explicit "ignore the reference model's face/body" guidance.
// If `terse` is true we emit a much shorter version used only as a retry
// after a safety block — fewer words = fewer trigger surfaces.
func individualTryOnPrompt(details, themeDescription string, terse bool) string {
	if terse {
		var sb strings.Builder
		sb.WriteString("Fashion photo: the customer (image 1) wearing the garment from the reference photo(s). ")
		sb.WriteString("Keep the customer's face, hair, body, and pose exactly as shown. ")
		sb.WriteString("Copy the garment's color, pattern, and cut from the reference. ")
		sb.WriteString("If the reference photo shows a model, use it for the garment only — do not copy that model's identity.")
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteString("You are a virtual fashion stylist. Generate one photograph showing the customer wearing the product.\n\n")
	sb.WriteString("IMAGE 1 — Customer:\n")
	sb.WriteString("  Keep the customer's face, hair, skin tone, body shape, and pose exactly as shown.\n")
	sb.WriteString("  Do not alter the customer's identity.\n\n")
	sb.WriteString("REMAINING IMAGES — Product reference:\n")
	sb.WriteString("  These show the garment to be worn. If a model is shown wearing it, the model is only a visual reference for how the product looks.\n")
	sb.WriteString("  Use the reference ONLY for the garment's color, pattern, fabric, cut, and details. Do not copy the reference model's face, body, or identity.\n\n")
	sb.WriteString("OUTPUT:\n")
	sb.WriteString("  A single photograph of the customer (from IMAGE 1) wearing the garment shown in the reference images.\n")
	sb.WriteString("  The garment in the output must match the reference (same color, pattern, fabric, cut). Do not invent or substitute clothing.\n")
	if details != "" {
		sb.WriteString(fmt.Sprintf("\nCustomer details: %s\n", details))
	}
	if themeDescription != "" {
		sb.WriteString(fmt.Sprintf("\nScene / theme: %s\n", themeDescription))
	}
	return sb.String()
}

// multiPersonTryOnPrompt is the multi-person/couple equivalent of
// individualTryOnPrompt. Same anti-trigger reasoning applies.
func multiPersonTryOnPrompt(numPeople int, themeDescription string, terse bool) string {
	if terse {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Fashion photo: the %d customers wearing the garments from the reference photos. ", numPeople)
		sb.WriteString("For each customer I send their photo followed by their garment reference(s). ")
		sb.WriteString("Keep every customer's face, hair, body, and pose exactly as shown. ")
		sb.WriteString("Copy each garment's color, pattern, and cut from the reference. ")
		sb.WriteString("If a reference photo shows a model, use it for the garment only — do not copy that model's identity.")
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteString("You are a virtual fashion stylist. Generate one photograph showing each customer wearing their product.\n\n")
	fmt.Fprintf(&sb, "There are %d customers. For each customer I provide their photo followed by their garment reference image(s).\n\n", numPeople)
	sb.WriteString("FOR EACH CUSTOMER:\n")
	sb.WriteString("  Keep that customer's face, hair, skin tone, body shape, and pose exactly as shown.\n")
	sb.WriteString("  Do not alter or merge the customers' identities.\n\n")
	sb.WriteString("GARMENT REFERENCE IMAGES:\n")
	sb.WriteString("  Show the garment to be worn by the preceding customer. If a model is shown wearing it, the model is only a visual reference for how the product looks.\n")
	sb.WriteString("  Use the reference ONLY for the garment's color, pattern, fabric, cut, and details. Do not copy the reference model's face, body, or identity.\n\n")
	sb.WriteString("OUTPUT:\n")
	sb.WriteString("  A single photograph of all customers (unchanged) wearing the garments from their respective references.\n")
	sb.WriteString("  Garments in the output must match the references (same color, pattern, fabric, cut). Do not invent or substitute clothing.\n")
	if themeDescription != "" {
		sb.WriteString(fmt.Sprintf("\nScene / theme: %s\n", themeDescription))
	}
	return sb.String()
}

// maxImageBytes bounds a single fetched image. A CDN that streams forever
// would otherwise be an unbounded allocation inside a request.
const maxImageBytes = 25 << 20 // 25 MiB

// fetchImage retrieves an image by path or URL. It takes a context so the
// parent Gemini deadline can cancel an in-flight download, and uses a client
// with an explicit timeout (see imageFetchClient) rather than http.Get.
func fetchImage(ctx context.Context, pathOrURL string) ([]byte, error) {
	if !strings.HasPrefix(pathOrURL, "http") {
		return os.ReadFile(pathOrURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pathOrURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := imageFetchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch image, status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}
	return data, nil
}

// imageSource reduces a fetch target to something safe to log: scheme, host
// and path, with the query string dropped. The query is where a presign puts
// its signature and credential scope, so the full URL must never reach a log
// line or an alert — but without *some* identity, a repeating "image fetch
// failed" says only that one of N images died, and cannot be traced back to
// the stored key or the retailer URL that produced it.
func imageSource(pathOrURL string) string {
	if !strings.HasPrefix(pathOrURL, "http") {
		return pathOrURL // local path, no signature to leak
	}
	u, err := neturl.Parse(pathOrURL)
	if err != nil {
		return "unparseable-url"
	}
	return u.Host + u.Path
}

func fetchImageLogged(ctx context.Context, label, url string) ([]byte, string, error) {
	data, err := fetchImage(ctx, url)
	if err != nil {
		src := imageSource(url)
		slog.Warn("gemini image fetch failed", "label", label, "source", src, "error", err.Error())
		alert.Warnf("gemini", "image fetch failed", err, "label", label, "source", src)
		return nil, "", err
	}
	mime := "jpeg"
	if len(data) > 4 {
		if data[0] == 0x89 && data[1] == 0x50 {
			mime = "png"
		} else if data[0] == 0x52 && data[1] == 0x49 {
			mime = "webp"
		}
	}
	slog.Debug("gemini image fetched", "label", label, "bytes", len(data), "mime", mime)
	return data, mime, nil
}

// PersonTryOnData holds the presigned URLs and details for a person in a try-on session
type PersonTryOnData struct {
	Details        string
	PersonImageURL string
	TopURL         []string
	BottomURL      []string
	AccessoryURL   []string
	DressURL       []string
}

// GenerateMultiPersonTryOnImage generates a multi-person virtual try-on image using Gemini.
// `themeReferenceURL` is accepted but ignored (kept for caller signature parity).
func GenerateMultiPersonTryOnImage(ctx context.Context, tryOnType string, themeImageURL, _, themeDescription string, people []PersonTryOnData, quality string) ([]byte, error) {
	return generateMultiPersonTryOn(ctx, tryOnType+" try-on", themeImageURL, themeDescription, people, quality)
}

// GenerateCoupleTryOnImage generates a virtual try-on image specifically structured for exactly 2 people (a couple).
func GenerateCoupleTryOnImage(ctx context.Context, themeImageURL, themeDescription string, people []PersonTryOnData, quality string) ([]byte, error) {
	if len(people) != 2 {
		return nil, fmt.Errorf("GenerateCoupleTryOnImage requires exactly 2 people")
	}
	return generateMultiPersonTryOn(ctx, "couple try-on", themeImageURL, themeDescription, people, quality)
}

func generateMultiPersonTryOn(ctx context.Context, label, themeImageURL, themeDescription string, people []PersonTryOnData, quality string) ([]byte, error) {
	if config.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}
	if len(people) == 0 {
		return nil, fmt.Errorf("no people provided")
	}
	if err := guardBreaker(); err != nil {
		return nil, err
	}

	model, err := newImageModel(ctx, quality)
	if err != nil {
		return nil, err
	}

	type img struct {
		mime string
		data []byte
	}
	type personImgs struct {
		details     string
		person      *img
		tops        []img
		bottoms     []img
		dresses     []img
		accessories []img
	}

	fetchAll := func(label string, urls []string) []img {
		out := make([]img, 0, len(urls))
		for _, u := range urls {
			if b, mime, err := fetchImageLogged(ctx, label, u); err == nil {
				out = append(out, img{mime: mime, data: b})
			}
		}
		return out
	}

	resolved := make([]personImgs, 0, len(people))
	for i, p := range people {
		tag := fmt.Sprintf("Person %d", i+1)
		var pi personImgs
		pi.details = p.Details
		if p.PersonImageURL != "" {
			if b, mime, err := fetchImageLogged(ctx, tag+"-photo", p.PersonImageURL); err == nil {
				pi.person = &img{mime: mime, data: b}
			}
		}
		pi.tops = fetchAll(tag+"-top", p.TopURL)
		pi.bottoms = fetchAll(tag+"-bottom", p.BottomURL)
		pi.dresses = fetchAll(tag+"-dress", p.DressURL)
		pi.accessories = fetchAll(tag+"-accessory", p.AccessoryURL)
		resolved = append(resolved, pi)
	}
	var themeImg *img
	if themeImageURL != "" {
		if b, mime, err := fetchImageLogged(ctx, "theme-background", themeImageURL); err == nil {
			themeImg = &img{mime: mime, data: b}
		}
	}

	buildParts := func(terse bool, perPersonGarmentLimit int) []genai.Part {
		parts := []genai.Part{genai.Text(multiPersonTryOnPrompt(len(resolved), themeDescription, terse))}
		for i, pi := range resolved {
			tag := fmt.Sprintf("Customer %d", i+1)
			if !terse {
				if pi.details != "" {
					parts = append(parts, genai.Text(fmt.Sprintf("%s (%s) — photo followed by their garment reference(s):", tag, pi.details)))
				} else {
					parts = append(parts, genai.Text(fmt.Sprintf("%s — photo followed by their garment reference(s):", tag)))
				}
			} else {
				parts = append(parts, genai.Text(fmt.Sprintf("%s photo, then garment:", tag)))
			}
			if pi.person != nil {
				parts = append(parts, genai.ImageData(pi.person.mime, pi.person.data))
			}
			remaining := perPersonGarmentLimit
			appendGarments := func(gs []img) {
				for _, g := range gs {
					if remaining == 0 {
						return
					}
					parts = append(parts, genai.ImageData(g.mime, g.data))
					if remaining > 0 {
						remaining--
					}
				}
			}
			appendGarments(pi.tops)
			appendGarments(pi.bottoms)
			appendGarments(pi.dresses)
			appendGarments(pi.accessories)
		}
		if themeImg != nil && !terse {
			parts = append(parts, genai.Text("Use this image as the background environment:"))
			parts = append(parts, genai.ImageData(themeImg.mime, themeImg.data))
		}
		return parts
	}

	primary := buildParts(false, -1)
	retry := func() []genai.Part { return buildParts(true, 1) }

	imgCount := 0
	for _, p := range primary {
		if _, ok := p.(genai.Blob); ok {
			imgCount++
		}
	}
	slog.Info("gemini request built", "label", label, "images", imgCount, "parts", len(primary))

	out, err := runGemini(ctx, model, label, primary, retry)
	if err == nil {
		GeminiBreaker.RecordSuccess()
	}
	return out, err
}

// GenerateIndividualTryOnImage generates a virtual try-on image specifically structured for exactly 1 person.
func GenerateIndividualTryOnImage(ctx context.Context, themeImageURL, themeDescription string, person PersonTryOnData, quality string) ([]byte, error) {
	if config.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}
	if err := guardBreaker(); err != nil {
		return nil, err
	}

	model, err := newImageModel(ctx, quality)
	if err != nil {
		return nil, err
	}

	// Resolve images up front so we can pass them to both the primary
	// attempt and any retry without re-downloading.
	type img struct {
		mime string
		data []byte
	}
	var personImg *img
	if person.PersonImageURL != "" {
		if b, mime, err := fetchImageLogged(ctx, "person", person.PersonImageURL); err == nil {
			personImg = &img{mime: mime, data: b}
		}
	}
	fetchAll := func(label string, urls []string) []img {
		out := make([]img, 0, len(urls))
		for _, u := range urls {
			if b, mime, err := fetchImageLogged(ctx, label, u); err == nil {
				out = append(out, img{mime: mime, data: b})
			}
		}
		return out
	}
	tops := fetchAll("top", person.TopURL)
	bottoms := fetchAll("bottom", person.BottomURL)
	dresses := fetchAll("dress", person.DressURL)
	accessories := fetchAll("accessory", person.AccessoryURL)
	var themeImg *img
	if themeImageURL != "" {
		if b, mime, err := fetchImageLogged(ctx, "theme-background", themeImageURL); err == nil {
			themeImg = &img{mime: mime, data: b}
		}
	}

	garmentCount := len(tops) + len(bottoms) + len(dresses) + len(accessories)
	if personImg == nil || garmentCount == 0 {
		return nil, fmt.Errorf("not enough images fetched (person=%v, garments=%d)", personImg != nil, garmentCount)
	}

	buildParts := func(terse bool, garmentLimit int) []genai.Part {
		parts := []genai.Part{genai.Text(individualTryOnPrompt(person.Details, themeDescription, terse))}
		parts = append(parts, genai.ImageData(personImg.mime, personImg.data))
		remaining := garmentLimit
		appendGarments := func(gs []img) {
			for _, g := range gs {
				if remaining == 0 {
					return
				}
				parts = append(parts, genai.ImageData(g.mime, g.data))
				if remaining > 0 {
					remaining--
				}
			}
		}
		appendGarments(tops)
		appendGarments(bottoms)
		appendGarments(dresses)
		appendGarments(accessories)
		if themeImg != nil && !terse {
			parts = append(parts, genai.Text("Use this image as the background environment:"))
			parts = append(parts, genai.ImageData(themeImg.mime, themeImg.data))
		}
		return parts
	}

	primary := buildParts(false, -1)
	// Retry strategy: drop "Person Details", drop theme, keep only the first
	// garment image, use the terse prompt. Lowest possible trigger surface
	// while still giving the model the bare minimum to do the job.
	retry := func() []genai.Part { return buildParts(true, 1) }

	imgCount := 0
	for _, p := range primary {
		if _, ok := p.(genai.Blob); ok {
			imgCount++
		}
	}
	slog.Info("gemini request built", "label", "individual try-on", "images", imgCount, "parts", len(primary))

	out, err := runGemini(ctx, model, "individual try-on", primary, retry)
	if err == nil {
		GeminiBreaker.RecordSuccess()
	}
	return out, err
}
