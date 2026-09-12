package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Admin test-generation.
//
// The problem this solves: without it, the only way to see what a prompt does
// is to publish the trend, open the app, and pay for a generation — a loop
// far too slow to write a good prompt in. With it, an admin edits a template
// and sees the resolved text and the image side by side.
//
// Three things make it safe enough to live in production, which it has to
// because there is only one backend:
//
//   - a dedicated shared secret, separate from the one that gates the dev-only
//     star routes, so it can be rotated without touching those;
//   - a hard rate limit, because every call spends real money upstream;
//   - no star accounting and no gallery document, so a preview can never be
//     confused for a user's own generation.
//
// It is still an endpoint that costs money per request. The token is the only
// thing standing between it and someone else's bill.

// trendPreviewLimiter bounds previews per token per window.
var trendPreviewLimiter = newKeyedLimiter()

const (
	trendPreviewLimit  = 20
	trendPreviewWindow = 5 * time.Minute
	// maxPreviewBody is larger than the ordinary JSON cap because a preview
	// carries inline base64 sample images — an admin testing a trend has no
	// staged uploads to point at.
	maxPreviewBody = 24 << 20
)

// TrendPreviewRequest is the body of POST /internal/trends/preview.
//
// The trend is sent whole rather than by id on purpose: the whole point is to
// test an edit that has not been saved yet.
type TrendPreviewRequest struct {
	Trend models.Trend `json:"trend"`
	// Inputs is the same shape a real generation sends, minus anything that
	// refers to a user's own data.
	Inputs trendInputs `json:"inputs"`
	// Quality overrides the trend's tier for this run.
	Quality string `json:"quality"`
	// SampleImages are base64 data (with or without a data: prefix) standing
	// in for the subject photos.
	SampleImages []string `json:"sample_images"`
}

// TrendPreviewHandler serves POST /internal/trends/preview.
func TrendPreviewHandler(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimSpace(config.AdminInternalToken)
	given := r.Header.Get("X-Internal-Secret")
	if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(given)) != 1 {
		// 404, not 401: an endpoint that spends money should not confirm it
		// exists to someone holding the wrong key.
		http.NotFound(w, r)
		return
	}

	if !trendPreviewLimiter.allow("trend_preview", trendPreviewLimit, trendPreviewWindow) {
		utils.RespondJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"error":       fmt.Sprintf("Preview limit reached (%d per %s). Every preview costs a real generation.", trendPreviewLimit, trendPreviewWindow),
			"retry_after": int(trendPreviewWindow.Seconds()),
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxPreviewBody))
	if err != nil {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}
	var req TrendPreviewRequest
	if err := json.Unmarshal(body, &req); err != nil {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}

	trend := req.Trend
	trend.Pricing.Normalise()
	if trend.Slug == "" {
		trend.Slug = "preview"
	}

	// Validated against a trend whose people block also accepts uploads.
	//
	// A preview supplies its subject photos inline — an admin has no saved
	// profiles to pick from, and would not want to use their own if they did.
	// Without this, a trend restricted to saved profiles could never be
	// previewed at all, which is exactly the trend whose prompt most needs
	// testing. Every other rule (counts, required fields, valid choices) is
	// applied unchanged, so the preview still refuses what a real run would.
	validated, err := validateTrendRequest(previewTrend(trend), trendGenerateRequest{
		Quality: req.Quality,
		Inputs:  req.Inputs,
	})
	if err != nil {
		// A preview of an invalid configuration is still useful information:
		// it tells the admin the schema rejects their own sample inputs.
		utils.RespondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":  err.Error(),
			"reason": "invalid_inputs",
		})
		return
	}

	images, err := decodeSampleImages(r.Context(), req.SampleImages)
	if err != nil {
		utils.RespondError(w, nil, err.Error(), http.StatusBadRequest)
		return
	}

	// The prompt is rendered from the same functions a real generation uses,
	// so what an admin sees here is exactly what a user's run would send.
	runInputs := previewRunInputs(validated)
	prompt, _ := utils.RenderTrendPrompt(trend.Generation, utils.TrendPromptValues{
		PeopleCount:     len(images),
		BackgroundText:  validated.Background.Text,
		Pose:            validated.Pose,
		Instruction:     validated.Instruction,
		Fields:          validated.Fields,
		ChoiceFragments: utils.TrendChoiceFragments(trend, runInputs),
	})

	started := time.Now()
	cost, breakdown := trend.Pricing.Cost(validated.Selection())

	run := models.TrendRun{
		TrendID:      trend.ID.Hex(),
		TrendSlug:    trend.Slug,
		TrendTitle:   trend.Title,
		TrendVersion: trend.Version,
		UserID:       "admin-preview",
		Quality:      validated.Quality,
		Provider:     trendProvider(trend),
		Model:        trendModelName(trend, validated.Quality),
		Inputs:       runInputs,
		// The cost is recorded but never charged: an admin iterating on a
		// prompt should be able to see what the trend would have cost, and
		// what the previews themselves are costing in model calls.
		StarsCharged:     0,
		PricingBreakdown: breakdown,
		ResolvedPrompt:   prompt,
		IsPreview:        true,
		CreatedAt:        time.Now(),
	}

	// A preview always produces one image. Generating four to test a prompt
	// is four times the spend for no extra information.
	genCtx, cancel := context.WithTimeout(context.Background(),
		utils.TrendTimeout(validated.Quality, trend.Generation.TimeoutSecs))
	defer cancel()

	generated, genErr := utils.GenerateTrendImage(genCtx, utils.TrendGenSpec{
		Label:         "trend-preview:" + trend.Slug,
		Prompt:        prompt,
		TersePrompt:   trend.Generation.TersePrompt,
		Images:        images,
		Provider:      trend.Generation.Provider,
		Quality:       validated.Quality,
		ModelOverride: trend.Generation.ModelOverride,
		Outputs:       1,
	})

	persistCtx, cancelPersist := persistCtx()
	defer cancelPersist()

	if genErr != nil {
		run.Status = models.TrendRunFailed
		run.DurationMS = time.Since(started).Milliseconds()
		run.Error = &models.TrendRunError{
			Stage:        models.TrendStageGenerate,
			Reason:       utils.FailureReason(genErr),
			Message:      truncateForLog(genErr.Error(), 500),
			FinishReason: utils.FinishReasonOf(genErr),
		}
		recordTrendRun(persistCtx, run)

		// 200 with a failure body, not an error status: the request itself
		// worked, and the admin needs the reason and the prompt that produced
		// it far more than they need an HTTP code.
		utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
			"ok":              false,
			"reason":          utils.FailureReason(genErr),
			"finish_reason":   utils.FinishReasonOf(genErr),
			"error":           genErr.Error(),
			"resolved_prompt": prompt,
			"model":           run.Model,
			"quality":         run.Quality,
			"duration_ms":     run.DurationMS,
			"would_cost":      cost,
		})
		return
	}

	objectKey := fmt.Sprintf("trend_previews/%s/%d.jpg", trend.Slug, time.Now().UnixNano())
	if _, err := utils.UploadFileToS3(persistCtx, bytes.NewReader(generated.Images[0]), objectKey, "image/jpeg"); err != nil {
		utils.RespondInternalError(w, r, nil, "s3",
			"The image generated but couldn't be saved.", err, http.StatusInternalServerError)
		return
	}
	url, _ := utils.GetPresignedURL(r.Context(), objectKey)

	run.Provider, run.Model = generated.Provider, generated.Model
	run.Status = models.TrendRunCompleted
	run.OutputKeys = []string{objectKey}
	run.DurationMS = time.Since(started).Milliseconds()
	recordTrendRun(persistCtx, run)

	// A saved trend that has just been previewed is very likely about to be
	// looked at in the app, so drop the cached list rather than making the
	// admin wait out its TTL.
	invalidateTrendCache()

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"ok":              true,
		"image_url":       url,
		"object_key":      objectKey,
		"resolved_prompt": prompt,
		"model":           run.Model,
		"quality":         run.Quality,
		"duration_ms":     run.DurationMS,
		"would_cost":      cost,
		"cost_breakdown":  breakdown,
	})
}

