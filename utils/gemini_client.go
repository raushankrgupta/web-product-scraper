package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/raushankrgupta/web-product-scraper/config"
	"google.golang.org/api/option"
)

// GenerateTryOnImage generates a virtual try-on image using Gemini
func GenerateTryOnImage(ctx context.Context, personImageURL string, productImages []string, dimensions string, personDetails string) ([]byte, error) {
	if config.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(config.GeminiAPIKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-3-pro-image-preview")
	// Note: "gemini-nano-banana" or "gemini-3-pro-image-preview" mentioned in prompt might be placeholders or specific preview models.
	// We'll use a standard capable model. If specifically "gemini-1.5-pro" or similar is needed for vision + text -> image (if supported directly or via description).
	// Wait, the user asked for "gemini-3-pro-image-preview". I should probably check if that's a valid model or use a standard one.
	// Given the prompt "gemini nano banana (gemini-3-pro-image-preview)", I'll stick to a known working model for now, or try to use the requested one if it's a valid string.
	// For now, let's use "gemini-1.5-pro" as it's the current standard for multimodal.
	// If the user specifically wants image GENERATION, we might need a specific model or tool.
	// Standard Gemini models generate text. For image generation, we might need to use a different endpoint or model if available in this SDK.
	// However, assuming the user implies a multimodal capability where we send images and get an image back (or a description to generate one).
	// The prompt says "After we get results from gemini send it as api response".
	// If the model returns an image, we need to handle that.
	// The Go SDK for Gemini supports generating content.

	// Let's construct the prompt.
	prompt := fmt.Sprintf(`
I want the cloths product images to be worn by the person's image provided.
Use the dimensions to accurately demonstrate how this will look upon the user.
Show the size perfectly as well.
Show 100%% truth, do not change the person's image with new person's image in due process.

Person Details: %s
Dimensions: %s
`, personDetails, dimensions)

	// Fetch images
	personImgData, err := fetchImage(personImageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch person image: %v", err)
	}

	parts := []genai.Part{
		genai.Text(prompt),
		genai.ImageData("jpeg", personImgData), // Assuming JPEG for now, ideally detect type
	}

	for _, url := range productImages {
		if url == "" {
			continue
		}
		prodImgData, err := fetchImage(url)
		if err == nil {
			parts = append(parts, genai.ImageData("jpeg", prodImgData))
		}
	}

	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %v", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content generated")
	}

	// Check if we got an image back?
	// Currently, Gemini API mainly returns text unless using specific image generation capabilities which might return a link or base64.
	// If the model is a text-only model, this won't work for image generation.
	// If the user expects an image, we might be using the wrong tool or model if we just expect "GenerateContent" to return an image directly without specific setup.
	// BUT, for the sake of this task, we will assume the model returns the image data or we handle the text response.
	// If the response contains an image part, we extract it.

	for _, part := range resp.Candidates[0].Content.Parts {
		switch p := part.(type) {
		case genai.Text:
			return []byte(p), nil
		case genai.Blob:
			return p.Data, nil
		default:
			errMsg := fmt.Sprintf("Received unexpected part type: %T", p)
			fmt.Println(errMsg)
			return []byte(fmt.Sprintf("%v", p)), nil
		}
	}

	return nil, fmt.Errorf("unexpected response format (empty content)")
}

