package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadForTest(t *testing.T) *StarConfig {
	t.Helper()
	os.Unsetenv("STARS_CONFIG_PATH")
	if err := LoadStars(); err != nil {
		t.Fatalf("embedded star config must be valid: %v", err)
	}
	return Stars
}

func TestEmbeddedConfigIsValid(t *testing.T) {
	s := loadForTest(t)

	if s.Version <= 0 {
		t.Errorf("version = %d, want > 0", s.Version)
	}
	if len(s.Packs) == 0 {
		t.Error("no packs configured")
	}
	// The `_note` keys inside models/tiers are documentation and must not
	// survive decoding as if they were real entries.
	if _, ok := s.Models["_note"]; ok {
		t.Error("_note leaked into models")
	}
	if _, ok := s.Tiers["_note"]; ok {
		t.Error("_note leaked into tiers")
	}
}

func TestCheapestTierDrivesFreeThreshold(t *testing.T) {
	s := loadForTest(t)

	// The suppression threshold must be derived, never hardcoded: it is what
	// keeps "a user who can afford to generate gets no free try-on" correct
	// after a repricing.
	want := 1 << 30
	for _, byQuality := range s.Tiers {
		for _, cost := range byQuality {
			if cost < want {
				want = cost
			}
		}
	}
	if got := s.CheapestTierStars(); got != want {
		t.Errorf("CheapestTierStars() = %d, want %d", got, want)
	}
}

func TestTierCostRejectsUnknownCombinations(t *testing.T) {
	s := loadForTest(t)

	// An unpriced combination must be an error, never a free generation.
	if _, ok := s.TierCost("individual", "nonexistent"); ok {
		t.Error("unknown quality was priced")
	}
	if _, ok := s.TierCost("quartet", "flash"); ok {
		t.Error("unknown try-on type was priced")
	}
	if cost, ok := s.TierCost("individual", "flash"); !ok || cost <= 0 {
		t.Errorf("individual/flash = (%d, %v), want a positive cost", cost, ok)
	}
}

func TestNormaliseQualityNeverEscalates(t *testing.T) {
	s := loadForTest(t)

	// A bad or missing quality must fall back to the cheap default. Falling
	// back *up* would let a malformed request buy the expensive model.
	for _, in := range []string{"", "nonsense", "PRO-ISH", "  "} {
		if got := s.NormaliseQuality(in); got != s.DefaultQuality {
			t.Errorf("NormaliseQuality(%q) = %q, want %q", in, got, s.DefaultQuality)
		}
	}
	if got := s.NormaliseQuality("  PRO "); got != "pro" {
		t.Errorf("NormaliseQuality(\"  PRO \") = %q, want \"pro\"", got)
	}
}

func TestFreeCoversOnlyTheFreeTier(t *testing.T) {
	s := loadForTest(t)

	if !s.FreeCovers("individual", s.Free.FreeQuality) {
		t.Error("individual at the free quality should be covered")
	}
	if s.FreeCovers("individual", "pro") {
		t.Error("pro quality must never be free")
	}
	if s.FreeCovers("group", s.Free.FreeQuality) {
		t.Error("group try-ons must never be free")
	}
}

func TestMinStarValueUsesTheCheapestRate(t *testing.T) {
	s := loadForTest(t)

	// Margins must be computed against what a customer on the biggest pack
	// pays, not the headline rate — otherwise the best customers are the
	// least profitable and nobody notices.
	got := s.MinStarValueINR()
	for _, p := range s.Packs {
		rate := float64(p.PriceINR) / float64(p.Stars)
		if rate < got-1e-9 {
			t.Errorf("MinStarValueINR() = %.4f but pack %s is cheaper at %.4f", got, p.ProductID, rate)
		}
	}
}

