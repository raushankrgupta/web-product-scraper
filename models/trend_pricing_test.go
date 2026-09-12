package models

import "testing"

func TestTrendPricing_Flat(t *testing.T) {
	p := TrendPricing{Mode: PricingFlat, FlatStars: 20}

	// A flat price ignores everything about the selection. That is the point
	// of it: "Ghibli Style" costs 20 stars whether you picked one photo or
	// spent a minute fiddling with the pose box.
	for _, sel := range []TrendSelection{
		{People: 1, Outputs: 1},
		{People: 4, Outputs: 4, ExtraImages: 3},
	} {
		got, breakdown := p.Cost(sel)
		if got != 20 {
			t.Errorf("Cost(%+v) = %d, want 20", sel, got)
		}
		if breakdown[PriceLineTotal] != 20 {
			t.Errorf("breakdown total = %d, want 20", breakdown[PriceLineTotal])
		}
	}
}

func TestTrendPricing_Dynamic(t *testing.T) {
	p := TrendPricing{
		Mode:                PricingDynamic,
		BaseStars:           15,
		IncludedPeople:      1,
		PerPersonStars:      8,
		PerExtraImageStars:  2,
		PerExtraOutputStars: 10,
	}

	cases := []struct {
		name string
		sel  TrendSelection
		want int
	}{
		{"one person, one output", TrendSelection{People: 1, Outputs: 1}, 15},
		{"second person costs extra", TrendSelection{People: 2, Outputs: 1}, 23},
		{"fourth person", TrendSelection{People: 4, Outputs: 1}, 39},
		{"extra outputs", TrendSelection{People: 1, Outputs: 3}, 35},
		{"uploaded photos", TrendSelection{People: 1, Outputs: 1, ExtraImages: 2}, 19},
		// Fewer people than are included must not produce a discount.
		{"zero people never goes below base", TrendSelection{People: 0, Outputs: 1}, 15},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := p.Cost(c.sel); got != c.want {
				t.Errorf("Cost = %d, want %d", got, c.want)
			}
		})
	}
}

func TestTrendPricing_QualityMultiplier(t *testing.T) {
	p := TrendPricing{
		Mode:              PricingDynamic,
		BaseStars:         15,
		IncludedPeople:    1,
		PerPersonStars:    8,
		QualityMultiplier: map[string]float64{"flash": 1, "pro": 2},
	}

	if got, _ := p.Cost(TrendSelection{People: 2, Outputs: 1, Quality: "flash"}); got != 23 {
		t.Errorf("flash = %d, want 23", got)
	}
	if got, _ := p.Cost(TrendSelection{People: 2, Outputs: 1, Quality: "pro"}); got != 46 {
		t.Errorf("pro = %d, want 46", got)
	}
	// An unpriced quality must not silently multiply by zero and hand out a
	// free generation on the expensive model.
	if got, _ := p.Cost(TrendSelection{People: 2, Outputs: 1, Quality: "ultra"}); got != 23 {
		t.Errorf("unknown quality = %d, want the unmultiplied 23", got)
	}
}

func TestTrendPricing_MaxStarsIsTheSafetyRail(t *testing.T) {
	// The scenario this exists for: a mistyped per_person_stars. It should
	// cost one confused user a capped amount, not their whole balance.
	p := TrendPricing{
		Mode:           PricingDynamic,
		BaseStars:      10,
		PerPersonStars: 5000, // fat-fingered
		MaxStars:       120,
	}
	got, breakdown := p.Cost(TrendSelection{People: 4, Outputs: 1})
	if got != 120 {
		t.Fatalf("Cost = %d, want clamped to 120", got)
	}
	if breakdown[PriceLineClamped] == 0 {
		t.Error("a clamped price must say so in its breakdown")
	}
}

func TestTrendPricing_MinStars(t *testing.T) {
	p := TrendPricing{Mode: PricingDynamic, BaseStars: 2, MinStars: 10}
	if got, _ := p.Cost(TrendSelection{People: 1, Outputs: 1}); got != 10 {
		t.Errorf("Cost = %d, want raised to the 10 minimum", got)
	}
}

func TestTrendPricing_NeverNegative(t *testing.T) {
	// Nothing should be able to produce a negative charge — a refund is not a
	// pricing outcome.
	p := TrendPricing{Mode: PricingDynamic, BaseStars: -50}
	if got, _ := p.Cost(TrendSelection{People: 1, Outputs: 1}); got != 0 {
		t.Errorf("Cost = %d, want 0", got)
	}
}

func TestTrendPricing_NormaliseRefusesToBeFree(t *testing.T) {
	// A half-written draft with an empty pricing block must not become a
	// trend that generates for nothing on every run. FreeEligible is how a
	// trend is made free, and it goes through the daily allowance instead.
	p := TrendPricing{}
	p.Normalise()
	if p.Mode != PricingFlat {
		t.Errorf("Mode = %q, want flat", p.Mode)
	}
	if got, _ := p.Cost(TrendSelection{People: 1, Outputs: 1}); got < 1 {
		t.Errorf("Cost = %d, want at least 1", got)
	}
}
