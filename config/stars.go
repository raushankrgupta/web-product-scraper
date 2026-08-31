package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
)

// starsJSON is compiled into the binary so a deploy can never ship without a
// star configuration. An external file (STARS_CONFIG_PATH) overrides it, which
// is how you reprice without a rebuild.
//
//go:embed stars.json
var starsJSON []byte

// Stars is the process-wide star economy. Populated by LoadStars during boot,
// before any handler is registered. Read-only afterwards.
var Stars *StarConfig

// StarConfig mirrors config/stars.json. Fields prefixed with `_` in the JSON
// are documentation for whoever edits the file and are deliberately not
// mapped here.
type StarConfig struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at"`
	Currency  string `json:"currency"`

	Economics StarEconomics             `json:"economics"`
	Models    map[string]StarModel      `json:"models"`
	Tiers     map[string]map[string]int `json:"tiers"`
	Packs     []StarPack                `json:"packs"`
	Free      StarFreeRules             `json:"free"`
	Identity  StarIdentityRules         `json:"identity"`
	Billing   StarBillingRules          `json:"billing"`

	DefaultQuality string `json:"default_quality"`

	// cheapestTier is derived, not configured: the smallest star cost across
	// every (type, quality) pair. It is the threshold at which free
	// entitlements are suppressed, so it must track repricing automatically.
	cheapestTier int
}

