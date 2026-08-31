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
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// GuestTryOnHandler runs a one-shot try-on for an anonymous (guest) user
// without persisting a person/product to MongoDB. It's the minimum-friction
// funnel entry point — the user uploads a photo + drops a product link, and
// gets the result image back. StarGateMiddleware caps this at 1/day per
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
	defer func() { utils.FlushLog(r.Context(), &logMessageBuilder) }()
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
		utils.RespondInternalError(w, r, &logMessageBuilder, "s3",
			"We couldn't save your photo. Please try again.", err, http.StatusInternalServerError)
		return
	}
	personImageURL, err := utils.GetPresignedURL(r.Context(), personKey)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to presign person image", http.StatusInternalServerError)
		return
	}

	// 2. Product — either a URL we scrape, or an uploaded product image
	productURL := strings.TrimSpace(r.FormValue("product_url"))
	if productURL != "" {
		// Same normalisation as /product/details: share-sheet pastes carry
		// newlines and tracking params.
		normalized, err := utils.NormalizeProductURL(productURL)
		if err != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Rejected malformed product_url %q: %v", productURL, err))
			utils.RespondError(w, nil, "That doesn't look like a valid product link.", http.StatusBadRequest)
			return
		}
		productURL = normalized
	}
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
			utils.RespondInternalError(w, r, &logMessageBuilder, "s3",
				"We couldn't save that product image. Please try again.", err, http.StatusInternalServerError)
			return
		}
		signed, _ := utils.GetPresignedURL(r.Context(), productKey)
		if signed != "" {
			productImageURLs = append(productImageURLs, signed)
		}
	}

	if productURL != "" {
		// Best-effort scrape. If it fails we still proceed with any uploaded
		// product_image (don't block the user on a flaky scraper).
		//
		// Myntra blocks this server's datacenter IP, so when server B is
		// configured we delegate Myntra URLs to it; everything else (and all
		// URLs when B is not configured) goes through the local scraper
		// factory. We pass persist=false so B does an ephemeral scrape (no
		// DB/S3 footprint) — the guest flow only needs the images and never
		// persisted a product before.
		scrapedViaB := false
		if delegateToServerB(productURL) {
			guestUserID, _ := GetUserIDFromContext(r.Context())
			if product, err := scrapeViaServerB(r.Context(), guestUserID, productURL, false); err != nil {
				// Don't give up here: fall through to the local scraper.
				// This is the exact path that produced "Could not get
				// product images" in production when B's tunnel hostname
				// stopped resolving.
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("server B scrape failed, falling back to local: %v", err))
				markServerBUnhealthy(err)
				alert.Errorf("serverb", "guest scrape delegation failed", err)
			} else {
				productImageURLs = append(productImageURLs, product.Images...)
				scrapedViaB = true
			}
		}

		if !scrapedViaB {
			switch scraper, resolvedURL, err := selectScraper(productURL); {
			case err != nil:
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("scraper_not_found: %v", err))
				alert.Warnf("scraper", "no scraper found for domain", err, "domain", hostOf(productURL), "flow", "guest")
			default:
				if product, err := scraper.ScrapeProduct(resolvedURL); err != nil {
					utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("scrape_failed: %v", err))
					alert.Errorf("scraper", "scrape failed after routing", err, "domain", hostOf(resolvedURL), "flow", "guest")
				} else {
					productImageURLs = append(productImageURLs, product.Images...)
				}
			}
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

	// Guests are pinned to the free quality tier by StarGateMiddleware; read
	// it back rather than assuming, so a config change moves this too.
	quality := GetQualityFromContext(r.Context())

	geminiCtx, cancel := context.WithTimeout(context.Background(), geminiTimeout(quality))
	defer cancel()

	genStart := time.Now()
	generated, err := utils.GenerateIndividualTryOnImage(geminiCtx, "", "", utils.PersonTryOnData{
		Details:        personDetails,
		PersonImageURL: personImageURL,
		TopURL:         productImageURLs,
	}, quality)
	if err != nil {
		// Shared classifier so guest and signed-in try-on report the same
		// status codes for the same upstream failure. The safety-block copy
		// is tuned for a first-time user with no wardrobe to fall back on.
		respondGenError(w, r, &logMessageBuilder, "/try-on/guest", "guest", err, genStart, map[int]string{
			http.StatusUnprocessableEntity: "We couldn't generate a try-on for this item. Try a clearer photo of yourself, or pick a different product.",
		})
		return
	}
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf(
		"Guest try-on generated OK: bytes=%d duration=%s", len(generated), time.Since(genStart).Round(time.Millisecond)))

	// 4. Upload result + return presigned URL. Don't write to the tryons
	//    collection — guests don't have a gallery to come back to.
	resultKey := fmt.Sprintf("generated_images/guest_%d.jpg", time.Now().UnixNano())

	// persistCtx, not r.Context(): the generation is already paid for by the
	// time we get here, so a caller who hung up during those 20-odd seconds
	// must not be able to cancel the upload out from under it. See persistCtx.
	uploadCtx, cancelUpload := persistCtx()
	defer cancelUpload()

	if _, err := utils.UploadFileToS3(uploadCtx, bytes.NewReader(generated), resultKey, "image/jpeg"); err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "s3",
			"We generated your look but couldn't save it. Please try again.", err, http.StatusInternalServerError)
		return
	}
	resultURL, _ := utils.GetPresignedURL(r.Context(), resultKey)

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"result":   resultURL,
		"is_guest": true,
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
