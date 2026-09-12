package api

import (
	"fmt"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Validating a trend generation request against its schema.
//
// This half is deliberately pure — no database, no network — because it runs
// twice: once in the billing resolver to decide what to charge, and once in
// the handler to build the generation. Both must reach the identical verdict.
// If the resolver clamped a request the handler then honoured in full, a user
// could ask for five subjects, be charged for two, and receive five.
//
// So nothing here clamps. Anything outside the schema is rejected, and it is
// rejected *before* a star hold exists, which means a malformed request costs
// nothing and needs no refund.

// trendGenerateRequest is the POST /trends/generate body.
type trendGenerateRequest struct {
	TrendID        string      `json:"trend_id"`
	Quality        string      `json:"quality"`
	IdempotencyKey string      `json:"idempotency_key"`
	Inputs         trendInputs `json:"inputs"`
}

type trendInputs struct {
	People     []trendPersonRef      `json:"people"`
	Wardrobe   []trendGarmentRef     `json:"wardrobe"`
	Background trendBackgroundChoice `json:"background"`

	Pose        string `json:"pose"`
	Instruction string `json:"instruction"`
	Aspect      string `json:"aspect"`
	Outputs     int    `json:"outputs"`

	Fields map[string]interface{} `json:"fields"`
}

type trendPersonRef struct {
	Source   string `json:"source"`
	PersonID string `json:"person_id"`
	UploadID string `json:"upload_id"`
}

type trendGarmentRef struct {
	Slot     string `json:"slot"`
	ItemID   string `json:"item_id"`
	UploadID string `json:"upload_id"`
	Text     string `json:"text"`
}

type trendBackgroundChoice struct {
	Mode     string `json:"mode"`
	PresetID string `json:"preset_id"`
	UploadID string `json:"upload_id"`
	Text     string `json:"text"`
}

// validatedTrendRequest is a request that has been checked against the schema.
// The counts on it are what pricing is computed from.
type validatedTrendRequest struct {
	Quality string
	Outputs int
	// People is the request's people list, in order.
	People []trendPersonRef
	// UploadCount is how many ad-hoc images the request carries across every
	// input. It is what per_extra_image_stars is charged against.
	UploadCount int
	Wardrobe    []trendGarmentRef
	Background  trendBackgroundChoice
	Pose        string
	Instruction string
	Aspect      string
	Fields      map[string]interface{}
}

// Selection turns a validated request into the pricing inputs.
func (v validatedTrendRequest) Selection() models.TrendSelection {
	return models.TrendSelection{
		People:      len(v.People),
		ExtraImages: v.UploadCount,
		Outputs:     v.Outputs,
		Quality:     v.Quality,
	}
}

// validateTrendRequest checks a request against a trend's schema.
//
// The returned error is safe to show a user: these are all "you asked for
// something this trend does not offer" conditions, and a vague message would
// leave someone stuck on a screen with no way to work out what is wrong.
func validateTrendRequest(trend models.Trend, req trendGenerateRequest) (validatedTrendRequest, error) {
	in := req.Inputs
	out := validatedTrendRequest{
		Fields: map[string]interface{}{},
	}

	// --- Quality ---
	out.Quality = resolveTrendQuality(trend, req.Quality)

	// --- Outputs ---
	outputs := in.Outputs
	if !trend.Inputs.Outputs.Enabled {
		outputs = 1
	} else {
		if outputs == 0 {
			outputs = trend.Inputs.Outputs.Default
		}
		if outputs == 0 {
			outputs = 1
		}
		lo, hi := trend.Inputs.Outputs.Min, trend.Inputs.Outputs.Max
		if lo <= 0 {
			lo = 1
		}
		if hi <= 0 {
			hi = lo
		}
		if outputs < lo || outputs > hi {
			return out, fmt.Errorf("choose between %d and %d images", lo, hi)
		}
	}
	out.Outputs = outputs

	// --- People ---
	people := trend.Inputs.People
	if !people.Enabled {
		if len(in.People) > 0 {
			return out, fmt.Errorf("this trend does not use photos of people")
		}
	} else {
		lo, hi := people.Min, people.Max
		if lo < 0 {
			lo = 0
		}
		if hi <= 0 {
			hi = lo
		}
		if hi <= 0 {
			hi = 1
		}
		if len(in.People) < lo {
			return out, fmt.Errorf("pick at least %d %s", lo, pluralPerson(lo))
		}
		if len(in.People) > hi {
			return out, fmt.Errorf("pick at most %d %s", hi, pluralPerson(hi))
		}

		allowed := sourceSet(people.Sources, models.PhotoSourceProfile)
		seenProfiles := map[string]bool{}
		for _, p := range in.People {
			source := strings.TrimSpace(p.Source)
			if source == "" {
				// An older client that only ever sent profile ids.
				source = models.PhotoSourceProfile
			}
			if !allowed[source] {
				return out, fmt.Errorf("photos from %q are not allowed on this trend", source)
			}
			switch source {
			case models.PhotoSourceProfile:
				if strings.TrimSpace(p.PersonID) == "" {
					return out, fmt.Errorf("a selected profile is missing")
				}
				// The same face twice is never what someone meant, and it
				// doubles the cost on a per-person price.
				if seenProfiles[p.PersonID] {
					return out, fmt.Errorf("that profile is already selected")
				}
				seenProfiles[p.PersonID] = true
			case models.PhotoSourceUpload:
				if strings.TrimSpace(p.UploadID) == "" {
					return out, fmt.Errorf("an uploaded photo is missing")
				}
				out.UploadCount++
			}
		}
		out.People = in.People
	}

	// --- Wardrobe ---
	wardrobe := trend.Inputs.Wardrobe
	if !wardrobe.Enabled {
		if len(in.Wardrobe) > 0 {
			return out, fmt.Errorf("this trend does not use wardrobe items")
		}
	} else {
		if wardrobe.Max > 0 && len(in.Wardrobe) > wardrobe.Max {
			return out, fmt.Errorf("pick at most %d wardrobe items", wardrobe.Max)
		}
		if len(in.Wardrobe) < wardrobe.Min {
			return out, fmt.Errorf("pick at least %d wardrobe items", wardrobe.Min)
		}
		allowedSlots := stringSet(wardrobe.Slots)
		allowedSources := sourceSet(wardrobe.Sources, "wardrobe")
		for _, g := range in.Wardrobe {
			if g.Slot != "" && len(allowedSlots) > 0 && !allowedSlots[g.Slot] {
				return out, fmt.Errorf("%q is not an option on this trend", g.Slot)
			}
			switch {
			case strings.TrimSpace(g.ItemID) != "":
				if !allowedSources["wardrobe"] {
					return out, fmt.Errorf("wardrobe items are not allowed on this trend")
				}
			case strings.TrimSpace(g.UploadID) != "":
				if !allowedSources["upload"] {
					return out, fmt.Errorf("uploaded garments are not allowed on this trend")
				}
				out.UploadCount++
			case strings.TrimSpace(g.Text) != "":
				if !allowedSources["text"] {
					return out, fmt.Errorf("described outfits are not allowed on this trend")
				}
			}
		}
		out.Wardrobe = in.Wardrobe
	}

	// --- Background ---
	background := trend.Inputs.Background
	chosen := trendBackgroundChoice{Mode: models.BackgroundModeNone}
	if background.Enabled {
		mode := strings.TrimSpace(in.Background.Mode)
		if mode == "" {
			mode = background.DefaultMode
		}
		if mode == "" {
			mode = models.BackgroundModeNone
		}
		if mode != models.BackgroundModeNone && !stringSet(background.Modes)[mode] {
			return out, fmt.Errorf("%q backgrounds are not available on this trend", mode)
		}
		chosen.Mode = mode
		switch mode {
		case models.BackgroundModePreset:
			found := false
			for _, preset := range background.Presets {
				if preset.ID == in.Background.PresetID {
					found = true
					break
				}
			}
			if !found {
				return out, fmt.Errorf("that background is no longer available")
			}
			chosen.PresetID = in.Background.PresetID
		case models.BackgroundModeUpload:
			if strings.TrimSpace(in.Background.UploadID) == "" {
				return out, fmt.Errorf("upload a background or choose another option")
			}
			chosen.UploadID = in.Background.UploadID
			out.UploadCount++
		case models.BackgroundModeText:
			text := utils.SanitizeTrendFreeText(in.Background.Text, charCap(background.TextMaxChars, 300))
			if text == "" {
				return out, fmt.Errorf("describe the background or choose another option")
			}
			chosen.Text = text
		}
	}
	out.Background = chosen

	// --- Pose and instruction ---
	var err error
	if out.Pose, err = validateTextInput(trend.Inputs.Pose, in.Pose, "pose"); err != nil {
		return out, err
	}
	if out.Instruction, err = validateTextInput(trend.Inputs.Instruction, in.Instruction, "instruction"); err != nil {
		return out, err
	}

	// --- Aspect ---
	if trend.Inputs.Aspect.Enabled {
		aspect := strings.TrimSpace(in.Aspect)
		if aspect == "" {
			aspect = trend.Inputs.Aspect.Default
		}
		if aspect != "" {
			valid := false
			for _, choice := range trend.Inputs.Aspect.Choices {
				if choice.Value == aspect {
					valid = true
					break
				}
			}
			if !valid {
				return out, fmt.Errorf("that format is not available on this trend")
			}
		}
		out.Aspect = aspect
	}

	// --- Custom fields ---
	for _, field := range trend.Inputs.Fields {
		value, present := in.Fields[field.Key]
		normalised, err := validateField(field, value, present)
		if err != nil {
			return out, err
		}
		if normalised != nil {
			out.Fields[field.Key] = normalised
			if field.Type == models.FieldTypeImage {
				out.UploadCount++
			}
		}
	}

	return out, nil
}

// validateField checks one custom field and returns its normalised value, or
// nil when the field was left empty and is allowed to be.
func validateField(field models.CustomField, value interface{}, present bool) (interface{}, error) {
	missing := func() (interface{}, error) {
		if field.Required {
			return nil, fmt.Errorf("%s is required", fieldName(field))
		}
		return nil, nil
	}

	if !present || value == nil {
		if field.Default != nil {
			value = field.Default
		} else {
			return missing()
		}
	}

	switch field.Type {
	case models.FieldTypeText, models.FieldTypeTextarea, models.FieldTypeImage:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be text", fieldName(field))
		}
		limit := charCap(field.MaxChars, 500)
		if field.Type == models.FieldTypeImage {
			// An image field's value is an upload id, not prose — no
			// sanitising, just a length bound so it cannot be a payload.
			text = strings.TrimSpace(text)
			if len(text) > 64 {
				return nil, fmt.Errorf("%s is not a valid image", fieldName(field))
			}
		} else {
			text = utils.SanitizeTrendFreeText(text, limit)
		}
		if text == "" {
			return missing()
		}
		return text, nil

	case models.FieldTypeSingle:
		selected, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be one of the options", fieldName(field))
		}
		if strings.TrimSpace(selected) == "" {
			return missing()
		}
		for _, choice := range field.Choices {
			if choice.Value == selected {
				return selected, nil
			}
		}
		return nil, fmt.Errorf("that option is no longer available for %s", fieldName(field))

	case models.FieldTypeMulti:
		raw := toStringList(value)
		if len(raw) == 0 {
			return missing()
		}
		allowed := map[string]bool{}
		for _, choice := range field.Choices {
			allowed[choice.Value] = true
		}
		for _, selected := range raw {
			if !allowed[selected] {
				return nil, fmt.Errorf("that option is no longer available for %s", fieldName(field))
			}
		}
		return raw, nil

	case models.FieldTypeBoolean:
		on, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("%s must be on or off", fieldName(field))
		}
		if !on {
			// A false boolean contributes nothing to the prompt; storing it
			// would only clutter the run record.
			if field.Required {
				return nil, fmt.Errorf("%s is required", fieldName(field))
			}
			return nil, nil
		}
		return true, nil

	case models.FieldTypeNumber:
		number, ok := toFloat(value)
		if !ok {
			return nil, fmt.Errorf("%s must be a number", fieldName(field))
		}
		if field.Min != 0 || field.Max != 0 {
			if field.Max > field.Min && (number < field.Min || number > field.Max) {
				return nil, fmt.Errorf("%s must be between %g and %g", fieldName(field), field.Min, field.Max)
			}
		}
		return number, nil

	default:
		// An unknown type is an admin authoring a field this backend does not
		// understand yet. Dropping it silently is better than failing a paid
		// generation over a field the app probably did not render either.
		return nil, nil
	}
}

