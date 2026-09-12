package models

import "math"

// TrendSelection is the part of a user's answers that affects the price.
type TrendSelection struct {
	People      int
	ExtraImages int
	Outputs     int
	Quality     string
}

// Breakdown keys, so the stored PricingBreakdown and the admin simulator
// agree on what each line is called.
const (
	PriceLineFlat    = "flat"
	PriceLineBase    = "base"
	PriceLinePeople  = "people"
	PriceLineImages  = "extra_images"
	PriceLineOutputs = "extra_outputs"
	PriceLineQuality = "quality_multiplier"
	PriceLineClamped = "clamped"
	PriceLineTotal   = "total"
)

// Cost returns what a selection costs, and the line items behind it.
//
// This is the only place a trend's price is decided. The client ships an
// identical formula (fitly-app/src/config/trends.ts) purely so the button can
// say "★25" before the request is made — it is never the number that charges,
// because the server recomputes from this block and the resolved inputs
// rather than from anything the client sent.
//
// MaxStars is applied last and deliberately: a mistyped per_person_stars
// should cost one confused user a capped amount, not their whole balance.
func (p TrendPricing) Cost(sel TrendSelection) (int, map[string]int) {
	breakdown := map[string]int{}

	var subtotal int
	if p.Mode == PricingFlat {
		subtotal = p.FlatStars
		breakdown[PriceLineFlat] = p.FlatStars
	} else {
		subtotal = p.BaseStars
		breakdown[PriceLineBase] = p.BaseStars

		if extraPeople := sel.People - p.IncludedPeople; extraPeople > 0 && p.PerPersonStars > 0 {
			line := extraPeople * p.PerPersonStars
			subtotal += line
			breakdown[PriceLinePeople] = line
		}
		if sel.ExtraImages > 0 && p.PerExtraImageStars > 0 {
			line := sel.ExtraImages * p.PerExtraImageStars
			subtotal += line
			breakdown[PriceLineImages] = line
		}
		if extraOutputs := sel.Outputs - 1; extraOutputs > 0 && p.PerExtraOutputStars > 0 {
			line := extraOutputs * p.PerExtraOutputStars
			subtotal += line
			breakdown[PriceLineOutputs] = line
		}
	}

	if mult, ok := p.QualityMultiplier[sel.Quality]; ok && mult > 0 && mult != 1 {
		scaled := int(math.Round(float64(subtotal) * mult))
		breakdown[PriceLineQuality] = scaled - subtotal
		subtotal = scaled
	}

	clamped := subtotal
	if p.MinStars > 0 && clamped < p.MinStars {
		clamped = p.MinStars
	}
	if p.MaxStars > 0 && clamped > p.MaxStars {
		clamped = p.MaxStars
	}
	if clamped != subtotal {
		breakdown[PriceLineClamped] = clamped - subtotal
	}

	// A trend that resolves to zero would generate for nothing on every run,
	// which is a pricing mistake rather than a free tier — FreeEligible is
	// how a trend is made free, and it goes through the daily allowance.
	if clamped < 0 {
		clamped = 0
	}

	breakdown[PriceLineTotal] = clamped
	return clamped, breakdown
}

// Normalise fills in the defaults that keep Cost sane for a half-written
// document, so an admin who saved a draft with an empty pricing block gets a
// trend that refuses to be cheap rather than one that is accidentally free.
func (p *TrendPricing) Normalise() {
	if p.Mode != PricingFlat && p.Mode != PricingDynamic {
		p.Mode = PricingFlat
	}
	if p.QualityMultiplier == nil {
		p.QualityMultiplier = map[string]float64{}
	}
	if p.Mode == PricingFlat && p.FlatStars <= 0 {
		p.FlatStars = 1
	}
	if p.IncludedPeople < 0 {
		p.IncludedPeople = 0
	}
}