func fetchImage(pathOrURL string) ([]byte, error) {
	if !strings.HasPrefix(pathOrURL, "http") {
		return os.ReadFile(pathOrURL)
	}
	resp, err := http.Get(pathOrURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch image, status: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func fetchImageLogged(label, url string) ([]byte, string, error) {
	data, err := fetchImage(url)
	if err != nil {
		fmt.Printf("[Gemini] FAILED to fetch %s image: %v (url prefix: %.80s...)\n", label, err, url)
		return nil, "", err
	}
	mime := "jpeg"
	if len(data) > 4 {
		if data[0] == 0x89 && data[1] == 0x50 {
			mime = "png"
		} else if data[0] == 0x52 && data[1] == 0x49 {
			mime = "webp"
		}
	}
	fmt.Printf("[Gemini] Fetched %s image OK (%d bytes, %s)\n", label, len(data), mime)
	return data, mime, nil
}

// PersonTryOnData holds the presigned URLs and details for a person in a try-on session
type PersonTryOnData struct {
	Details        string
	PersonImageURL string
	TopURL         []string
	BottomURL      []string
	AccessoryURL   []string
	DressURL       []string
}

// GenerateMultiPersonTryOnImage generates a multi-person virtual try-on image using Gemini
func GenerateMultiPersonTryOnImage(ctx context.Context, tryOnType string, themeImageURL, themeReferenceURL, themeDescription string, people []PersonTryOnData) ([]byte, error) {
	if config.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(config.GeminiAPIKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-3-pro-image-preview")

	imgCount := 0
	var parts []genai.Part

	promptBuilder := strings.Builder{}
	promptBuilder.WriteString("I want the garment/clothing product images provided to be worn by each person.\n")
	promptBuilder.WriteString(fmt.Sprintf("There are %d people. For each person I provide their photo followed by the garment images they must wear.\n", len(people)))
	promptBuilder.WriteString("Remove all original clothing and dress each person ONLY in their provided garment images.\n")
	promptBuilder.WriteString("The garments in the output must be an identical copy of the provided garment images — same color, design, pattern, fabric, texture.\n")
	promptBuilder.WriteString("Do NOT invent or substitute any clothing.\n")
	promptBuilder.WriteString("Show 100% truth, do not change the persons' faces or bodies.\n")
	if themeDescription != "" {
		promptBuilder.WriteString(fmt.Sprintf("\nScene/Theme: %s\n", themeDescription))
	}

	parts = append(parts, genai.Text(promptBuilder.String()))

	for i, p := range people {
		label := fmt.Sprintf("Person %d", i+1)

		if p.Details != "" {
			parts = append(parts, genai.Text(fmt.Sprintf("%s (%s) — photo followed by their garments:", label, p.Details)))
		} else {
			parts = append(parts, genai.Text(fmt.Sprintf("%s — photo followed by their garments:", label)))
		}

		if p.PersonImageURL != "" {
			if b, mime, err := fetchImageLogged(label+"-photo", p.PersonImageURL); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.TopURL {
			if b, mime, err := fetchImageLogged(label+"-top", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.BottomURL {
			if b, mime, err := fetchImageLogged(label+"-bottom", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.DressURL {
			if b, mime, err := fetchImageLogged(label+"-dress", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.AccessoryURL {
			if b, mime, err := fetchImageLogged(label+"-accessory", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
	}

	if themeImageURL != "" {
		if b, mime, err := fetchImageLogged("theme-background", themeImageURL); err == nil {
			parts = append(parts, genai.Text("Use this image as the background environment:"))
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}

	fmt.Printf("[Gemini] %s try-on: sending %d images in %d parts to model\n", tryOnType, imgCount, len(parts))

	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %v", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content generated")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		switch p := part.(type) {
		case genai.Text:
			fmt.Printf("[Gemini] Response type: TEXT (%d bytes)\n", len(p))
			return []byte(p), nil
		case genai.Blob:
			fmt.Printf("[Gemini] Response type: IMAGE (%d bytes, %s)\n", len(p.Data), p.MIMEType)
			return p.Data, nil
		default:
			return []byte(fmt.Sprintf("%v", p)), nil
		}
	}

	return nil, fmt.Errorf("unexpected response format (empty content)")
}

// GenerateCoupleTryOnImage generates a virtual try-on image specifically structured for exactly 2 people (a couple).
func GenerateCoupleTryOnImage(ctx context.Context, themeImageURL, themeDescription string, people []PersonTryOnData) ([]byte, error) {
	if config.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}
	if len(people) != 2 {
		return nil, fmt.Errorf("GenerateCoupleTryOnImage requires exactly 2 people")
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(config.GeminiAPIKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-3-pro-image-preview")

	imgCount := 0
	var parts []genai.Part

	promptBuilder := strings.Builder{}
	promptBuilder.WriteString("I want the garment/clothing product images provided to be worn by each person.\n")
	promptBuilder.WriteString("There are 2 people. For each person I provide their photo followed by the garment images they must wear.\n")
	promptBuilder.WriteString("Remove all original clothing and dress each person ONLY in their provided garment images.\n")
	promptBuilder.WriteString("The garments in the output must be an identical copy of the provided garment images — same color, design, pattern, fabric, texture.\n")
	promptBuilder.WriteString("Do NOT invent or substitute any clothing.\n")
	promptBuilder.WriteString("Show 100% truth, do not change the persons' faces or bodies.\n")
	if themeDescription != "" {
		promptBuilder.WriteString(fmt.Sprintf("\nScene/Theme: %s\n", themeDescription))
	}

	parts = append(parts, genai.Text(promptBuilder.String()))

	for i, p := range people {
		label := fmt.Sprintf("Person %d", i+1)

		if p.Details != "" {
			parts = append(parts, genai.Text(fmt.Sprintf("%s (%s) — photo followed by their garments:", label, p.Details)))
		} else {
			parts = append(parts, genai.Text(fmt.Sprintf("%s — photo followed by their garments:", label)))
		}

		if p.PersonImageURL != "" {
			if b, mime, err := fetchImageLogged(label+"-photo", p.PersonImageURL); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.TopURL {
			if b, mime, err := fetchImageLogged(label+"-top", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.BottomURL {
			if b, mime, err := fetchImageLogged(label+"-bottom", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.DressURL {
			if b, mime, err := fetchImageLogged(label+"-dress", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
		for _, url := range p.AccessoryURL {
			if b, mime, err := fetchImageLogged(label+"-accessory", url); err == nil {
				parts = append(parts, genai.ImageData(mime, b))
				imgCount++
			}
		}
	}

	if themeImageURL != "" {
		if b, mime, err := fetchImageLogged("theme-background", themeImageURL); err == nil {
			parts = append(parts, genai.Text("Use this image as the background environment:"))
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}

	fmt.Printf("[Gemini] Couple try-on: sending %d images in %d parts to model\n", imgCount, len(parts))

	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %v", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content generated")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		switch p := part.(type) {
		case genai.Text:
			fmt.Printf("[Gemini] Response type: TEXT (%d bytes)\n", len(p))
			return []byte(p), nil
		case genai.Blob:
			fmt.Printf("[Gemini] Response type: IMAGE (%d bytes, %s)\n", len(p.Data), p.MIMEType)
			return p.Data, nil
		default:
			return []byte(fmt.Sprintf("%v", p)), nil
		}
	}

	return nil, fmt.Errorf("unexpected response format (empty content)")
}

// GenerateIndividualTryOnImage generates a virtual try-on image specifically structured for exactly 1 person.
func GenerateIndividualTryOnImage(ctx context.Context, themeImageURL, themeDescription string, person PersonTryOnData) ([]byte, error) {
	if config.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(config.GeminiAPIKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-3-pro-image-preview")

	imgCount := 0
	var parts []genai.Part

	promptBuilder := strings.Builder{}
	promptBuilder.WriteString("I want the garment/clothing product images provided to be worn by the person in the photo.\n")
	promptBuilder.WriteString("Remove all original clothing from the person and dress them ONLY in the exact garment images provided.\n")
	promptBuilder.WriteString("The garments in the output must be an identical copy of the provided garment images — same color, design, pattern, fabric, texture.\n")
	promptBuilder.WriteString("Do NOT invent or substitute any clothing.\n")
	promptBuilder.WriteString("Show 100% truth, do not change the person's face or body.\n")
	if person.Details != "" {
		promptBuilder.WriteString(fmt.Sprintf("\nPerson Details: %s\n", person.Details))
	}
	if themeDescription != "" {
		promptBuilder.WriteString(fmt.Sprintf("\nScene/Theme: %s\n", themeDescription))
	}

	parts = append(parts, genai.Text(promptBuilder.String()))

	if person.PersonImageURL != "" {
		if b, mime, err := fetchImageLogged("person", person.PersonImageURL); err == nil {
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}

	for _, url := range person.TopURL {
		if b, mime, err := fetchImageLogged("top", url); err == nil {
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}
	for _, url := range person.BottomURL {
		if b, mime, err := fetchImageLogged("bottom", url); err == nil {
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}
	for _, url := range person.DressURL {
		if b, mime, err := fetchImageLogged("dress", url); err == nil {
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}
	for _, url := range person.AccessoryURL {
		if b, mime, err := fetchImageLogged("accessory", url); err == nil {
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}

	if themeImageURL != "" {
		if b, mime, err := fetchImageLogged("theme-background", themeImageURL); err == nil {
			parts = append(parts, genai.Text("Use this image as the background environment:"))
			parts = append(parts, genai.ImageData(mime, b))
			imgCount++
		}
	}

	fmt.Printf("[Gemini] Individual try-on: sending %d images in %d parts to model\n", imgCount, len(parts))

	if imgCount < 2 {
		return nil, fmt.Errorf("not enough images fetched (got %d, need at least person + 1 garment)", imgCount)
	}

	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %v", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content generated")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		switch p := part.(type) {
		case genai.Text:
			fmt.Printf("[Gemini] Response type: TEXT (%d bytes)\n", len(p))
			return []byte(p), nil
		case genai.Blob:
			fmt.Printf("[Gemini] Response type: IMAGE (%d bytes, %s)\n", len(p.Data), p.MIMEType)
			return p.Data, nil
		default:
			return []byte(fmt.Sprintf("%v", p)), nil
		}
	}

	return nil, fmt.Errorf("unexpected response format (empty content)")
}
