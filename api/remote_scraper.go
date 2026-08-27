package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/myntra_scraper"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// serverBClient is reused across requests. Myntra scrapes on server B can run
// the ChromeDP/Selenium fallback chain, so the timeout is generous — but not
// 3 minutes, which was long enough for a user to give up, retry, and pay for
// a second scrape while the first was still running.
var serverBClient = &http.Client{Timeout: 90 * time.Second}

// errServerBDisabled is returned instead of dialling when the offload path is
// switched off. It is a normal operating state, not a failure: callers fall
// back to scraping locally.
var errServerBDisabled = errors.New("server B is disabled (SERVER_B_ENABLED=false)")

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
// It returns false when the offload path is switched off — SERVER_B_ENABLED=false
// or no SERVER_B_SCRAPE_URL — so scraping stays local, exactly as before, or
// when the URL is not a Myntra URL. Shortened/share deeplinks are resolved once
// to classify them.
//
// The disabled check comes first so a stood-down B costs nothing: no redirect
// resolution, no DNS lookup against a hostname we already know is dead.
func delegateToServerB(productURL string) bool {
	if !config.ServerBEnabled {
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
	// Single choke point for the flag. delegateToServerB already gates the
	// scrape path, but this guarantees no code path — present or future —
	// can dial a B that operations have switched off.
	if !config.ServerBEnabled {
		return nil, errServerBDisabled
	}

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
//
// It returns false when B could not be reached at all, so the caller can fall
// back to scraping locally. A local attempt may well be IP-blocked, but a
// blocked attempt is strictly better than the unconditional 502 this used to
// return — which is exactly what users got for six days while B's quick
// tunnel hostname no longer resolved.
func forwardScrapeToServerB(w http.ResponseWriter, r *http.Request, logger *strings.Builder, userID, productURL string) bool {
	utils.AddToLogMessage(logger, fmt.Sprintf("Delegating Myntra scrape to server B: %s", productURL))

	// The /product/details flow persists, exactly as the local path used to.
	resp, err := callServerB(r.Context(), userID, productURL, true)
	if err != nil {
		utils.AddToLogMessage(logger, fmt.Sprintf("Server B unreachable: %v", err))
		markServerBUnhealthy(err)
		alert.Errorf("serverb", "scrape delegation failed", err, "url", redactHost(config.ServerBScrapeURL))
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.AddToLogMessage(logger, fmt.Sprintf("Failed reading server B response: %v", err))
		alert.Errorf("serverb", "unreadable response", err)
		return false
	}

	utils.AddToLogMessage(logger, fmt.Sprintf("Server B responded %d", resp.StatusCode))
	if resp.StatusCode >= 500 {
		alert.Errorf("serverb", "scrape returned 5xx", fmt.Errorf("status %d", resp.StatusCode))
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return true
}

// markServerBUnhealthy records a transport failure seen on the request path so
// /health reflects it immediately rather than waiting for the next 5-minute
// poll.
func markServerBUnhealthy(err error) {
	serverBMu.Lock()
	serverBHealthy = false
	serverBReason = err.Error()
	serverBChecked = time.Now()
	serverBMu.Unlock()
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