// validateTextInput checks a pose or instruction box.
func validateTextInput(spec models.TextInput, raw, label string) (string, error) {
	if !spec.Enabled {
		return "", nil
	}
	text := utils.SanitizeTrendFreeText(raw, charCap(spec.MaxChars, 500))
	if text == "" && spec.Required {
		return "", fmt.Errorf("%s is required", fallbackLabel(spec.Label, label))
	}
	return text, nil
}

// resolveTrendQuality picks the tier to generate at.
//
// Resolution is always downward: an unrecognised or disallowed quality falls
// back to the trend's default rather than to the most expensive tier the
// trend happens to offer. Same rule as config.Stars.NormaliseQuality, for the
// same reason — a bad request must never buy the expensive model.
func resolveTrendQuality(trend models.Trend, requested string) string {
	spec := trend.Inputs.Quality
	fallbackQuality := spec.Default
	if fallbackQuality == "" {
		fallbackQuality = trend.Generation.QualityTier
	}

	if !spec.Enabled || len(spec.Allowed) == 0 {
		if fallbackQuality != "" {
			return fallbackQuality
		}
		return trend.Generation.QualityTier
	}

	requested = strings.TrimSpace(requested)
	for _, allowed := range spec.Allowed {
		if allowed == requested {
			return requested
		}
	}
	if fallbackQuality != "" {
		for _, allowed := range spec.Allowed {
			if allowed == fallbackQuality {
				return fallbackQuality
			}
		}
	}
	return spec.Allowed[0]
}

func fieldName(field models.CustomField) string {
	if field.Label != "" {
		return field.Label
	}
	return field.Key
}

func fallbackLabel(label, def string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	return def
}

func charCap(configured, def int) int {
	if configured > 0 {
		return configured
	}
	return def
}

func pluralPerson(n int) string {
	if n == 1 {
		return "person"
	}
	return "people"
}

// sourceSet builds the allowed-source lookup, defaulting when an admin left
// the list empty.
func sourceSet(sources []string, def string) map[string]bool {
	if len(sources) == 0 {
		return map[string]bool{def: true}
	}
	return stringSet(sources)
}

func stringSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	return out
}

func toStringList(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func toFloat(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
