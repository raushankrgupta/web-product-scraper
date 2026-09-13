package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// errTrendUnavailable is the verdict for a trend that cannot be generated
// right now — retired, out of its window, or never active. Separated from a
// validation error so the gate can answer 404 rather than 400.
var errTrendUnavailable = errors.New("trend unavailable")

// trendCostResolver prices a trend generation.
//
// It re-reads the trend from Mongo rather than trusting anything in the
// request, validates the request against that document, and computes the cost
// from the document's own formula. The client ships the same formula so it can
// render a price; this is the one that charges.
func trendCostResolver(r *http.Request, body []byte, quality string) (GateDecision, error) {
	var req trendGenerateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return GateDecision{Kind: "trend"}, fmt.Errorf("invalid request body")
	}

	trend, ok := findTrend(r.Context(), req.TrendID)
	if !ok || !trendVisibleNow(trend, time.Now()) {
		return GateDecision{Kind: "trend"}, errTrendUnavailable
	}

	validated, err := validateTrendRequest(trend, req)
	if err != nil {
		return GateDecision{Kind: "trend"}, err
	}

	cost, _ := trend.Pricing.Cost(validated.Selection())
	return GateDecision{
		Kind:         "trend",
		Cost:         cost,
		FreeEligible: trend.Pricing.FreeEligible,
	}, nil
}

// TrendGenerateHandler serves POST /trends/generate.
//
// It runs inside StarGateMiddlewareWith(trendCostResolver, …), so by the time
// it is called the stars are already held. Every failure below therefore has
// to leave a non-2xx status behind — that is what the middleware settles the
// refund against — and none of them may write a success first.
func TrendGenerateHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { utils.FlushLog(r.Context(), &logMessageBuilder) }()
	utils.AddToLogMessage(&logMessageBuilder, "[Trend Generate]")

	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if IsGuestFromContext(r.Context()) {
		utils.RespondErrorReason(w, &logMessageBuilder, "Sign in to try this look",
			"guest_not_eligible", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody))
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}
	var req trendGenerateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}

	trend, ok := findTrend(r.Context(), req.TrendID)
	if !ok || !trendVisibleNow(trend, time.Now()) {
		utils.RespondError(w, &logMessageBuilder, "That trend is no longer available", http.StatusNotFound)
		return
	}

	// Re-validated rather than carried over from the billing resolver. The
	// two must agree, and the cheapest way to guarantee that is to run the
	// same pure function on the same bytes.
	validated, err := validateTrendRequest(trend, req)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, err.Error(), http.StatusBadRequest)
		return
	}

	cost, breakdown := trend.Pricing.Cost(validated.Selection())
	started := time.Now()

	run := models.TrendRun{
		TrendID:          trend.ID.Hex(),
		TrendSlug:        trend.Slug,
		TrendTitle:       trend.Title,
		TrendVersion:     trend.Version,
		UserID:           userID,
		Quality:          validated.Quality,
		Provider:         trendProvider(trend),
		Model:            trendModelName(trend, validated.Quality),
		StarsCharged:     cost,
		PricingBreakdown: breakdown,
		RequestID:        utils.RequestIDFromContext(r.Context()),
		CreatedAt:        time.Now(),
	}

	resolved, err := resolveTrendInputs(r.Context(), userID, trend, validated)
	if err != nil {
		run.Inputs = models.TrendRunInputs{Fields: validated.Fields}
		failTrendRun(r, &logMessageBuilder, run, models.TrendStageResolve, "input_unavailable",
			err.Error(), started)
		utils.RespondError(w, &logMessageBuilder, err.Error(), http.StatusBadRequest)
		return
	}

	run.Inputs = resolved.Run
	run.InputKeys = resolved.Keys
	run.ResolvedPrompt = resolved.Prompt

	genCtx, cancelGen := context.WithTimeout(context.Background(),
		utils.TrendTimeout(validated.Quality, trend.Generation.TimeoutSecs))
	defer cancelGen()

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf(
		"Generating trend %s: people=%d images=%d outputs=%d quality=%s",
		trend.Slug, len(validated.People), len(resolved.Spec.Images), validated.Outputs, validated.Quality))

	generated, genErr := utils.GenerateTrendImage(genCtx, resolved.Spec)
	if genErr != nil {
		failTrendRun(r, &logMessageBuilder, run, models.TrendStageGenerate,
			utils.FailureReason(genErr), genErr.Error(), started)
		respondTrendGenError(w, r, &logMessageBuilder, genErr)
		return
	}

	// Storing what we have already paid to produce gets its own context: a
	// client that hung up mid-generation must not cost us the result.
	storeCtx, cancelStore := persistCtx()
	defer cancelStore()

	// Recorded from what actually served the image: a transport failure on
	// the trend's own provider falls back to the other one.
	run.Provider, run.Model = generated.Provider, generated.Model

	stored, err := storeTrendOutputs(storeCtx, userID, trend, generated.Images)
	if err != nil {
		alert.Errorf("s3", "trend image upload failed", err, "trend", trend.Slug)
		failTrendRun(r, &logMessageBuilder, run, models.TrendStageStore, "store_failed", err.Error(), started)
		utils.RespondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "We created your look but couldn't save it. Please try again.",
		})
		return
	}

	run.Status = models.TrendRunCompleted
	run.OutputKeys = stored.keys
	run.TryOnIDs = stored.tryOnIDs
	run.DurationMS = time.Since(started).Milliseconds()
	recordTrendRun(storeCtx, run)
	bumpTrendCounters(storeCtx, trend.ID)
	keepTrendUploads(storeCtx, userID, resolved.Run)

	urls := make([]string, 0, len(stored.keys))
	for _, key := range stored.keys {
		if url, err := utils.GetPresignedURL(r.Context(), key); err == nil {
			urls = append(urls, url)
		}
	}
	if len(urls) == 0 {
		// The images are stored; we just cannot sign them. Rare, and not
		// worth failing a paid generation over — but the client has nothing
		// to show, so it is an error rather than an empty success.
		utils.RespondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "We created your look but couldn't load it. It's saved in your gallery.",
		})
		return
	}

	primary := stored.record
	primary.GeneratedImageURL = urls[0]

	utils.L(r.Context()).Info("trend success",
		"trend", trend.Slug, "user_id", userID, "outputs", len(urls),
		"stars", cost, "duration_ms", run.DurationMS)

	// Shaped like a try-on response on purpose: the results screen renders it
	// with no branch of its own.
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"result":        urls[0],
		"results":       urls,
		"tryon_details": primary,
		"run_id":        run.ID.Hex(),
		"trend": map[string]interface{}{
			"id":    trend.ID.Hex(),
			"slug":  trend.Slug,
			"title": trend.Title,
		},
	})
}

