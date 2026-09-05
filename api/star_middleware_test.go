package api

import (
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

func setupStars(t *testing.T) {
	t.Helper()
	if err := config.LoadStars(); err != nil {
		t.Fatalf("load star config: %v", err)
	}
}

func TestTryOnTypeForPath(t *testing.T) {
	setupStars(t)

	cases := map[string]string{
		"/try-on":            "individual",
		"/try-on/individual": "individual",
		"/try-on/couple":     "couple",
		"/try-on/group":      "group",
		"/try-on/guest":      "individual",
	}
	for path, want := range cases {
		if got := tryOnTypeForPath(path); got != want {
			t.Errorf("tryOnTypeForPath(%q) = %q, want %q", path, got, want)
		}
		// Every mapped type must actually be priced, or the request would
		// fail at reservation time with a 400 the user cannot act on.
		if _, ok := config.Stars.TierCost(want, config.Stars.DefaultQuality); !ok {
			t.Errorf("type %q from path %q is not priced in stars.json", want, path)
		}
	}
}

func TestPlanBypassesStars(t *testing.T) {
	// Accounts flagged plus/pro predate the star system. They must keep
	// working rather than suddenly finding themselves unable to generate.
	if !planBypassesStars(models.PlanPlus) || !planBypassesStars(models.PlanPro) {
		t.Error("legacy paid plans must bypass star charging")
	}
	if planBypassesStars(models.PlanFree) || planBypassesStars(models.PlanGuest) {
		t.Error("free and guest plans must be charged")
	}
}

func TestSynthesiseLegacyQuotaBlocksOnlyWhenBroke(t *testing.T) {
	setupStars(t)
	threshold := config.Stars.CheapestTierStars()

	tests := []struct {
		name        string
		plan        string
		summary     utils.StarSummary
		wantBlocked bool
	}{
		{
			// Old builds read quota.remaining to grey out the button. A user
			// holding stars must not be told to come back tomorrow.
			name:    "has enough stars",
			plan:    models.PlanFree,
			summary: utils.StarSummary{Stars: threshold},
		},
		{
			name:    "free try-on available",
			plan:    models.PlanFree,
			summary: utils.StarSummary{Stars: 0, FreeAvailable: true},
		},
		{
			name:    "legacy paid plan with nothing",
			plan:    models.PlanPro,
			summary: utils.StarSummary{},
		},
		{
			name:        "no stars and no free allowance",
			plan:        models.PlanFree,
			summary:     utils.StarSummary{Stars: threshold - 1, FreeAvailable: false},
			wantBlocked: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := synthesiseLegacyQuota(tc.plan, tc.summary)
			blocked := q.Limit > 0 && q.Remaining <= 0
			if blocked != tc.wantBlocked {
				t.Errorf("blocked = %v, want %v (limit=%d remaining=%d)",
					blocked, tc.wantBlocked, q.Limit, q.Remaining)
			}
		})
	}
}

func TestGetQualityFromContextDefaultsDown(t *testing.T) {
	setupStars(t)

	// A handler reading an unpopulated context must get the cheap tier. The
	// opposite would mean an unbilled request silently buying the expensive
	// model.
	if got := GetQualityFromContext(t.Context()); got != config.Stars.DefaultQuality {
		t.Errorf("GetQualityFromContext(empty) = %q, want %q", got, config.Stars.DefaultQuality)
	}
}

func TestGuestQualityIsPinnedToTheFreeTier(t *testing.T) {
	setupStars(t)

	// Guests cannot buy stars, so offering them a paid tier is a dead end.
	// The middleware pins them; this asserts the config makes that coherent.
	fq := config.Stars.Free.FreeQuality
	if _, ok := config.Stars.Model(fq); !ok {
		t.Fatalf("free quality %q is not a defined model", fq)
	}
	if !config.Stars.FreeCovers("individual", fq) {
		t.Error("the guest path (individual, free quality) must be free-eligible")
	}
}
