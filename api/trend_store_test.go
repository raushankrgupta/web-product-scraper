package api

import (
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
)

// cachedTrend is a trend shaped like one the list endpoint would serve: every
// place a stored S3 key can hide is populated.
func cachedTrend() models.Trend {
	return models.Trend{
		Slug:            "poster",
		Status:          models.TrendStatusActive,
		CoverImageURL:   "trends/poster/cover.jpg",
		SampleImageURLs: []string{"trends/poster/sample-1.jpg", "trends/poster/sample-2.jpg"},
		Inputs: models.TrendInputs{
			Background: models.BackgroundInput{
				Presets: []models.BackgroundPreset{
					{ID: "studio", ImageKey: "trends/poster/preset-studio.jpg"},
				},
			},
			Fields: []models.CustomField{
				{
					Key: "era",
					Choices: []models.FieldChoice{
						{Value: "1985", ImageKey: "trends/poster/era-1985.jpg"},
					},
				},
			},
		},
	}
}

// TestCopyTrendsForResponse_DoesNotShareNestedSlices is the regression guard
// for a cache-corruption bug.
//
// activeTrends hands back the cached slice itself, and the list handler has to
// presign every key before serving. A `copy()` of the slice looks like it
// isolates that, but it copies struct values whose nested slices still point at
// the cache's backing arrays — so signing in place rewrote the cached trend's
// sample, preset and choice keys into signed URLs. The next reader of that
// cache entry then saw a URL rather than a key, passed it through unchanged,
// and served a signature that had already started ageing. Worse, a signing
// failure writes "" and that empty string sticks for the life of the entry,
// leaving permanently blank tiles.
func TestCopyTrendsForResponse_DoesNotShareNestedSlices(t *testing.T) {
	cache := []models.Trend{cachedTrend()}

	listed := copyTrendsForResponse(cache)

	// Simulate what presignTrend does: overwrite every key in place.
	listed[0].CoverImageURL = "https://signed/cover"
	listed[0].SampleImageURLs[0] = "https://signed/sample-1"
	listed[0].Inputs.Background.Presets[0].ImageKey = "https://signed/preset"
	listed[0].Inputs.Fields[0].Choices[0].ImageKey = "https://signed/era"

	original := cachedTrend()
	if cache[0].CoverImageURL != original.CoverImageURL {
		t.Errorf("cover key was rewritten in the cache: %q", cache[0].CoverImageURL)
	}
	if cache[0].SampleImageURLs[0] != original.SampleImageURLs[0] {
		t.Errorf("sample key was rewritten in the cache: %q", cache[0].SampleImageURLs[0])
	}
	if cache[0].Inputs.Background.Presets[0].ImageKey != original.Inputs.Background.Presets[0].ImageKey {
		t.Errorf("preset key was rewritten in the cache: %q",
			cache[0].Inputs.Background.Presets[0].ImageKey)
	}
	if cache[0].Inputs.Fields[0].Choices[0].ImageKey != original.Inputs.Fields[0].Choices[0].ImageKey {
		t.Errorf("field choice key was rewritten in the cache: %q",
			cache[0].Inputs.Fields[0].Choices[0].ImageKey)
	}
}

// TestCopyTrendsForResponse_PreservesContent guards the other direction: the
// copy has to actually carry everything, or the response silently loses the
// samples and presets it was supposed to sign.
func TestCopyTrendsForResponse_PreservesContent(t *testing.T) {
	cache := []models.Trend{cachedTrend()}
	listed := copyTrendsForResponse(cache)

	if len(listed) != 1 {
		t.Fatalf("got %d trends, want 1", len(listed))
	}
	if listed[0].Slug != "poster" {
		t.Errorf("slug = %q", listed[0].Slug)
	}
	if len(listed[0].SampleImageURLs) != 2 {
		t.Errorf("samples = %d, want 2", len(listed[0].SampleImageURLs))
	}
	if len(listed[0].Inputs.Background.Presets) != 1 {
		t.Errorf("presets = %d, want 1", len(listed[0].Inputs.Background.Presets))
	}
	if len(listed[0].Inputs.Fields[0].Choices) != 1 {
		t.Errorf("choices = %d, want 1", len(listed[0].Inputs.Fields[0].Choices))
	}
	if listed[0].Inputs.Fields[0].Choices[0].ImageKey != "trends/poster/era-1985.jpg" {
		t.Errorf("choice key = %q", listed[0].Inputs.Fields[0].Choices[0].ImageKey)
	}
}

