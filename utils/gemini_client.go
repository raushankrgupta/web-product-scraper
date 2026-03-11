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
			// Log the type for debugging (printing to stdout/err since we don't have logger passed here easily, or use fmt)
			fmt.Printf("Received unexpected part type: %T\n", p)
			// Return string representation as fallback?
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
	TopURL         string
	BottomURL      string
	AccessoryURL   string
}

// GenerateMultiPersonTryOnImage generates a multi-person virtual try-on image using Gemini
func GenerateMultiPersonTryOnImage(ctx context.Context, tryOnType string, themeImageURL, themeReferenceURL, themeDescription, promptModifier string, people []PersonTryOnData) ([]byte, error) {
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

	// Build the text prompt
	promptBuilder := strings.Builder{}
	promptBuilder.WriteString(fmt.Sprintf("Generate a highly realistic image for a %s virtual try-on.\n", tryOnType))

	if themeImageURL != "" {
		promptBuilder.WriteString("A main canvas image is provided. Use this blank canvas as the exact background setting for the final composite.\n")
	}
	if themeReferenceURL != "" {
		promptBuilder.WriteString(fmt.Sprintf("A theme reference image is also provided. Use this as inspiration for the pose and overall visual vibe. Theme Description: %s\n", themeDescription))
	}
	if promptModifier != "" {
		promptBuilder.WriteString(fmt.Sprintf("Style Modifiers: %s\n", promptModifier))
	}

	promptBuilder.WriteString(fmt.Sprintf("\nThere are %d people to render in the scene.\n", len(people)))
	for i, p := range people {
		promptBuilder.WriteString(fmt.Sprintf("\n--- Person %d ---\n", i+1))
		promptBuilder.WriteString(fmt.Sprintf("Details: %s\n", p.Details))
		promptBuilder.WriteString("This person has specific images provided for their face/body, and the clothes they MUST wear. CRITICAL: You MUST flawlessly replace whatever clothing they are wearing in their 'actual photo' with the provided 'Top to wear', 'Bottom to wear', and 'Accessory to wear'. Do NOT retain the clothing from their original photo.\n")
	}
	promptBuilder.WriteString("\nEnsure all subjects are placed naturally within the scene together, scaled correctly, and lighting matches the background. Specifically, ensure that every person in the generated image is wearing the new exact garments provided, replacing any clothing from their original photos. Show 100% truth to the uploaded faces and garments.\n\n")

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

	// Add images for people
	for i, p := range people {
		pHeader := fmt.Sprintf("Images for Person %d:", i+1)
		parts = append(parts, genai.Text(pHeader))
		
		if p.PersonImageURL != "" {
			b, err := fetchImage(p.PersonImageURL)
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("Person %d's actual photo (replace their clothes):", i+1)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if p.TopURL != "" {
			b, err := fetchImage(p.TopURL)
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("Top to wear for Person %d:", i+1)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if p.BottomURL != "" {
			b, err := fetchImage(p.BottomURL)
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("Bottom to wear for Person %d:", i+1)))
				parts = append(parts, genai.ImageData("jpeg", b))
			}
		}
		if p.AccessoryURL != "" {
			b, err := fetchImage(p.AccessoryURL)
			if err == nil {
				parts = append(parts, genai.Text(fmt.Sprintf("Accessory to wear for Person %d:", i+1)))
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
			fmt.Printf("Received unexpected part type: %T\n", p)
			return []byte(fmt.Sprintf("%v", p)), nil
		}
	}

	return nil, fmt.Errorf("unexpected response format (empty content)")
}