func TestEveryTierClearsTheHardMarginFloor(t *testing.T) {
	s := loadForTest(t)

	for _, m := range s.Margins() {
		if m.BelowMin {
			t.Errorf("%s/%s priced at %d stars is below the %.2fx floor "+
				"(net ₹%.2f vs cost ₹%.2f); needs %d stars",
				m.Type, m.Quality, m.Stars, s.Economics.MinMarginMultiple,
				m.NetINR, m.CostINR, m.MinStars)
		}
	}
}

// writeConfig materialises a config override and points LoadStars at it.
func writeConfig(t *testing.T, doc map[string]interface{}) error {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stars.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STARS_CONFIG_PATH", path)
	return LoadStars()
}

// baseConfig is a minimal valid document that each test then breaks in one
// specific way.
func baseConfig() map[string]interface{} {
	return map[string]interface{}{
		"version": 1, "currency": "INR", "default_quality": "flash",
		"economics": map[string]interface{}{
			"usd_inr": 88.0, "play_service_fee_pct": 15.0,
			"min_margin_multiple": 1.0, "target_margin_multiple": 2.0,
		},
		"models": map[string]interface{}{
			"flash": map[string]interface{}{
				"gemini_model": "m-flash", "est_cost_usd": 0.04,
				"timeout_secs": 45, "multi_timeout_secs": 90,
			},
		},
		"tiers": map[string]interface{}{"individual": map[string]int{"flash": 10}},
		"packs": []map[string]interface{}{
			{"product_id": "p1", "stars": 100, "price_inr": 100},
		},
		"free": map[string]interface{}{
			"welcome_credits": 5, "returning_welcome_credits": 1,
			"daily_free_count": 1, "guest_daily_free_count": 1,
			"free_quality": "flash", "free_types": []string{"individual"},
			"suppress_when_affordable": true,
		},
		"identity": map[string]interface{}{"enabled": true, "normalise_gmail_dots": true},
		"billing": map[string]interface{}{
			"package_name": "com.example.app", "hold_expiry_minutes": 5,
			"acknowledge_window_hours": 72,
		},
	}
}

func TestValidationRejectsDangerousConfigs(t *testing.T) {
	// Each of these would cost real money or strand a paying user if it were
	// allowed to boot, so validation must be fatal rather than forgiving.
	tests := []struct {
		name   string
		break_ func(m map[string]interface{})
	}{
		{"tier referencing an undefined model", func(m map[string]interface{}) {
			m["tiers"] = map[string]interface{}{"individual": map[string]int{"ultra": 10}}
		}},
		{"zero-cost tier", func(m map[string]interface{}) {
			m["tiers"] = map[string]interface{}{"individual": map[string]int{"flash": 0}}
		}},
		{"duplicate pack product ids", func(m map[string]interface{}) {
			m["packs"] = []map[string]interface{}{
				{"product_id": "dup", "stars": 100, "price_inr": 100},
				{"product_id": "dup", "stars": 200, "price_inr": 190},
			}
		}},
		{"free-tier quality that is not a model", func(m map[string]interface{}) {
			m["free"].(map[string]interface{})["free_quality"] = "ghost"
		}},
		{"default quality that is not a model", func(m map[string]interface{}) {
			m["default_quality"] = "ghost"
		}},
		{"missing package name", func(m map[string]interface{}) {
			m["billing"].(map[string]interface{})["package_name"] = ""
		}},
		{"hold expiry shorter than the generation timeout", func(m map[string]interface{}) {
			// A hold that expires mid-generation is refunded while the work
			// is still running, letting the same stars fund a second one.
			m["billing"].(map[string]interface{})["hold_expiry_minutes"] = 1
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := baseConfig()
			tc.break_(doc)
			if err := writeConfig(t, doc); err == nil {
				t.Error("expected the config to be rejected, but it loaded")
			}
		})
	}
}

func TestValidConfigOverrideLoads(t *testing.T) {
	if err := writeConfig(t, baseConfig()); err != nil {
		t.Fatalf("base config should load: %v", err)
	}
	if Stars.CheapestTierStars() != 10 {
		t.Errorf("cheapest tier = %d, want 10", Stars.CheapestTierStars())
	}
}
