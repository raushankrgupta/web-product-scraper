package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// Turning a validated request into something the generator can run.
//
// Everything here is scoped to the caller. Person ids, wardrobe ids and
// upload ids all travel to the client, so an id alone must never be enough to
// generate against someone else's photograph — the same rule the try-on
// handler enforces, and the same reason: ids are not secrets.

// resolvedTrend is a generation ready to run.
type resolvedTrend struct {
	Spec   utils.TrendGenSpec
	Run    models.TrendRunInputs
	Keys   []string
	Prompt string
}

// resolveTrendInputs loads every referenced image, checks ownership, and
// renders the prompt.
//
// Runs after the star hold is taken, which means every error path here must
// return an error rather than write a response — the middleware's refund
// depends on seeing a non-2xx status, and a handler that wrote a 200 and then
// failed would charge for nothing.
func resolveTrendInputs(ctx context.Context, userID string, trend models.Trend,
	req validatedTrendRequest) (*resolvedTrend, error) {

	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	out := &resolvedTrend{}
	run := models.TrendRunInputs{
		Pose:        req.Pose,
		Instruction: req.Instruction,
		Aspect:      req.Aspect,
		Outputs:     req.Outputs,
		Fields:      req.Fields,
	}

	var images []utils.TrendImageRef
	var personDetails []string

	// --- Subjects ---
	for i, ref := range req.People {
		source := ref.Source
		if source == "" {
			source = models.PhotoSourceProfile
		}

		var key, details string
		switch source {
		case models.PhotoSourceProfile:
			person, err := findOwnedPerson(lookupCtx, ref.PersonID, userObjID)
			if err != nil {
				return nil, err
			}
			if len(person.ImagePaths) == 0 {
				return nil, fmt.Errorf("%s has no photo yet — add one and try again", personLabel(person))
			}
			key = person.ImagePaths[0]
			details = personDescription(person)
		case models.PhotoSourceUpload:
			upload, err := findOwnedUpload(lookupCtx, ref.UploadID, userID)
			if err != nil {
				return nil, err
			}
			key = upload.ObjectKey
		default:
			return nil, fmt.Errorf("unsupported photo source")
		}

		url, err := utils.GetPresignedURL(ctx, key)
		if err != nil || url == "" {
			return nil, fmt.Errorf("we couldn't load one of the selected photos")
		}

		images = append(images, utils.TrendImageRef{
			URL:       url,
			Label:     subjectLabel(trend, i, len(req.People), details),
			Essential: true,
		})
		out.Keys = append(out.Keys, key)
		if details != "" {
			personDetails = append(personDetails, details)
		}

		run.People = append(run.People, models.TrendRunPerson{
			Source:    source,
			PersonID:  ref.PersonID,
			UploadID:  ref.UploadID,
			ObjectKey: key,
			Details:   details,
		})
	}

	// --- Wardrobe ---
	var outfitText []string
	for _, garment := range req.Wardrobe {
		entry := models.TrendRunGarment{Slot: garment.Slot}

		switch {
		case strings.TrimSpace(garment.ItemID) != "":
			item, err := findOwnedWardrobeItem(lookupCtx, garment.ItemID, userID)
			if err != nil {
				return nil, err
			}
			entry.ItemID = garment.ItemID
			entry.ObjectKeys = item.Images
			for _, key := range item.Images {
				url, err := utils.GetPresignedURL(ctx, key)
				if err != nil || url == "" {
					continue
				}
				images = append(images, utils.TrendImageRef{
					URL:         url,
					Label:       garmentLabel(garment.Slot),
					DropOnRetry: true,
				})
				out.Keys = append(out.Keys, key)
			}

		case strings.TrimSpace(garment.UploadID) != "":
			upload, err := findOwnedUpload(lookupCtx, garment.UploadID, userID)
			if err != nil {
				return nil, err
			}
			entry.UploadID = garment.UploadID
			entry.ObjectKeys = []string{upload.ObjectKey}
			if url, err := utils.GetPresignedURL(ctx, upload.ObjectKey); err == nil && url != "" {
				images = append(images, utils.TrendImageRef{
					URL:         url,
					Label:       garmentLabel(garment.Slot),
					DropOnRetry: true,
				})
				out.Keys = append(out.Keys, upload.ObjectKey)
			}

		case strings.TrimSpace(garment.Text) != "":
			limit := charCap(trend.Inputs.Wardrobe.TextMaxChars, 300)
			entry.Text = utils.SanitizeTrendFreeText(garment.Text, limit)
			if entry.Text != "" {
				outfitText = append(outfitText, entry.Text)
			}
		}

		run.Wardrobe = append(run.Wardrobe, entry)
	}

	// --- Background ---
	background := models.TrendRunBackground{Mode: req.Background.Mode}
	var backgroundText string
	switch req.Background.Mode {
	case models.BackgroundModePreset:
		background.PresetID = req.Background.PresetID
		for _, preset := range trend.Inputs.Background.Presets {
			if preset.ID != req.Background.PresetID || preset.ImageKey == "" {
				continue
			}
			background.ObjectKey = preset.ImageKey
			if url := presignAnyKey(ctx, preset.ImageKey); url != "" {
				images = append(images, utils.TrendImageRef{
					URL:         url,
					Label:       "Background reference — use this as the setting, not as a person:",
					DropOnRetry: true,
				})
				out.Keys = append(out.Keys, preset.ImageKey)
			}
		}
	case models.BackgroundModeUpload:
		upload, err := findOwnedUpload(lookupCtx, req.Background.UploadID, userID)
		if err != nil {
			return nil, err
		}
		background.UploadID = req.Background.UploadID
		background.ObjectKey = upload.ObjectKey
		if url, err := utils.GetPresignedURL(ctx, upload.ObjectKey); err == nil && url != "" {
			images = append(images, utils.TrendImageRef{
				URL:         url,
				Label:       "Background reference — use this as the setting, not as a person:",
				DropOnRetry: true,
			})
			out.Keys = append(out.Keys, upload.ObjectKey)
		}
	case models.BackgroundModeText:
		background.Text = req.Background.Text
		backgroundText = req.Background.Text
	}
	run.Background = background

	// --- Image-typed custom fields ---
	for _, field := range trend.Inputs.Fields {
		if field.Type != models.FieldTypeImage {
			continue
		}
		uploadID, _ := req.Fields[field.Key].(string)
		if strings.TrimSpace(uploadID) == "" {
			continue
		}
		upload, err := findOwnedUpload(lookupCtx, uploadID, userID)
		if err != nil {
			return nil, err
		}
		if url, err := utils.GetPresignedURL(ctx, upload.ObjectKey); err == nil && url != "" {
			images = append(images, utils.TrendImageRef{
				URL:         url,
				Label:       fieldName(field) + ":",
				DropOnRetry: true,
			})
			out.Keys = append(out.Keys, upload.ObjectKey)
		}
	}

	// --- Admin style references ---
	//
	// Last in the parts list and first to be dropped on a retry: they shape
	// the look, but the subject photo is the thing the result is of.
	for _, key := range trend.Generation.ReferenceKeys {
		if url := presignAnyKey(ctx, key); url != "" {
			images = append(images, utils.TrendImageRef{
				URL:         url,
				Label:       "Style reference — match this look, do not copy any person from it:",
				DropOnRetry: true,
			})
		}
	}

	// A generation with nothing to work from would burn a model call to
	// produce something unrelated to anything the user chose.
	if len(images) == 0 {
		return nil, fmt.Errorf("add at least one photo to generate this look")
	}
	if limit := trend.Generation.MaxImages; limit > 0 && len(images) > limit {
		images = images[:limit]
	}

	prompt, _ := utils.RenderTrendPrompt(trend.Generation, utils.TrendPromptValues{
		PeopleCount:     len(req.People),
		PersonDetails:   personDetails,
		OutfitText:      strings.Join(outfitText, "; "),
		BackgroundText:  backgroundText,
		Pose:            req.Pose,
		Instruction:     req.Instruction,
		Fields:          req.Fields,
		ChoiceFragments: utils.TrendChoiceFragments(trend, run),
	})

	out.Run = run
	out.Prompt = prompt
	out.Spec = utils.TrendGenSpec{
		Label:         "trend:" + trend.Slug,
		Prompt:        prompt,
		TersePrompt:   trend.Generation.TersePrompt,
		Images:        images,
		Provider:      trend.Generation.Provider,
		Quality:       req.Quality,
		ModelOverride: trend.Generation.ModelOverride,
		Outputs:       req.Outputs,
	}
	return out, nil
}