type storedTrendOutputs struct {
	keys     []string
	tryOnIDs []string
	record   models.TryOn
}

// storeTrendOutputs uploads each generated image and writes its gallery
// document.
//
// The gallery documents are what make a trend result behave like everything
// else in the app — favouriting, saving to the phone, sharing and deleting
// all go through the same collection and needed no changes. Succeeds if at
// least one image lands; a partial failure loses one picture, not the run.
func storeTrendOutputs(ctx context.Context, userID string, trend models.Trend,
	images [][]byte) (storedTrendOutputs, error) {

	var out storedTrendOutputs
	collection := utils.GetCollection(config.DBName, "tryons")

	for i, data := range images {
		ext, mime := utils.GeneratedImageName(data)
		objectKey := fmt.Sprintf("generated_trends/%s/%d-%d%s", trend.Slug, time.Now().UnixNano(), i, ext)
		if _, err := utils.UploadFileToS3(ctx, bytes.NewReader(data), objectKey, mime); err != nil {
			utils.L(ctx).Error("trend output upload failed",
				"trend", trend.Slug, "index", i, "error", err.Error())
			continue
		}

		record := models.TryOn{
			ID:                primitive.NewObjectID(),
			UserID:            userID,
			Type:              "trend",
			TrendID:           trend.ID.Hex(),
			TrendSlug:         trend.Slug,
			TrendTitle:        trend.Title,
			GeneratedImageURL: objectKey,
			Status:            "completed",
			CreatedAt:         time.Now(),
		}
		if _, err := collection.InsertOne(ctx, record); err != nil {
			// The image exists in S3 and the run record will point at it, so
			// this costs a gallery entry rather than the result.
			utils.L(ctx).Error("trend gallery insert failed", "trend", trend.Slug, "error", err.Error())
		}

		out.keys = append(out.keys, objectKey)
		out.tryOnIDs = append(out.tryOnIDs, record.ID.Hex())
		if i == 0 {
			out.record = record
		}
	}

	if len(out.keys) == 0 {
		return out, fmt.Errorf("no generated image could be stored")
	}
	return out, nil
}

