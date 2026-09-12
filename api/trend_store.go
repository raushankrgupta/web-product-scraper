package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Reading trend definitions.
//
// This process never writes to the trends collection — the admin backend owns
// authoring, and both point at the same database. That asymmetry is what lets
// a new trend appear in the app without a deploy on this side, and it is also
// why everything read here is treated as untrusted-ish: a document may have
// been hand-edited, may predate a field, or may be half-written. Nothing below
// assumes otherwise.

// trendCacheTTL is how long the active list is reused.
//
// One minute, not five: publishing a trend is a deliberate act with someone
// watching, and a minute is the difference between "it works" and "is it
// broken?". The response itself is cached for longer downstream, so this only
// bounds how stale the origin is.
const trendCacheTTL = 60 * time.Second

var (
	trendMu     sync.RWMutex
	trendCache  []models.Trend
	trendCached time.Time
	trendLoaded bool
)

// activeTrends returns the trends that should be visible right now, ordered.
//
// "Right now" means: status active, this environment listed, and inside any
// configured schedule window. The schedule is evaluated here rather than by a
// cron job flipping statuses, so an ends_at that passes at 3am retires the
// trend without anything having to be running at 3am.
func activeTrends(ctx context.Context) []models.Trend {
	trendMu.RLock()
	if trendLoaded && time.Since(trendCached) < trendCacheTTL {
		cached := trendCache
		trendMu.RUnlock()
		return cached
	}
	trendMu.RUnlock()

	loaded := loadActiveTrends(ctx)

	trendMu.Lock()
	trendCache, trendCached, trendLoaded = loaded, time.Now(), true
	trendMu.Unlock()

	return loaded
}

func loadActiveTrends(ctx context.Context) []models.Trend {
	if !utils.MongoReady() {
		return nil
	}

	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	cursor, err := utils.GetCollection(config.DBName, models.CollTrends).Find(readCtx,
		bson.M{"status": models.TrendStatusActive},
		options.Find().SetSort(bson.D{{Key: "sort_order", Value: 1}, {Key: "created_at", Value: -1}}))
	if err != nil {
		// Serving no trends is a degraded home screen; serving an error is a
		// broken one. The carousel hides itself when the list is empty.
		utils.L(ctx).Warn("trends query failed", "error", err.Error())
		return nil
	}
	defer cursor.Close(readCtx)

	var all []models.Trend
	if err := cursor.All(readCtx, &all); err != nil {
		utils.L(ctx).Warn("trends decode failed", "error", err.Error())
		return nil
	}

	now := time.Now()
	visible := make([]models.Trend, 0, len(all))
	for _, t := range all {
		if !trendVisibleNow(t, now) {
			continue
		}
		t.Pricing.Normalise()
		visible = append(visible, t)
	}
	return visible
}

// trendVisibleNow evaluates the availability rules.
func trendVisibleNow(t models.Trend, now time.Time) bool {
	if t.Status != models.TrendStatusActive {
		return false
	}
	if !trendInEnvironment(t) {
		return false
	}
	if t.StartsAt != nil && now.Before(*t.StartsAt) {
		return false
	}
	if t.EndsAt != nil && now.After(*t.EndsAt) {
		return false
	}
	return true
}

// trendInEnvironment reports whether this deployment should serve a trend.
//
// An empty Environments list means "everywhere", which is the sensible
// default for a trend an admin never thought about environments for. A
// populated list is a visibility switch: tagging a trend `local` is how you
// exercise it against the real backend without it appearing for real users.
func trendInEnvironment(t models.Trend) bool {
	if len(t.Environments) == 0 {
		return true
	}
	env := strings.ToLower(strings.TrimSpace(config.Environment))
	if env == "production" {
		env = "prod"
	}
	for _, e := range t.Environments {
		candidate := strings.ToLower(strings.TrimSpace(e))
		if candidate == "production" {
			candidate = "prod"
		}
		if candidate == env {
			return true
		}
	}
	return false
}

// findTrend loads one trend by id, whatever its status.
//
// Not served from the active cache, because the two callers that need a
// single trend want different things from it: a generation must see the
// document as it is now (a trend retired mid-session must stop generating),
// and the admin preview path deliberately runs against drafts.
func findTrend(ctx context.Context, id string) (models.Trend, bool) {
	objID, err := primitive.ObjectIDFromHex(strings.TrimSpace(id))
	if err != nil || !utils.MongoReady() {
		return models.Trend{}, false
	}

	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var trend models.Trend
	if err := utils.GetCollection(config.DBName, models.CollTrends).
		FindOne(readCtx, bson.M{"_id": objID}).Decode(&trend); err != nil {
		return models.Trend{}, false
	}
	trend.Pricing.Normalise()
	return trend, true
}