func TestTrendVisibleNow_Window(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	base := func() models.Trend {
		return models.Trend{Status: models.TrendStatusActive}
	}

	if !trendVisibleNow(base(), now) {
		t.Error("an active trend with no window should be visible")
	}

	notYet := base()
	notYet.StartsAt = &future
	if trendVisibleNow(notYet, now) {
		t.Error("a trend that has not started should be hidden")
	}

	over := base()
	over.EndsAt = &past
	if trendVisibleNow(over, now) {
		// The schedule is evaluated on read rather than by a job flipping
		// statuses, so an ends_at that passes at 3am retires the trend
		// without anything having to be running at 3am.
		t.Error("a trend past its end date should be hidden")
	}

	inWindow := base()
	inWindow.StartsAt = &past
	inWindow.EndsAt = &future
	if !trendVisibleNow(inWindow, now) {
		t.Error("a trend inside its window should be visible")
	}

	draft := base()
	draft.Status = models.TrendStatusDraft
	if trendVisibleNow(draft, now) {
		t.Error("a draft must never be served")
	}
}

// TestNormaliseTrendForClient_StampsTheTierTheServerWillUse is the regression
// guard for a price the button could understate.
//
// Generation is stripped before a trend reaches a device, so when a trend does
// not let the user pick a quality the client has no way to learn which tier the
// server will run. It guessed "flash". With a quality multiplier configured,
// that guess advertised the flash price for a generation billed at the pro one.
func TestNormaliseTrendForClient_StampsTheTierTheServerWillUse(t *testing.T) {
	t.Run("picker off, no default, generation says pro", func(t *testing.T) {
		trend := models.Trend{
			Inputs:     models.TrendInputs{Quality: models.QualityInput{Enabled: false}},
			Generation: models.TrendGeneration{QualityTier: "pro"},
		}
		normaliseTrendForClient(&trend)
		if trend.Inputs.Quality.Default != "pro" {
			t.Fatalf("default = %q, want pro — the tier the server actually runs",
				trend.Inputs.Quality.Default)
		}
	})

	t.Run("an explicit default is left alone", func(t *testing.T) {
		trend := models.Trend{
			Inputs:     models.TrendInputs{Quality: models.QualityInput{Enabled: false, Default: "flash"}},
			Generation: models.TrendGeneration{QualityTier: "pro"},
		}
		normaliseTrendForClient(&trend)
		if trend.Inputs.Quality.Default != "flash" {
			t.Fatalf("default = %q, want flash", trend.Inputs.Quality.Default)
		}
	})

	t.Run("picker on, default outside the allowed list", func(t *testing.T) {
		// The client would offer only what is allowed, so the stamped default
		// has to be one of them or the two disagree on the opening price.
		trend := models.Trend{
			Inputs: models.TrendInputs{Quality: models.QualityInput{
				Enabled: true, Allowed: []string{"pro"}, Default: "flash",
			}},
			Generation: models.TrendGeneration{QualityTier: "flash"},
		}
		normaliseTrendForClient(&trend)
		if trend.Inputs.Quality.Default != "pro" {
			t.Fatalf("default = %q, want pro — the only tier on offer",
				trend.Inputs.Quality.Default)
		}
	})

	t.Run("agrees with what a generation would resolve", func(t *testing.T) {
		// The property that matters: whatever is stamped, feeding it back
		// through the generator's own resolver returns the same tier.
		for _, trend := range []models.Trend{
			{Inputs: models.TrendInputs{Quality: models.QualityInput{Enabled: false}},
				Generation: models.TrendGeneration{QualityTier: "pro"}},
			{Inputs: models.TrendInputs{Quality: models.QualityInput{
				Enabled: true, Allowed: []string{"flash", "pro"}, Default: "pro"}}},
			{Inputs: models.TrendInputs{Quality: models.QualityInput{Enabled: true}},
				Generation: models.TrendGeneration{QualityTier: "flash"}},
		} {
			normaliseTrendForClient(&trend)
			stamped := trend.Inputs.Quality.Default
			if got := resolveTrendQuality(trend, stamped); got != stamped {
				t.Errorf("stamped %q but a generation resolves %q", stamped, got)
			}
		}
	})
}
