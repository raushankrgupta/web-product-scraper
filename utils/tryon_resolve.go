package utils

import (
	"context"
	"fmt"
	"log/slog"
)

// This file holds the provider-independent half of a try-on: turning a set of
// presigned URLs into bytes.
//
// It exists because the fallback needs it. Fetching used to live inside the
// Gemini generator, so a second provider would have had to re-download the
// customer photo and every garment reference — several seconds of a request
// budget that is already nearly spent by the time the first provider has
// failed, on images we are holding in memory anyway.

// fetchedImage is one downloaded image and its sniffed MIME type.
type fetchedImage struct {
	mime string
	data []byte
}

// resolvedPerson is one customer with their garment references, in the order
// they should be shown to the model: tops, bottoms, dresses, accessories.
//
// The four slots are flattened deliberately. Nothing downstream treats them
// differently — the prompt says "the garment(s) in the reference images" — and
// the only thing that ever cared about the order is the safety-block retry,
// which keeps the first N. A flat slice makes that "take the first one"
// instead of four nested loops with a shared counter.
type resolvedPerson struct {
	Details  string
	Photo    *fetchedImage
	Garments []fetchedImage
}

// resolvedTryOn is everything a provider needs, already downloaded.
type resolvedTryOn struct {
	Label            string
	People           []resolvedPerson
	Theme            *fetchedImage
	ThemeDescription string
	SpecialRequest   string
}

// GarmentCount is the total across all people.
func (r *resolvedTryOn) GarmentCount() int {
	n := 0
	for _, p := range r.People {
		n += len(p.Garments)
	}
	return n
}

// prompt renders the instruction text for this try-on.
//
// One person gets the individual prompt, several get the multi-person one, and
// both providers get the identical string. The prompts were written around
// image-model safety behaviour rather than around one vendor's API, and a
// single source means Gemini and OpenAI cannot drift into answering the same
// request differently for reasons nobody can see.
func (r *resolvedTryOn) prompt(terse bool) string {
	if len(r.People) == 1 {
		return individualTryOnPrompt(r.People[0].Details, r.ThemeDescription, r.SpecialRequest, terse)
	}
	return multiPersonTryOnPrompt(len(r.People), r.ThemeDescription, r.SpecialRequest, terse)
}

// resolveTryOn downloads every image a generation needs, once.
//
// Individual fetch failures are tolerated — a garment whose retailer CDN has
// expired should not sink a try-on that still has the customer's photo and two
// other references — but the result has to be usable, so a run with no photo
// or no garment at all is rejected here rather than sent upstream to be
// refused at our expense.
func resolveTryOn(ctx context.Context, label string, scene TryOnScene, people []PersonTryOnData) (*resolvedTryOn, error) {
	if len(people) == 0 {
		return nil, fmt.Errorf("no people provided")
	}

	out := &resolvedTryOn{
		Label:            label,
		ThemeDescription: scene.ThemeDescription,
		SpecialRequest:   scene.SpecialRequest,
		People:           make([]resolvedPerson, 0, len(people)),
	}

	fetchAll := func(tag string, urls []string) []fetchedImage {
		imgs := make([]fetchedImage, 0, len(urls))
		for _, u := range urls {
			if b, mime, err := fetchImageLogged(ctx, tag, u); err == nil {
				imgs = append(imgs, fetchedImage{mime: mime, data: b})
			}
		}
		return imgs
	}

	photos := 0
	for i, p := range people {
		tag := fmt.Sprintf("Person %d", i+1)
		rp := resolvedPerson{Details: p.Details}

		if p.PersonImageURL != "" {
			if b, mime, err := fetchImageLogged(ctx, tag+"-photo", p.PersonImageURL); err == nil {
				rp.Photo = &fetchedImage{mime: mime, data: b}
				photos++
			}
		}

		rp.Garments = append(rp.Garments, fetchAll(tag+"-top", p.TopURL)...)
		rp.Garments = append(rp.Garments, fetchAll(tag+"-bottom", p.BottomURL)...)
		rp.Garments = append(rp.Garments, fetchAll(tag+"-dress", p.DressURL)...)
		rp.Garments = append(rp.Garments, fetchAll(tag+"-accessory", p.AccessoryURL)...)

		out.People = append(out.People, rp)
	}

	if scene.ThemeImageURL != "" {
		if b, mime, err := fetchImageLogged(ctx, "theme-background", scene.ThemeImageURL); err == nil {
			out.Theme = &fetchedImage{mime: mime, data: b}
		}
	}

	// "not enough images" is matched by FailureReason as
	// insufficient_input_images, which is on the do-not-fall-back list: a
	// second vendor cannot fetch an image we failed to fetch.
	if photos == 0 || out.GarmentCount() == 0 {
		return nil, fmt.Errorf("not enough images fetched (photos=%d, garments=%d)", photos, out.GarmentCount())
	}

	slog.Info("try-on images resolved",
		"label", label, "people", len(out.People), "photos", photos,
		"garments", out.GarmentCount(), "theme", out.Theme != nil)
	return out, nil
}