// failTrendRun records a failed run and logs it.
//
// Written for every failure, not just interesting ones, because the run
// collection is the only place a prompt's behaviour is visible after the
// fact — and a prompt that fails 40% of the time is exactly what nobody
// notices without a record of the failures.
func failTrendRun(r *http.Request, logger *strings.Builder, run models.TrendRun,
	stage, reason, message string, started time.Time) {

	run.Status = models.TrendRunFailed
	run.DurationMS = time.Since(started).Milliseconds()
	run.Error = &models.TrendRunError{
		Stage:   stage,
		Reason:  reason,
		Message: truncateForLog(message, 500),
	}
	// A hold that is about to be refunded was never charged.
	run.StarsCharged = 0

	utils.AddToLogMessage(logger, fmt.Sprintf("Trend run failed: stage=%s reason=%s", stage, reason))

	ctx, cancel := persistCtx()
	defer cancel()
	recordTrendRun(ctx, run)
}

// recordTrendRun persists one run. Best-effort: a diagnostic write must never
// be the reason a user is told their generation failed.
func recordTrendRun(ctx context.Context, run models.TrendRun) {
	if !utils.MongoReady() {
		return
	}
	if run.ID.IsZero() {
		run.ID = primitive.NewObjectID()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	if _, err := utils.GetCollection(config.DBName, models.CollTrendRuns).InsertOne(ctx, run); err != nil {
		utils.L(ctx).Warn("trend run insert failed", "trend", run.TrendSlug, "error", err.Error())
	}
}

// bumpTrendCounters keeps the admin list's "how much is this being used"
// column honest without a scan over the runs collection.
func bumpTrendCounters(ctx context.Context, trendID primitive.ObjectID) {
	if !utils.MongoReady() {
		return
	}
	now := time.Now()
	_, err := utils.GetCollection(config.DBName, models.CollTrends).UpdateOne(ctx,
		bson.M{"_id": trendID},
		bson.M{"$inc": bson.M{"run_count": 1}, "$set": bson.M{"last_run_at": now}})
	if err != nil {
		utils.L(ctx).Debug("trend counter update failed", "error", err.Error())
	}
}

// respondTrendGenError maps a generation failure onto a status and a sentence
// a user can act on. Mirrors respondGenError's vocabulary so a trend failure
// and a try-on failure read the same way in the app.
func respondTrendGenError(w http.ResponseWriter, r *http.Request, logger *strings.Builder, err error) {
	switch {
	case errors.Is(err, utils.ErrUpstreamUnavailable):
		utils.RespondError(w, logger,
			"Image generation is briefly unavailable. Please try again in a minute.",
			http.StatusServiceUnavailable)
	case utils.IsQuotaError(err):
		utils.RespondError(w, logger,
			"We're at capacity right now. Please try again shortly.",
			http.StatusTooManyRequests)
	case strings.Contains(strings.ToLower(err.Error()), "blocked"):
		utils.RespondError(w, logger,
			"We couldn't create this look from those photos. Try a different photo or another trend.",
			http.StatusUnprocessableEntity)
	case errors.Is(r.Context().Err(), context.DeadlineExceeded):
		utils.RespondError(w, logger,
			"That took too long. Please try again.", http.StatusGatewayTimeout)
	default:
		utils.RespondError(w, logger,
			"We couldn't create this look. Please try again.", http.StatusBadGateway)
	}
}

func trendProvider(trend models.Trend) string {
	return utils.NormaliseTrendProvider(trend.Generation.Provider)
}

// trendModelName is the model a trend is configured to use — for the run
// record before generation, and for a failure. A success overwrites it with
// the model that actually served the image.
func trendModelName(trend models.Trend, quality string) string {
	if trend.Generation.ModelOverride != "" {
		return trend.Generation.ModelOverride
	}
	if trendProvider(trend) == utils.ProviderOpenAI {
		if model, _, ok := config.Stars.OpenAIModelFor(quality); ok {
			return model
		}
	}
	return config.Stars.GeminiModelFor(quality)
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// TrendGenerateRoute is the full middleware sandwich for a trend generation.
//
// Identical ordering to the try-on routes and for identical reasons:
// TryOnGuardMiddleware sits outside billing so a duplicate in-flight request
// is rejected before it can take a second hold, and the star gate reserves
// before the handler runs so nothing reaches a paid upstream call unpaid.
func TrendGenerateRoute() http.Handler {
	return AuthMiddleware(TryOnGuardMiddleware(
		StarGateMiddlewareWith(trendCostResolver, http.HandlerFunc(TrendGenerateHandler))))
}