// previewTrend relaxes the one rule a preview cannot satisfy: where a subject
// photo came from. Returns a copy — the caller still generates, records and
// prices against the trend as authored.
func previewTrend(trend models.Trend) models.Trend {
	people := trend.Inputs.People
	if people.Enabled {
		sources := append([]string(nil), people.Sources...)
		hasUpload := false
		for _, source := range sources {
			if source == models.PhotoSourceUpload {
				hasUpload = true
				break
			}
		}
		if !hasUpload {
			sources = append(sources, models.PhotoSourceUpload)
		}
		people.Sources = sources
		trend.Inputs.People = people
	}
	return trend
}

// previewRunInputs is the run-record shape for a preview.
//
// It carries the answers but none of the image references, because a preview
// has no person ids, no wardrobe ids and no staged uploads to point at.
func previewRunInputs(v validatedTrendRequest) models.TrendRunInputs {
	return models.TrendRunInputs{
		Background: models.TrendRunBackground{
			Mode:     v.Background.Mode,
			PresetID: v.Background.PresetID,
			Text:     v.Background.Text,
		},
		Pose:        v.Pose,
		Instruction: v.Instruction,
		Aspect:      v.Aspect,
		Outputs:     v.Outputs,
		Fields:      v.Fields,
	}
}

// maxPreviewImages bounds how many samples one preview may send. Matches the
// largest people count any sane trend asks for.
const maxPreviewImages = 4

// decodeSampleImages turns the admin's base64 samples into image refs.
//
// Uploaded to S3 rather than passed inline, because GenerateTrendImage fetches
// by URL — one code path to the model, shared with real generations, so a
// preview cannot succeed in a way a real run would not.
func decodeSampleImages(ctx context.Context, samples []string) ([]utils.TrendImageRef, error) {
	if len(samples) > maxPreviewImages {
		samples = samples[:maxPreviewImages]
	}

	refs := make([]utils.TrendImageRef, 0, len(samples))
	for i, sample := range samples {
		if idx := strings.Index(sample, ","); strings.HasPrefix(sample, "data:") && idx > 0 {
			sample = sample[idx+1:]
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sample))
		if err != nil {
			return nil, fmt.Errorf("sample image %d is not valid base64", i+1)
		}
		if len(data) == 0 {
			continue
		}

		key := fmt.Sprintf("trend_previews/inputs/%d-%d.jpg", time.Now().UnixNano(), i)
		uploadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err = utils.UploadFileToS3(uploadCtx, bytes.NewReader(data), key, "image/jpeg")
		cancel()
		if err != nil {
			return nil, fmt.Errorf("could not stage sample image %d", i+1)
		}

		url, err := utils.GetPresignedURL(ctx, key)
		if err != nil || url == "" {
			return nil, fmt.Errorf("could not stage sample image %d", i+1)
		}

		label := ""
		if len(samples) > 1 {
			label = fmt.Sprintf("Subject %d:", i+1)
		}
		refs = append(refs, utils.TrendImageRef{URL: url, Label: label, Essential: true})
	}
	return refs, nil
}
