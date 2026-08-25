package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// hostOf extracts the domain from a URL for logging and alerting, without
// carrying the (often tracker-laden) rest of the link into a Telegram message.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return strings.ToLower(u.Host)
}

// ScrapeHandler handles the scraping request
func ScrapeHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Scrape API]")

	// Support both Query Params and JSON Body
	productURL := r.URL.Query().Get("url")
	if productURL == "" {
		// Try JSON body
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			productURL = req.URL
		}
	}

	if productURL == "" {
		utils.RespondError(w, &logMessageBuilder, "Please provide a 'url' query parameter or JSON body", http.StatusBadRequest)
		return
	}

	// Normalise before anything else touches the string. A URL pasted from a
	// share sheet arrives as "\nhttps://...\n " often enough that it produced
	// a real "net/url: invalid control character in URL" failure in
	// production. This also strips utm_* so the same product shared from two
	// places doesn't scrape twice.
	rawURL := productURL
	productURL, err := utils.NormalizeProductURL(productURL)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Rejected malformed URL %q: %v", rawURL, err))
		utils.RespondError(w, nil, "That doesn't look like a valid product link. Please paste the full URL.", http.StatusBadRequest)
		return
	}
	if productURL != strings.TrimSpace(rawURL) {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Normalized URL: %s", productURL))
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Scraping URL query: %s", productURL))

	userID, _ := GetUserIDFromContext(r.Context())

	// Myntra blocks this server's datacenter IP. When server B (which runs on
	// a dynamic IP Myntra doesn't block) is configured, delegate Myntra scrapes
	// to it — B performs the full scrape, S3 upload and persistence, and we
	// proxy its response straight back. Non-Myntra URLs (and all URLs when B is
	// not configured) continue to be scraped locally below.
	if delegateToServerB(productURL) {
		if forwardScrapeToServerB(w, r, &logMessageBuilder, userID, productURL) {
			return
		}
		// B is unreachable. Fall through and try locally — this server's IP
		// may be blocked, but a blocked attempt beats an unconditional 502.
		utils.AddToLogMessage(&logMessageBuilder, "Server B unavailable — falling back to local scraper")
	}

	collection := utils.GetCollection(config.DBName, "products")

	saveFailedScrape := func(resolvedURL, scrapeErr string) {
		failedProduct := models.Product{
			ID:          primitive.NewObjectID(),
			UserID:      userID,
			URL:         productURL,
			ResolvedURL: resolvedURL,
			Status:      "failed",
			ScrapeError: scrapeErr,
			Source:      "link",
			CreatedAt:   time.Now(),
		}
		if _, dbErr := collection.InsertOne(r.Context(), failedProduct); dbErr != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save failed scrape record: %v", dbErr))
		} else {
			utils.AddToLogMessage(&logMessageBuilder, "Failed scrape record saved to MongoDB for debugging")
		}
	}

	// selectScraper resolves short links and routes Myntra URLs to the
	// isolated myntra_scraper package; everything else still goes through
	// the standard scrapers.GetScraper factory.
	scraper, resolvedURL, err := selectScraper(productURL)
	if err != nil {
		saveFailedScrape("", fmt.Sprintf("scraper_not_found: %v", err))
		// A domain we can't route is product-roadmap input, not an outage —
		// WARN with rollup so a burst of the same domain is one message.
		alert.Warnf("scraper", "no scraper found for domain", err, "domain", hostOf(productURL))
		utils.L(r.Context()).Warn("no scraper found", "domain", hostOf(productURL), "url", productURL)
		utils.RespondError(w, nil, "We can't read product details from that site yet. Try Amazon, Flipkart, Myntra, or upload the photo directly.", http.StatusBadRequest)
		return
	}

	adapter := scrapers.ScraperName(scraper)
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Resolved URL: %s (adapter=%s)", resolvedURL, adapter))

	product, err := scraper.ScrapeProduct(resolvedURL)
	if err != nil {
		saveFailedScrape(resolvedURL, fmt.Sprintf("scrape_failed: %v", err))
		alert.Errorf("scraper", "scrape failed after routing", err,
			"domain", hostOf(resolvedURL), "adapter", adapter)
		utils.L(r.Context()).Error("scrape failed", "domain", hostOf(resolvedURL), "adapter", adapter, "error", err.Error())
		utils.RespondError(w, nil, "We couldn't read that product page. Please try again, or upload the photo directly.", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Scraping successful")
	utils.L(r.Context()).Info("scrape success", "domain", hostOf(resolvedURL), "adapter", adapter, "images", len(product.Images))

	// Collect all images
	var allImages []string
	allImages = append(allImages, product.Images...)
	if product.CurrentSelection != nil {
		allImages = append(allImages, product.CurrentSelection.Images...)
	}
	for _, v := range product.Variants {
		allImages = append(allImages, v.Images...)
	}

	// Deduplicate
	uniqueImages := make(map[string]bool)
	var dedupedImages []string
	for _, img := range allImages {
		if _, exists := uniqueImages[img]; !exists {
			uniqueImages[img] = true
			dedupedImages = append(dedupedImages, img)
		}
	}

	// Upload images to S3
	folderName := "product_images"
	urlToKey, err := utils.UploadImagesToS3(r.Context(), dedupedImages, folderName)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Error uploading images: %v", err))
	}

	var localMainKeys []string
	for _, img := range product.Images {
		if key, ok := urlToKey[img]; ok {
			localMainKeys = append(localMainKeys, key)
		} else {
			localMainKeys = append(localMainKeys, img)
		}
	}
	product.Images = localMainKeys

	for i := range product.Variants {
		var localVarKeys []string
		for _, img := range product.Variants[i].Images {
			if key, ok := urlToKey[img]; ok {
				localVarKeys = append(localVarKeys, key)
			} else {
				localVarKeys = append(localVarKeys, img)
			}
		}
		product.Variants[i].Images = localVarKeys
	}

	// Save to MongoDB
	product.ID = primitive.NewObjectID()
	product.UserID = userID
	product.URL = productURL
	product.ResolvedURL = resolvedURL
	product.Status = "success"
	product.CreatedAt = time.Now()

	_, err = collection.InsertOne(r.Context(), product)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save product to MongoDB: %v", err))
	} else {
		utils.AddToLogMessage(&logMessageBuilder, "Product saved to MongoDB")
	}

	// Generate Presigned URLs for response
	product.Images = utils.PresignImageURLs(r.Context(), product.Images)
	for i := range product.Variants {
		product.Variants[i].Images = utils.PresignImageURLs(r.Context(), product.Variants[i].Images)
	}

	utils.RespondJSON(w, http.StatusOK, product)
}
