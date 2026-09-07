package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// recordScrapeFailure persists a product link we could not read.
//
// The row lands in `products` with status "failed" and no images, which is
// the shape every read path already excludes — the gallery reads `tryons`,
// and the try-on product lookup filters status != "failed" — so a diagnostic
// row can never surface in a user's feed. What the user gets instead is the
// screenshot-upload path, offered by the `reason` code in the response.
//
// Written on a background context, not the request's: this runs on paths that
// are about to return an error to a client who may well have already hung up,
// and the record is worth more than the connection.
func recordScrapeFailure(userID, productURL, resolvedURL, host, adapter, reason, flow, detail string) {
	// Best-effort: never take the process down to record a diagnostic.
	// See utils.MongoReady.
	if !utils.MongoReady() {
		slog.Warn("skipping scrape failure record: mongo not ready", "host", host, "reason", reason)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := utils.GetCollection(config.DBName, "products").InsertOne(ctx, models.Product{
		ID:             primitive.NewObjectID(),
		UserID:         userID,
		URL:            productURL,
		ResolvedURL:    resolvedURL,
		Source:         "link",
		Status:         "failed",
		ScrapeError:    detail,
		FailureReason:  reason,
		FailureHost:    host,
		FailureAdapter: adapter,
		Flow:           flow,
		CreatedAt:      time.Now(),
	})
	if err != nil {
		slog.Warn("failed to record scrape failure",
			"host", host, "reason", reason, "error", err.Error())
		return
	}
	slog.Info("scrape failure recorded",
		"host", host, "reason", reason, "adapter", adapter, "flow", flow)
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
	userID, _ := GetUserIDFromContext(r.Context())

	rawURL := productURL
	productURL, err := utils.NormalizeProductURL(productURL)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Rejected malformed URL %q: %v", rawURL, err))
		// Recorded like any other failure. A link the app itself considered
		// safe enough to send but that we then rejected is usually a share
		// sheet producing a shape we do not handle — invisible unless the
		// rejected text is kept.
		recordScrapeFailure(userID, strings.TrimSpace(rawURL), "", hostOf(rawURL), "", "invalid_url", "app",
			fmt.Sprintf("invalid_url: %v", err))
		utils.RespondErrorReason(w, nil,
			"That doesn't look like a valid product link. Please paste the full URL.",
			"invalid_url", http.StatusBadRequest)
		return
	}
	if productURL != strings.TrimSpace(rawURL) {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Normalized URL: %s", productURL))
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Scraping URL query: %s", productURL))

	// Legacy path. Clients >= 2.4.0 never reach this endpoint in device mode,
	// so every call here is either an old install or a rollback. The gate
	// decides whether we still scrape for them at all.
	if serverScrapeGate(w, r, &logMessageBuilder, userID, productURL, "app") {
		return
	}

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

	// selectScraper resolves short links and routes Myntra URLs to the
	// isolated myntra_scraper package; everything else still goes through
	// the standard scrapers.GetScraper factory.
	scraper, resolvedURL, err := selectScraper(productURL)
	if err != nil {
		recordScrapeFailure(userID, productURL, "", hostOf(productURL), "", "unsupported_site", "app",
			fmt.Sprintf("scraper_not_found: %v", err))
		// A domain we can't route is product-roadmap input, not an outage —
		// WARN with rollup so a burst of the same domain is one message.
		alert.Warnf("scraper", "no scraper found for domain", err, "domain", hostOf(productURL))
		utils.L(r.Context()).Warn("no scraper found", "domain", hostOf(productURL), "url", productURL)
		// `unsupported_site` is the app's cue to offer the crop-and-upload
		// path rather than a retry: retrying a domain we have no adapter for
		// will fail identically every time.
		utils.RespondErrorReason(w, nil,
			"We can't read product details from that site yet. Try Amazon, Flipkart, Myntra, or upload the photo directly.",
			"unsupported_site", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "products")
	adapter := scrapers.ScraperName(scraper)
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Resolved URL: %s (adapter=%s)", resolvedURL, adapter))

	product, err := scraper.ScrapeProduct(resolvedURL)
	if err != nil {
		recordScrapeFailure(userID, productURL, resolvedURL, hostOf(resolvedURL), adapter, "scrape_failed", "app",
			fmt.Sprintf("scrape_failed: %v", err))
		// `scrape_failed` covers a site we *do* support that refused us —
		// Myntra blocking the datacenter IP is the common case. Worth one
		// retry, and the app offers screenshots as the reliable fallback.
		//
		// Warn rather than Error, and 502 rather than 500: a retailer
		// blocking a scrape is upstream behaviour we expect, not a fault in
		// this server. At Error it pages, and with alerting armed at WARN a
		// single blocked domain would drown the channel in incidents nobody
		// can act on. The `reason` the app keys off is unchanged.
		alert.Warnf("scraper", "scrape failed after routing", err,
			"domain", hostOf(resolvedURL), "adapter", adapter)
		utils.L(r.Context()).Warn("scrape failed", "domain", hostOf(resolvedURL), "adapter", adapter, "error", err.Error())
		utils.RespondErrorReason(w, nil,
			"We couldn't read that product page. Please try again, or upload the photo directly.",
			"scrape_failed", http.StatusBadGateway)
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

	// An image we could not re-host keeps its original retailer URL. That is
	// deliberate — a partial product beats no product — but the URL is what
	// gets saved to the wardrobe verbatim, and retailer CDNs expire or
	// hotlink-block it, which surfaces much later as a 404 mid-generation.
	// Count them here so the scrape that caused it is the thing on record.
	var localMainKeys []string
	unhosted := 0
	for _, img := range product.Images {
		if key, ok := urlToKey[img]; ok {
			localMainKeys = append(localMainKeys, key)
		} else {
			localMainKeys = append(localMainKeys, img)
			unhosted++
		}
	}
	product.Images = localMainKeys
	if unhosted > 0 {
		utils.L(r.Context()).Warn("product images kept as remote URLs",
			"domain", hostOf(resolvedURL), "adapter", adapter,
			"unhosted", unhosted, "total", len(product.Images))
	}

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

// serverScrapeGate applies config.ServerScrapeMode to a legacy server-side
// scrape request. It reports true when it has written the response and the
// caller must return.
//
// The endpoint exists for clients that cannot be updated by us, so the two
// live modes are designed around what THEY do with the answer:
//
//   - deprecated: scrape as before, but add Deprecation/Sunset headers. The
//     legacy app ignores them; they are for anyone reading the traffic.
//   - disabled: 410 with reason "update_required". The legacy app has never
//     seen that reason, and its scrapeFailureReason() maps anything unknown to
//     "scrape_failed" — the sheet that offers "try again" and the
//     screenshot-upload path. So the old app degrades to its own fallback
//     instead of breaking. The guest screen shows the message verbatim.
//
// Every disabled hit is recorded as a failed product row: it is the count
// that decides when the scraper code can be deleted.
func serverScrapeGate(w http.ResponseWriter, r *http.Request, logger *strings.Builder, userID, productURL, flow string) bool {
	clientVersion := r.Header.Get("X-App-Version")
	if clientVersion == "" {
		clientVersion = "legacy"
	}
	utils.L(r.Context()).Info("legacy server scrape requested",
		"mode", config.ServerScrapeMode, "client_version", clientVersion,
		"flow", flow, "host", hostOf(productURL))

	switch config.ServerScrapeMode {
	case "disabled":
		recordScrapeFailure(userID, productURL, "", hostOf(productURL), "", "update_required", flow,
			"server scraping disabled; client must update to link import (client_version="+clientVersion+")")
		if config.ServerScrapeSunset != "" {
			w.Header().Set("Sunset", config.ServerScrapeSunset)
		}
		msg := "Fetching from links now happens inside the app. Please update TryOnFusion from the Play Store, or upload a screenshot of the outfit instead."
		if flow == "guest" {
			msg = "Link try-on now happens inside the app. Please update TryOnFusion from the Play Store, or upload a photo of the outfit instead."
		}
		utils.RespondErrorReason(w, logger, msg, "update_required", http.StatusGone)
		return true
	case "deprecated":
		w.Header().Set("Deprecation", "true")
		if config.ServerScrapeSunset != "" {
			w.Header().Set("Sunset", config.ServerScrapeSunset)
		}
	}
	return false
}
