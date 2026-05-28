package api

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/utils"
)

// GuestTryOnHandler runs a one-shot try-on for an anonymous (guest) user
// without persisting a person/product to MongoDB. It's the minimum-friction
// funnel entry point — the user uploads a photo + drops a product link, and
// gets the result image back. The QuotaMiddleware caps this at 1/day per
// device (PlanGuest).
//
// Multipart fields:
//
//	person_image      — required, the user's photo
//	product_url       — optional, will be scraped if present
//	product_image     — optional, used instead of/in addition to product_url
//	person_details    — optional, free-text body description ("F, 170cm, ...")
//
// Either product_url or product_image must be provided.
func GuestTryOnHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() { fmt.Println(logMessageBuilder.String()) }()
	utils.AddToLogMessage(&logMessageBuilder, "[Guest Try-On API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 10MB cap matches /persons; gives enough headroom for a phone photo.
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Error parsing form", http.StatusBadRequest)
		return
	}

	// 1. Person image (required) — upload to S3 under a guest-scoped prefix
	personFileHeader := firstFile(r, "person_image")
	if personFileHeader == nil {
		utils.RespondError(w, &logMessageBuilder, "person_image is required", http.StatusBadRequest)
		return
	}
	personFile, err := personFileHeader.Open()
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to read person_image", http.StatusBadRequest)
		return
	}
	defer personFile.Close()

	personKey := fmt.Sprintf("guest_uploads/person_%d_%s", time.Now().UnixNano(), sanitizeFilename(personFileHeader.Filename))
	if _, err := utils.UploadFileToS3(r.Context(), personFile, personKey, personFileHeader.Header.Get("Content-Type"), utils.CacheControlMutable); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Failed to upload person image: %v", err), http.StatusInternalServerError)
		return
	}
	personImageURL, err := utils.GetPresignedURL(r.Context(), personKey)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to presign person image", http.StatusInternalServerError)
		return
	}

	// 2. Product — either a URL we scrape, or an uploaded product image
	productURL := strings.TrimSpace(r.FormValue("product_url"))
	productFileHeader := firstFile(r, "product_image")
	if productURL == "" && productFileHeader == nil {
		utils.RespondError(w, &logMessageBuilder, "Provide either product_url or product_image", http.StatusBadRequest)
		return
	}

	var productImageURLs []string

	if productFileHeader != nil {
		pf, err := productFileHeader.Open()
		if err != nil {
			utils.RespondError(w, &logMessageBuilder, "Failed to read product_image", http.StatusBadRequest)
			return
		}
		defer pf.Close()
		productKey := fmt.Sprintf("guest_uploads/product_%d_%s", time.Now().UnixNano(), sanitizeFilename(productFileHeader.Filename))
		if _, err := utils.UploadFileToS3(r.Context(), pf, productKey, productFileHeader.Header.Get("Content-Type"), utils.CacheControlImmutable); err != nil {
			utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Failed to upload product image: %v", err), http.StatusInternalServerError)
			return
		}
		signed, _ := utils.GetPresignedURL(r.Context(), productKey)
		if signed != "" {
			productImageURLs = append(productImageURLs, signed)
		}
	}

	if productURL != "" {
		// Best-effort scrape. If it fails we still proceed with any uploaded
		// product_image (don't block the user on a flaky scraper). Routes
		// Myntra URLs to the isolated myntra_scraper package and everything
		// else to the standard scrapers.GetScraper factory.
		scraper, resolvedURL, err := selectScraper(productURL)
		if err != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("scraper_not_found: %v", err))
		} else if product, err := scraper.ScrapeProduct(resolvedURL); err != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("scrape_failed: %v", err))
		} else {
			productImageURLs = append(productImageURLs, product.Images...)
		}
	}

	if len(productImageURLs) == 0 {
		utils.RespondError(w, &logMessageBuilder, "Could not get product images", http.StatusBadRequest)
		return
	}

	// Cap garment images at 2 (main + 1 alt). Amazon/Flipkart/etc. scrapers
	// return every alt-image on the PDP — typically 5-7 shots including
	// model photos of real people wearing the garment. Feeding Gemini a
	// user photo + multiple model photos + "dress this person in these
	// clothes" reliably trips its identity-swap safety filter
	// (BlockReasonOther). Logged-in users avoid this because they curate a
	// single image into their wardrobe before try-on; guests don't have
	// that step, so we have to dedupe defensively here.
	const maxGuestGarmentImages = 2
	if len(productImageURLs) > maxGuestGarmentImages {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("trimmed product images %d -> %d", len(productImageURLs), maxGuestGarmentImages))
		productImageURLs = productImageURLs[:maxGuestGarmentImages]
	}

	// 3. Run Gemini — reuse the multi-person individual generator with a
	//    single person + no theme. Pass person_details through verbatim if
	//    provided; otherwise send the empty string so we don't inject the
	//    literal phrase "Guest user, no body details provided" into the
	//    prompt (which itself is a needless trigger surface for Gemini's
	//    image-gen safety classifier).
	personDetails := strings.TrimSpace(r.FormValue("person_details"))

	geminiCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	generated, err := utils.GenerateIndividualTryOnImage(geminiCtx, "", "", utils.PersonTryOnData{
		Details:        personDetails,
		PersonImageURL: personImageURL,
		TopURL:         productImageURLs,
	})
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Gemini failed: %v", err))
		errLower := strings.ToLower(err.Error())
		switch {
		case strings.Contains(errLower, "429"), strings.Contains(errLower, "quota"):
			utils.RespondError(w, nil, "We're at capacity, please try again in a moment.", http.StatusTooManyRequests)
		case strings.Contains(errLower, "blocked"), strings.Contains(errLower, "blockreason"), strings.Contains(errLower, "safety"):
			// Gemini's safety filter rejected this combination — usually
			// triggered by product PDPs that include model shots, or by
			// person photos the classifier can't cleanly parse. Don't
			// surface BlockReasonOther to the user; nudge them toward a
			// recoverable action instead.
			utils.RespondError(w, nil, "We couldn't generate a try-on for this item. Try a clearer photo of yourself, or pick a different product.", http.StatusUnprocessableEntity)
		default:
			utils.RespondError(w, nil, "Failed to generate try-on: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// 4. Upload result + return presigned URL. Don't write to the tryons
	//    collection — guests don't have a gallery to come back to.
	resultKey := fmt.Sprintf("generated_images/guest_%d.jpg", time.Now().UnixNano())
	if _, err := utils.UploadFileToS3(r.Context(), bytes.NewReader(generated), resultKey, "image/jpeg"); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Failed to upload result: %v", err), http.StatusInternalServerError)
		return
	}
	resultURL, _ := utils.GetPresignedURL(r.Context(), resultKey)

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"result":     resultURL,
		"is_guest":   true,
		"upsell": map[string]string{
			"title":  "Save & get 4 more free try-ons",
			"action": "signup",
		},
	})
}

// firstFile returns the first uploaded file under `key`, or nil if none.
// Wraps the mildly awkward multipart-form API so the handler stays readable.
func firstFile(r *http.Request, key string) *multipart.FileHeader {
	if r.MultipartForm == nil {
		return nil
	}
	fhs, ok := r.MultipartForm.File[key]
	if !ok || len(fhs) == 0 {
		return nil
	}
	return fhs[0]
}

// sanitizeFilename strips path separators so we never write to an unexpected
// S3 key. Strict allow-list approach: keep only alphanumerics, dots, dashes
// and underscores.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9',
			ch == '.', ch == '-', ch == '_':
			b.WriteRune(ch)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "upload"
	}
	return b.String()
}