// findOwnedPerson loads one of the caller's profiles.
func findOwnedPerson(ctx context.Context, id string, userID primitive.ObjectID) (models.Person, error) {
	objID, err := primitive.ObjectIDFromHex(strings.TrimSpace(id))
	if err != nil {
		return models.Person{}, fmt.Errorf("that profile is not available")
	}
	var person models.Person
	err = utils.GetCollection(config.DBName, "person").FindOne(ctx, bson.M{
		"_id":        objID,
		"user_id":    userID,
		"is_deleted": bson.M{"$ne": true},
	}).Decode(&person)
	if err != nil {
		// Deleted on another device and refused for not being yours look the
		// same from here, and should: distinguishing them would confirm that
		// a guessed id exists.
		return models.Person{}, fmt.Errorf("that profile is not available")
	}
	return person, nil
}

// findOwnedWardrobeItem loads one of the caller's wardrobe items. Note the
// user id is stored as a string on this collection, not an ObjectID.
func findOwnedWardrobeItem(ctx context.Context, id, userID string) (models.WardrobeItem, error) {
	objID, err := primitive.ObjectIDFromHex(strings.TrimSpace(id))
	if err != nil {
		return models.WardrobeItem{}, fmt.Errorf("that wardrobe item is not available")
	}
	var item models.WardrobeItem
	err = utils.GetCollection(config.DBName, "wardrobe").
		FindOne(ctx, bson.M{"_id": objID, "user_id": userID}).Decode(&item)
	if err != nil {
		return models.WardrobeItem{}, fmt.Errorf("that wardrobe item is not available")
	}
	return item, nil
}