// UnmarshalJSON decodes the config, skipping any map key that begins with an
// underscore. Those keys are editor-facing notes in config/stars.json — they
// sit inside `models` and `tiers` so that a price and its justification can
// never drift into separate files.
func (s *StarConfig) UnmarshalJSON(b []byte) error {
	type alias StarConfig // avoids recursing into this method
	var wire struct {
		alias
		Models map[string]json.RawMessage `json:"models"`
		Tiers  map[string]json.RawMessage `json:"tiers"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*s = StarConfig(wire.alias)

	s.Models = make(map[string]StarModel, len(wire.Models))
	for name, raw := range wire.Models {
		if strings.HasPrefix(name, "_") {
			continue
		}
		var m StarModel
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("model %q: %w", name, err)
		}
		s.Models[name] = m
	}

	s.Tiers = make(map[string]map[string]int, len(wire.Tiers))
	for name, raw := range wire.Tiers {
		if strings.HasPrefix(name, "_") {
			continue
		}
		var t map[string]int
		if err := json.Unmarshal(raw, &t); err != nil {
			return fmt.Errorf("tier %q: %w", name, err)
		}
		s.Tiers[name] = t
	}
	return nil
}

// StarEconomics holds the assumptions the margin checker reasons about. None
// of it affects runtime behaviour — it exists so that a price change and the
// justification for it live in the same file.
type StarEconomics struct {
	USDINR            float64 `json:"usd_inr"`
	PlayServiceFeePct float64 `json:"play_service_fee_pct"`
	// MinMarginMultiple is a hard floor — tools/stars_check fails below it.
	// TargetMarginMultiple is advisory and only warns. Both compare net
	// revenue (after Play's fee) to raw model cost at the cheapest star rate.
	MinMarginMultiple    float64 `json:"min_margin_multiple"`
	TargetMarginMultiple float64 `json:"target_margin_multiple"`
}

// StarModel maps a quality name the API accepts onto a literal Gemini model
// id plus its generation budget.
type StarModel struct {
	Label            string  `json:"label"`
	Tagline          string  `json:"tagline"`
	GeminiModel      string  `json:"gemini_model"`
	EstCostUSD       float64 `json:"est_cost_usd"`
	TimeoutSecs      int     `json:"timeout_secs"`
	MultiTimeoutSecs int     `json:"multi_timeout_secs"`
}

// StarPack is one purchasable bundle. ProductID must match the in-app product
// id in Play Console byte for byte.
type StarPack struct {
	ProductID string `json:"product_id"`
	Stars     int    `json:"stars"`
	PriceINR  int    `json:"price_inr"`
	Label     string `json:"label"`
	Badge     string `json:"badge"`
}

// StarFreeRules describes what a user gets without paying.
type StarFreeRules struct {
	WelcomeCredits          int      `json:"welcome_credits"`
	ReturningWelcomeCredits int      `json:"returning_welcome_credits"`
	DailyFreeCount          int      `json:"daily_free_count"`
	GuestDailyFreeCount     int      `json:"guest_daily_free_count"`
	FreeQuality             string   `json:"free_quality"`
	FreeTypes               []string `json:"free_types"`
	SuppressWhenAffordable  bool     `json:"suppress_when_affordable"`
}

// StarIdentityRules configures returning-user detection.
type StarIdentityRules struct {
	Enabled            bool `json:"enabled"`
	NormaliseGmailDots bool `json:"normalise_gmail_dots"`
}

// StarBillingRules configures Google Play consumable handling.
type StarBillingRules struct {
	PackageName            string `json:"package_name"`
	AcknowledgeWindowHours int    `json:"acknowledge_window_hours"`
	HoldExpiryMinutes      int    `json:"hold_expiry_minutes"`
}

// LoadStars parses the star configuration and installs it as config.Stars.
//
// It returns an error rather than logging and continuing, and main.go treats
// that as fatal. Every other config value in this package degrades to a
// default when it is wrong; this one must not. A typo that makes a Pro
// generation cost 1 star instead of 25 is a silent, uncapped bill.
func LoadStars() error {
	raw := starsJSON
	source := "embedded"

	if path := strings.TrimSpace(os.Getenv("STARS_CONFIG_PATH")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("STARS_CONFIG_PATH=%q could not be read: %w", path, err)
		}
		raw, source = b, path
	}

	var sc StarConfig
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&sc); err != nil {
		return fmt.Errorf("parse star config (%s): %w", source, err)
	}

	if err := sc.validate(); err != nil {
		return fmt.Errorf("invalid star config (%s): %w", source, err)
	}

	Stars = &sc
	slog.Info("star config loaded",
		"source", source, "version", sc.Version, "updated_at", sc.UpdatedAt,
		"packs", len(sc.Packs), "cheapest_tier_stars", sc.cheapestTier)
	return nil
}

// validate rejects any configuration that could charge the wrong amount,
// reference a model that does not exist, or credit a pack twice.
func (s *StarConfig) validate() error {
	if len(s.Models) == 0 {
		return fmt.Errorf("no models defined")
	}
	for name, m := range s.Models {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if m.GeminiModel == "" {
			return fmt.Errorf("model %q has no gemini_model", name)
		}
		if m.TimeoutSecs <= 0 || m.MultiTimeoutSecs <= 0 {
			return fmt.Errorf("model %q has a non-positive timeout", name)
		}
	}

	if _, ok := s.Models[s.DefaultQuality]; !ok {
		return fmt.Errorf("default_quality %q is not a defined model", s.DefaultQuality)
	}

	if len(s.Tiers) == 0 {
		return fmt.Errorf("no tiers defined")
	}
	s.cheapestTier = math.MaxInt32
	for tryOnType, byQuality := range s.Tiers {
		if strings.HasPrefix(tryOnType, "_") {
			continue
		}
		for quality, cost := range byQuality {
			if _, ok := s.Models[quality]; !ok {
				return fmt.Errorf("tier %s.%s references unknown quality %q", tryOnType, quality, quality)
			}
			if cost <= 0 {
				return fmt.Errorf("tier %s.%s costs %d stars; must be positive", tryOnType, quality, cost)
			}
			if cost < s.cheapestTier {
				s.cheapestTier = cost
			}
		}
	}
	if s.cheapestTier == math.MaxInt32 {
		return fmt.Errorf("tiers block contained no usable entries")
	}

	if len(s.Packs) == 0 {
		return fmt.Errorf("no packs defined")
	}
	seen := map[string]bool{}
	for _, p := range s.Packs {
		if p.ProductID == "" {
			return fmt.Errorf("pack %q has no product_id", p.Label)
		}
		if seen[p.ProductID] {
			return fmt.Errorf("duplicate pack product_id %q", p.ProductID)
		}
		seen[p.ProductID] = true
		if p.Stars <= 0 || p.PriceINR <= 0 {
			return fmt.Errorf("pack %q has a non-positive stars or price", p.ProductID)
		}
	}

	if _, ok := s.Models[s.Free.FreeQuality]; !ok {
		return fmt.Errorf("free.free_quality %q is not a defined model", s.Free.FreeQuality)
	}
	for _, t := range s.Free.FreeTypes {
		if _, ok := s.Tiers[t]; !ok {
			return fmt.Errorf("free.free_types references unknown try-on type %q", t)
		}
	}
	if s.Free.WelcomeCredits < 0 || s.Free.ReturningWelcomeCredits < 0 || s.Free.DailyFreeCount < 0 {
		return fmt.Errorf("free entitlements cannot be negative")
	}

	if s.Billing.PackageName == "" {
		return fmt.Errorf("billing.package_name is required")
	}
	if s.Billing.HoldExpiryMinutes <= 0 {
		return fmt.Errorf("billing.hold_expiry_minutes must be positive")
	}
	// A hold that expires before the generation it is holding for would let a
	// user spend the same stars twice on two slow requests.
	longest := 0
	for _, m := range s.Models {
		if m.MultiTimeoutSecs > longest {
			longest = m.MultiTimeoutSecs
		}
	}
	if s.Billing.HoldExpiryMinutes*60 <= longest {
		return fmt.Errorf("billing.hold_expiry_minutes (%dm) must exceed the longest generation timeout (%ds)",
			s.Billing.HoldExpiryMinutes, longest)
	}

	return nil
}

// ---------------------------------------------------------------- accessors

// TierCost returns the star price of one generation. ok is false for an
// unknown type/quality combination, which callers must treat as a 400 rather
// than as "free".
func (s *StarConfig) TierCost(tryOnType, quality string) (int, bool) {
	byQuality, ok := s.Tiers[tryOnType]
	if !ok {
		return 0, false
	}
	cost, ok := byQuality[quality]
	if !ok || cost <= 0 {
		return 0, false
	}
	return cost, true
}

// Model resolves a quality name to its model definition.
func (s *StarConfig) Model(quality string) (StarModel, bool) {
	m, ok := s.Models[quality]
	return m, ok
}

// GeminiModelFor returns the literal model id for a quality, falling back to
// the default quality's model. The fallback is deliberate: an unrecognised
// quality should never escalate a user to the expensive model.
func (s *StarConfig) GeminiModelFor(quality string) string {
	if m, ok := s.Models[quality]; ok {
		return m.GeminiModel
	}
	return s.Models[s.DefaultQuality].GeminiModel
}

// NormaliseQuality maps user input onto a known quality, defaulting rather
// than erroring. Same reasoning as GeminiModelFor: default down, never up.
func (s *StarConfig) NormaliseQuality(quality string) string {
	q := strings.ToLower(strings.TrimSpace(quality))
	if _, ok := s.Models[q]; ok {
		return q
	}
	return s.DefaultQuality
}

// CheapestTierStars is the star cost of the least expensive generation
// available. Free entitlements are suppressed at or above this balance —
// a user who can afford to generate is not shown a free try-on.
func (s *StarConfig) CheapestTierStars() int { return s.cheapestTier }

// PackByProductID looks up a pack by its Play product id. Purchases for an
// unknown id are rejected rather than credited with a guess.
func (s *StarConfig) PackByProductID(id string) (StarPack, bool) {
	for _, p := range s.Packs {
		if p.ProductID == id {
			return p, true
		}
	}
	return StarPack{}, false
}

// FreeCovers reports whether free entitlements may be spent on this
// combination. couple/group and Pro quality always cost stars.
func (s *StarConfig) FreeCovers(tryOnType, quality string) bool {
	if quality != s.Free.FreeQuality {
		return false
	}
	for _, t := range s.Free.FreeTypes {
		if t == tryOnType {
			return true
		}
	}
	return false
}

// SortedPacks returns packs cheapest-first, which is the order the store
// screen renders them in.
func (s *StarConfig) SortedPacks() []StarPack {
	out := append([]StarPack(nil), s.Packs...)
	sort.Slice(out, func(i, j int) bool { return out[i].PriceINR < out[j].PriceINR })
	return out
}

// MinStarValueINR is the rupee value of one star in the *cheapest* pack — the
// rate a whale pays. Margins must be computed against this, never against the
// headline ₹1/star, or the biggest customers become the least profitable.
func (s *StarConfig) MinStarValueINR() float64 {
	min := math.MaxFloat64
	for _, p := range s.Packs {
		if v := float64(p.PriceINR) / float64(p.Stars); v < min {
			min = v
		}
	}
	if min == math.MaxFloat64 {
		return 0
	}
	return min
}

// TierMargin reports the per-generation economics of one tier at the worst
// (cheapest) star rate. Used by tools/stars_check and the /billing/economics
// debug endpoint; never on a request path.
type TierMargin struct {
	Type      string
	Quality   string
	Stars     int
	GrossINR  float64
	NetINR    float64 // after Play's service fee
	CostINR   float64
	MarginINR float64
	// Multiple is net revenue divided by model cost — the headline number.
	Multiple float64
	// MinStars / TargetStars are the star prices that would achieve the hard
	// floor and the advisory target respectively.
	MinStars    int
	TargetStars int
	BelowMin    bool
	BelowTarget bool
}

// Margins computes TierMargin for every configured tier.
func (s *StarConfig) Margins() []TierMargin {
	starValue := s.MinStarValueINR()
	keep := 1 - s.Economics.PlayServiceFeePct/100

	var out []TierMargin
	for tryOnType, byQuality := range s.Tiers {
		if strings.HasPrefix(tryOnType, "_") {
			continue
		}
		for quality, stars := range byQuality {
			m, ok := s.Models[quality]
			if !ok {
				continue
			}
			cost := m.EstCostUSD * s.Economics.USDINR
			gross := float64(stars) * starValue
			net := gross * keep

			// Stars needed so that net revenue >= multiple x cost.
			starsFor := func(mult float64) int {
				if starValue <= 0 || keep <= 0 || mult <= 0 {
					return 0
				}
				return int(math.Ceil(cost * mult / (starValue * keep)))
			}
			minStars := starsFor(s.Economics.MinMarginMultiple)
			targetStars := starsFor(s.Economics.TargetMarginMultiple)

			multiple := 0.0
			if cost > 0 {
				multiple = net / cost
			}

			out = append(out, TierMargin{
				Type: tryOnType, Quality: quality, Stars: stars,
				GrossINR: gross, NetINR: net, CostINR: cost,
				MarginINR: net - cost, Multiple: multiple,
				MinStars: minStars, TargetStars: targetStars,
				BelowMin:    stars < minStars,
				BelowTarget: stars < targetStars,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Quality < out[j].Quality
	})
	return out
}
