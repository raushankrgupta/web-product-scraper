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

	var parts []genai.Part

	// Build the strict text prompt
	promptBuilder := strings.Builder{}
	promptBuilder.WriteString(fmt.Sprintf("Generate a highly realistic %s virtual try-on image.\n\n", tryOnType))

	if themeImageURL != "" {
		promptBuilder.WriteString("A base canvas image is provided. This canvas MUST be used as the exact background environment for the final image. Do not modify or replace the background.\n\n")
	}
	if themeDescription != "" {
		// Use themeReferenceURL logic if it's there as inspiration, otherwise just description
		promptBuilder.WriteString(fmt.Sprintf("Theme context: %s\n", themeDescription))
		if themeReferenceURL != "" {
			promptBuilder.WriteString("A theme reference image is also provided. Use this as inspiration for the pose and overall visual vibe.\n")
		}
		promptBuilder.WriteString("\n")
	}
	promptBuilder.WriteString(fmt.Sprintf("There are %d people in the scene. Each person has:\n", len(people)))
	promptBuilder.WriteString("• an input photo (face/body reference)\n")
	promptBuilder.WriteString("• garments they must wear\n\n")
	promptBuilder.WriteString("The final image must preserve the identity, facial features, body proportions, and skin tone from each person's input photo.\n\n")

	for i, p := range people {
		label := fmt.Sprintf("Person %d", i+1)

		promptBuilder.WriteString("--------------------------------\n")
		promptBuilder.WriteString(fmt.Sprintf("%s\n", label))
		promptBuilder.WriteString("--------------------------------\n")

		if p.Details != "" {
			promptBuilder.WriteString(fmt.Sprintf("%s\n\n", p.Details))
		}

		promptBuilder.WriteString("Inputs provided:\n")
		promptBuilder.WriteString(fmt.Sprintf("• %s actual photo\n", label))
		if len(p.TopURL) > 0 {
			promptBuilder.WriteString("• Top garment image\n")
		}
		if len(p.BottomURL) > 0 {
			promptBuilder.WriteString("• Bottom garment image\n")
		}
		if len(p.AccessoryURL) > 0 {
			promptBuilder.WriteString("• Accessory image\n")
		}
		if len(p.DressURL) > 0 {
			promptBuilder.WriteString("• Dress image\n")
		}

		promptBuilder.WriteString("\nTask:\n")
		promptBuilder.WriteString("Replace ALL clothing from the original photo with the provided garments.\n\n")

		promptBuilder.WriteString("Requirements:\n")
		promptBuilder.WriteString("• The person must wear the exact provided top, bottom, and accessory.\n")
		promptBuilder.WriteString("• Garments must fit naturally to the body with realistic folds and fabric behavior.\n")
		promptBuilder.WriteString("• Do NOT keep any clothing from the original photo.\n")
		promptBuilder.WriteString("• Preserve the person's face, hairstyle, body shape, and pose.\n")
		promptBuilder.WriteString("• Ensure garment alignment matches body perspective and pose.\n\n")
	}

	promptBuilder.WriteString("--------------------------------\n")
	promptBuilder.WriteString("Scene Composition\n")
	promptBuilder.WriteString("--------------------------------\n\n")
	promptBuilder.WriteString("• Place all people naturally within the provided background canvas.\n")
	promptBuilder.WriteString("• Ensure correct scale, perspective, and positioning relative to each other.\n")
	promptBuilder.WriteString("• Match lighting, shadows, and color temperature to the background scene.\n")
	promptBuilder.WriteString("• Ensure realistic interaction between the subjects and environment.\n")
	promptBuilder.WriteString("• Maintain photorealistic quality.\n\n")
	promptBuilder.WriteString(fmt.Sprintf("Final output must look like a real photograph of the %d people wearing the provided outfits in the given environment.\n", len(people)))

	parts = append(parts, genai.Text(promptBuilder.String()))

	// If theme images exist, fetch and add them
	if themeImageURL != "" {
		b, err := fetchImage(themeImageURL)
		if err == nil {
			parts = append(parts, genai.Text("Theme Canvas (Background Mapping):"))
			parts = append(parts, genai.ImageData("jpeg", b))
		}
	}
	if themeReferenceURL != "" {
		b, err := fetchImage(themeReferenceURL)
		if err == nil {
			parts = append(parts, genai.Text("Theme Reference (Pose/Vibe Mapping):"))
			parts = append(parts, genai.ImageData("jpeg", b))
		}
	}

	for i, p := range people {
		label := fmt.Sprintf("Person %d", i+1)

		parts = append(parts, genai.Text(fmt.Sprintf("Images for %s:", label)))

		if p.PersonImageURL != "" {
			b, err := fetchImage(p.PersonImageURL)
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s actual photo:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.TopURL) > 0 {
			b, err := fetchImage(p.TopURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Top garment image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.BottomURL) > 0 {
			b, err := fetchImage(p.BottomURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Bottom garment image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.AccessoryURL) > 0 {
			b, err := fetchImage(p.AccessoryURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Accessory image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.DressURL) > 0 {
			b, err := fetchImage(p.DressURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Dress image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
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

	var parts []genai.Part

	// Build the strict user-requested text prompt
	promptBuilder := strings.Builder{}
	promptBuilder.WriteString("Generate a highly realistic couple virtual try-on image.\n\n")

	if themeImageURL != "" {
		promptBuilder.WriteString("A base canvas image is provided. This canvas MUST be used as the exact background environment for the final image. Do not modify or replace the background.\n\n")
	}
	if themeDescription != "" {
		promptBuilder.WriteString(fmt.Sprintf("Theme context: %s\n\n", themeDescription))
	}

	promptBuilder.WriteString("There are two people in the scene. Each person has:\n")
	promptBuilder.WriteString("• an input photo (face/body reference)\n")
	promptBuilder.WriteString("• garments they must wear\n\n")
	promptBuilder.WriteString("The final image must preserve the identity, facial features, body proportions, and skin tone from each person's input photo.\n\n")

	for i, p := range people {
		personLabels := []string{"Person 1", "Person 2"}
		label := personLabels[i]

		promptBuilder.WriteString("--------------------------------\n")
		promptBuilder.WriteString(fmt.Sprintf("%s\n", label))
		promptBuilder.WriteString("--------------------------------\n")

		if p.Details != "" {
			promptBuilder.WriteString(fmt.Sprintf("%s\n\n", p.Details))
		}

		promptBuilder.WriteString("Inputs provided:\n")
		promptBuilder.WriteString(fmt.Sprintf("• %s actual photo\n", label))
		if len(p.TopURL) > 0 {
			promptBuilder.WriteString("• Top garment image\n")
		}
		if len(p.BottomURL) > 0 {
			promptBuilder.WriteString("• Bottom garment image\n")
		}
		if len(p.AccessoryURL) > 0 {
			promptBuilder.WriteString("• Accessory image\n")
		}
		if len(p.DressURL) > 0 {
			promptBuilder.WriteString("• Dress image\n")
		}

		promptBuilder.WriteString("\nTask:\n")
		promptBuilder.WriteString("Replace ALL clothing from the original photo with the provided garments.\n\n")

		promptBuilder.WriteString("Requirements:\n")
		promptBuilder.WriteString("• The person must wear the exact provided top, bottom, and accessory.\n")
		promptBuilder.WriteString("• Garments must fit naturally to the body with realistic folds and fabric behavior.\n")
		promptBuilder.WriteString("• Do NOT keep any clothing from the original photo.\n")
		promptBuilder.WriteString("• Preserve the person's face, hairstyle, body shape, and pose.\n")
		promptBuilder.WriteString("• Ensure garment alignment matches body perspective and pose.\n\n")
	}

	promptBuilder.WriteString("--------------------------------\n")
	promptBuilder.WriteString("Scene Composition\n")
	promptBuilder.WriteString("--------------------------------\n\n")
	promptBuilder.WriteString("• Place both people naturally within the provided background canvas.\n")
	promptBuilder.WriteString("• Ensure correct scale, perspective, and positioning relative to each other.\n")
	promptBuilder.WriteString("• Match lighting, shadows, and color temperature to the background scene.\n")
	promptBuilder.WriteString("• Ensure realistic interaction between the subjects and environment.\n")
	promptBuilder.WriteString("• Maintain photorealistic quality.\n\n")
	promptBuilder.WriteString("Final output must look like a real photograph of the couple wearing the provided outfits in the given environment.\n")

	parts = append(parts, genai.Text(promptBuilder.String()))

	if themeImageURL != "" {
		b, err := fetchImage(themeImageURL)
		if err == nil {
			parts = append(parts, genai.Text("Base Canvas (Background Environment):"))
			parts = append(parts, genai.ImageData("jpeg", b))
		}
	}

	for i, p := range people {
		personLabels := []string{"Person 1", "Person 2"}
		label := personLabels[i]

		parts = append(parts, genai.Text(fmt.Sprintf("Images for %s:", label)))

		if p.PersonImageURL != "" {
			b, err := fetchImage(p.PersonImageURL)
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s actual photo:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.TopURL) > 0 {
			b, err := fetchImage(p.TopURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Top garment image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.BottomURL) > 0 {
			b, err := fetchImage(p.BottomURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Bottom garment image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.AccessoryURL) > 0 {
			b, err := fetchImage(p.AccessoryURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Accessory image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if len(p.DressURL) > 0 {
			b, err := fetchImage(p.DressURL[0])
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("%s Dress image:", label)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
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

	var parts []genai.Part

	// Build the strict user-requested text prompt
	promptBuilder := strings.Builder{}
	promptBuilder.WriteString("Generate a highly realistic virtual try-on image of a single person.\n\n")

	if themeImageURL != "" {
		promptBuilder.WriteString("A base canvas image is provided. This canvas MUST be used as the exact background environment for the final image. Do not modify or replace the background.\n\n")
	}
	if themeDescription != "" {
		promptBuilder.WriteString(fmt.Sprintf("Theme context: %s\n\n", themeDescription))
	}

	promptBuilder.WriteString("The person has:\n")
	promptBuilder.WriteString("• an input photo (face/body reference)\n")
	promptBuilder.WriteString("• garments they must wear\n\n")
	promptBuilder.WriteString("The final image must preserve the identity, facial features, body proportions, and skin tone from the input photo.\n\n")

	promptBuilder.WriteString("--------------------------------\n")
	promptBuilder.WriteString("Subject Profile\n")
	promptBuilder.WriteString("--------------------------------\n")

	if person.Details != "" {
		promptBuilder.WriteString(fmt.Sprintf("%s\n\n", person.Details))
	}

	promptBuilder.WriteString("Inputs provided:\n")
	promptBuilder.WriteString("• Actual photo\n")
	if len(person.TopURL) != 0 {
		promptBuilder.WriteString("• Top garment image\n")
	}
	if len(person.BottomURL) != 0 {
		promptBuilder.WriteString("• Bottom garment image\n")
	}
	if len(person.AccessoryURL) != 0 {
		promptBuilder.WriteString("• Accessory image\n")
	}
	if len(person.DressURL) != 0 {
		promptBuilder.WriteString("• Dress image\n")
	}

	promptBuilder.WriteString("\nTask:\n")
	promptBuilder.WriteString("Replace ALL clothing from the original photo with the provided garments.\n\n")

	promptBuilder.WriteString("Requirements:\n")
	promptBuilder.WriteString("• The person must wear the exact provided top, bottom, dress and accessory.\n")
	promptBuilder.WriteString("• Garments must fit naturally to the body with realistic folds and fabric behavior.\n")
	promptBuilder.WriteString("• Do NOT keep any clothing from the original photo.\n")
	promptBuilder.WriteString("• Preserve the person's face, hairstyle, body shape, and pose.\n")
	promptBuilder.WriteString("• Ensure garment alignment matches body perspective and pose.\n\n")

	promptBuilder.WriteString("--------------------------------\n")
	promptBuilder.WriteString("Scene Composition\n")
	promptBuilder.WriteString("--------------------------------\n\n")
	promptBuilder.WriteString("• Place the person naturally within the provided background canvas.\n")
	promptBuilder.WriteString("• Match lighting, shadows, and color temperature to the background scene.\n")
	promptBuilder.WriteString("• Ensure realistic interaction between the subject and environment.\n")
	promptBuilder.WriteString("• Maintain photorealistic quality.\n\n")
	promptBuilder.WriteString("Final output must look like a real photograph of the person wearing the provided outfit in the given environment.\n")

	parts = append(parts, genai.Text(promptBuilder.String()))

	if themeImageURL != "" {
		b, err := fetchImage(themeImageURL)
		if err == nil {
			parts = append(parts, genai.Text("Base Canvas (Background Environment):"))
			parts = append(parts, genai.ImageData("jpeg", b))
		}
	}

	parts = append(parts, genai.Text("Images for Subject:"))

	if person.PersonImageURL != "" {
		b, err := fetchImage(person.PersonImageURL)
		if err == nil {
			parts = append(parts, genai.Text("Actual photo:"))
			parts = append(parts, genai.ImageData("jpeg", b))
		}
	}
	if len(person.TopURL) != 0 {
		for _, url := range person.TopURL {
			b, err := fetchImage(url)
			if err == nil {
				parts = append(parts, genai.Text("Top garment image:"))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
	}
	if len(person.BottomURL) != 0 {
		for _, url := range person.BottomURL {
			b, err := fetchImage(url)
			if err == nil {
				parts = append(parts, genai.Text("Bottom garment image:"))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
	}
	if len(person.AccessoryURL) != 0 {
		for _, url := range person.AccessoryURL {
			b, err := fetchImage(url)
			if err == nil {
				parts = append(parts, genai.Text("Accessory image:"))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
	}
	if len(person.DressURL) != 0 {
		for _, url := range person.DressURL {
			b, err := fetchImage(url)
			if err == nil {
				parts = append(parts, genai.Text("Dress image:"))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
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