// findOwnedUpload loads one of the caller's staged photos.
func findOwnedUpload(ctx context.Context, id, userID string) (models.TrendUpload, error) {
	objID, err := primitive.ObjectIDFromHex(strings.TrimSpace(id))
	if err != nil {
		return models.TrendUpload{}, fmt.Errorf("that photo is no longer available — upload it again")
	}
	var upload models.TrendUpload
	err = utils.GetCollection(config.DBName, models.CollTrendUploads).
		FindOne(ctx, bson.M{"_id": objID, "user_id": userID}).Decode(&upload)
	if err != nil {
		// Also the path an expired upload takes, which is the likely one: a
		// user who left the screen open overnight.
		return models.TrendUpload{}, fmt.Errorf("that photo is no longer available — upload it again")
	}
	return upload, nil
}

// keepTrendUploads clears the expiry on the uploads a successful generation
// used, promoting them from staged to evidence.
//
// Best-effort: a failure here means the TTL eventually reclaims an image a
// run record points at, which degrades an admin's view of that run. It must
// never fail the generation the user has already paid for and received.
func keepTrendUploads(ctx context.Context, userID string, in models.TrendRunInputs) {
	ids := make([]primitive.ObjectID, 0, 4)
	add := func(raw string) {
		if objID, err := primitive.ObjectIDFromHex(strings.TrimSpace(raw)); err == nil {
			ids = append(ids, objID)
		}
	}
	for _, p := range in.People {
		add(p.UploadID)
	}
	for _, g := range in.Wardrobe {
		add(g.UploadID)
	}
	add(in.Background.UploadID)

	if len(ids) == 0 {
		return
	}
	_, err := utils.GetCollection(config.DBName, models.CollTrendUploads).UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": ids}, "user_id": userID},
		bson.M{"$unset": bson.M{"expires_at": ""}})
	if err != nil {
		utils.L(ctx).Warn("could not persist trend uploads", "error", err.Error())
	}
}

// personDescription renders the profile details a prompt can use. Same shape
// the try-on handler builds, so a trend and a try-on describe a person to the
// model identically.
func personDescription(person models.Person) string {
	var parts []string
	if person.Gender != "" {
		parts = append(parts, fmt.Sprintf("Gender: %s", person.Gender))
	}
	if person.Age > 0 {
		parts = append(parts, fmt.Sprintf("Age: %d", person.Age))
	}
	if person.Height > 0 {
		parts = append(parts, fmt.Sprintf("Height: %.0f cm", person.Height))
	}
	return strings.Join(parts, ", ")
}

func personLabel(person models.Person) string {
	if strings.TrimSpace(person.Name) != "" {
		return person.Name
	}
	return "That profile"
}

// subjectLabel introduces a subject photo in the parts list.
//
// With one subject the label is unnecessary noise — the prompt already says
// "the reference photograph". With several, it is the only thing telling the
// model which face belongs to which role, and a trend that named its roles
// ("Lead star", "Co-star") gets to use those names.
func subjectLabel(trend models.Trend, index, total int, details string) string {
	if total <= 1 {
		return ""
	}
	name := fmt.Sprintf("Subject %d", index+1)
	if roles := trend.Inputs.People.RoleLabels; index < len(roles) && strings.TrimSpace(roles[index]) != "" {
		name = roles[index]
	}
	if details != "" {
		return fmt.Sprintf("%s (%s):", name, details)
	}
	return name + ":"
}

func garmentLabel(slot string) string {
	if slot == "" {
		return "Outfit reference — use this garment, not the model wearing it:"
	}
	return fmt.Sprintf("Outfit reference (%s) — use this garment, not the model wearing it:", slot)
}

// presignAnyKey signs a stored key, passing absolute URLs through.
func presignAnyKey(ctx context.Context, key string) string {
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "http") {
		return key
	}
	url, err := utils.GetPresignedURL(ctx, key)
	if err != nil {
		return ""
	}
	return url
}
