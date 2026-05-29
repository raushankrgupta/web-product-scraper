package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/myntra_scraper"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// serverBClient is reused across requests. Myntra scrapes on server B can run
// the ChromeDP/Selenium fallback chain, which takes well over a minute, so the
// timeout is generous.
var serverBClient = &http.Client{Timeout: 3 * time.Minute}

// looksLikeKnownNonMyntraStore lets delegateToServerB classify direct store
// URLs as "not Myntra" without paying for a redirect-following network
// resolution on the hot path. Shortened/unknown links are still resolved.
func looksLikeKnownNonMyntraStore(rawURL string) bool {
	u := strings.ToLower(rawURL)
	return strings.Contains(u, "amazon") || strings.Contains(u, "amzn") ||
		strings.Contains(u, "flipkart.com") ||
		strings.Contains(u, "tatacliq.com") ||
		strings.Contains(u, "peterengland")
}

// delegateToServerB reports whether productURL should be scraped on server B.
// It returns false when SERVER_B_SCRAPE_URL is unset (so scraping stays local,
// exactly as before) or when the URL is not a Myntra URL. Shortened/share
// deeplinks are resolved once to classify them.
func delegateToServerB(productURL string) bool {
	if config.ServerBScrapeURL == "" {
		return false
	}
	if myntra_scraper.IsMyntraURL(productURL) {
		return true // direct Myntra URL — no network round-trip needed
	}
	if looksLikeKnownNonMyntraStore(productURL) {
		return false // direct non-Myntra store URL — skip resolution
	}
	// Could be a shortened/share deeplink. Resolve once to classify it.
	resolved, err := utils.ResolveShortenedURL(productURL)
	if err != nil {
		return false
	}
	return myntra_scraper.IsMyntraURL(resolved)
}

// callServerB POSTs {user_id, url, persist} to server B's scrape endpoint,
// authenticated with the shared internal secret. persist=false asks B for an
// ephemeral scrape (no DB/S3 write). The caller owns resp.Body.
func callServerB(ctx context.Context, userID, productURL string, persist bool) (*http.Response, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"user_id": userID,
		"url":     productURL,
		"persist": persist,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.ServerBScrapeURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", config.InternalAPISecret)

	return serverBClient.Do(req)
}

// forwardScrapeToServerB delegates the entire scrape (including image upload
// and persistence) to server B and proxies B's response back to the client
// verbatim, so the client sees exactly what it would have when scraping
// happened locally.
func forwardScrapeToServerB(w http.ResponseWriter, r *http.Request, logger *strings.Builder, userID, productURL string) {
	utils.AddToLogMessage(logger, fmt.Sprintf("Delegating Myntra scrape to server B: %s", productURL))

	// The /product/details flow persists, exactly as the local path used to.
	resp, err := callServerB(r.Context(), userID, productURL, true)
	if err != nil {
		utils.RespondError(w, logger, fmt.Sprintf("Scraper service (server B) unreachable: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.RespondError(w, logger, fmt.Sprintf("Failed reading server B response: %v", err), http.StatusBadGateway)
		return
	}

	utils.AddToLogMessage(logger, fmt.Sprintf("Server B responded %d", resp.StatusCode))

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// scrapeViaServerB delegates a scrape to server B and returns the parsed
// product. Used by flows that need the product in-process (e.g. guest try-on)
// rather than proxying the HTTP response straight to the client. Pass
// persist=false for an ephemeral scrape that doesn't write to B's DB/S3.
func scrapeViaServerB(ctx context.Context, userID, productURL string, persist bool) (*models.Product, error) {
	resp, err := callServerB(ctx, userID, productURL, persist)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		// Server B returns {"error": "..."} on failure.
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("server B: %s", errResp.Error)
		}
		return nil, fmt.Errorf("server B returned status %d", resp.StatusCode)
	}

	var product models.Product
	if err := json.Unmarshal(body, &product); err != nil {
		return nil, fmt.Errorf("failed to decode server B product: %w", err)
	}
	return &product, nil
}