// invalidateTrendCache drops the cached list. Used by the preview path, so an
// admin who saves and immediately tests is not served a stale definition.
func invalidateTrendCache() {
	trendMu.Lock()
	trendLoaded = false
	trendMu.Unlock()
}

// copyTrendsForResponse deep-copies the cached trends so the caller can
// presign them without touching the cache.
//
// A plain `copy()` of the slice is not enough and the difference is not
// cosmetic: copying a Trend copies its struct fields, but SampleImageURLs,
// Background.Presets and Fields[].Choices are slices whose headers point back
// at the cache's own backing arrays. Signing "in the copy" therefore wrote
// signed URLs straight into the cached document, and the next reader of that
// entry saw a URL where a key belonged and passed it through unchanged —
// serving a signature that had already started ageing. A signing failure is
// worse still, because it writes "" and that sticks for the life of the
// entry, leaving a permanently blank tile.
//
// Every slice that can hold an S3 key is therefore rebuilt here.
func copyTrendsForResponse(trends []models.Trend) []models.Trend {
	out := make([]models.Trend, len(trends))
	for i, trend := range trends {
		trend.SampleImageURLs = append([]string(nil), trend.SampleImageURLs...)
		trend.Inputs.Background.Presets =
			append([]models.BackgroundPreset(nil), trend.Inputs.Background.Presets...)

		fields := append([]models.CustomField(nil), trend.Inputs.Fields...)
		for j := range fields {
			fields[j].Choices = append([]models.FieldChoice(nil), fields[j].Choices...)
		}
		trend.Inputs.Fields = fields

		// Not presigned, but copied for the same reason: a caller that
		// reordered or trimmed these would otherwise mutate the cache.
		trend.Inputs.Aspect.Choices =
			append([]models.FieldChoice(nil), trend.Inputs.Aspect.Choices...)
		trend.Environments = append([]string(nil), trend.Environments...)

		out[i] = trend
	}
	return out
}

// normaliseTrendForClient fills in what the client cannot work out for itself.
//
// Specifically the effective quality tier. When a trend does not let the user
// choose one, the server resolves it from Quality.Default and then from
// Generation.QualityTier — but Generation never reaches a device, so a trend
// with an empty Quality.Default left the client guessing "flash" while the
// server ran (and charged for) whatever the generation block said.
//
// With a quality multiplier configured that is not a cosmetic difference: the
// button would advertise the flash price and the user would be billed the pro
// one. Stamping the resolved tier here makes the client's own default read
// back the same answer the server will use.
func normaliseTrendForClient(t *models.Trend) {
	t.Inputs.Quality.Default = resolveTrendQuality(*t, "")
}

// presignTrend swaps stored S3 keys for URLs a client can load.
//
// Signed for PresignCatalog rather than the default hour, for the same reason
// the theme catalogue is: this payload is cached by the client, and a
// signature shorter than that cache is what leaves a grid of blank tiles an
// hour after first load.
func presignTrend(ctx context.Context, t *models.Trend) {
	t.CoverImageURL = presignCatalogKey(ctx, t.CoverImageURL)
	for i := range t.SampleImageURLs {
		t.SampleImageURLs[i] = presignCatalogKey(ctx, t.SampleImageURLs[i])
	}
	for i := range t.Inputs.Background.Presets {
		t.Inputs.Background.Presets[i].ImageKey =
			presignCatalogKey(ctx, t.Inputs.Background.Presets[i].ImageKey)
	}
	for i := range t.Inputs.Fields {
		for j := range t.Inputs.Fields[i].Choices {
			t.Inputs.Fields[i].Choices[j].ImageKey =
				presignCatalogKey(ctx, t.Inputs.Fields[i].Choices[j].ImageKey)
		}
	}
}

// presignCatalogKey signs one key, leaving absolute URLs alone — a seeded
// trend may point straight at an external image.
func presignCatalogKey(ctx context.Context, key string) string {
	if key == "" || strings.HasPrefix(key, "http") {
		return key
	}
	if url, err := utils.GetPresignedURLWithExpiry(ctx, key, utils.PresignCatalog); err == nil {
		return url
	}
	return ""
}
