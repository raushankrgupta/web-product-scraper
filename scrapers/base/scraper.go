package base

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// BaseScraper handles common scraping logic
type BaseScraper struct {
	Client *http.Client
}

// NewBaseScraper creates a new BaseScraper instance with SSRF protections
func NewBaseScraper() *BaseScraper {
	return &BaseScraper{
		Client: utils.NewSafeHTTPClient(30 * time.Second),
	}
}

// FetchDocument fetches the URL using multiple strategies with a custom validator
func (b *BaseScraper) FetchDocument(url string, validator func(*goquery.Document) bool) (*goquery.Document, error) {
	if validator == nil {
		validator = IsValidDocument
	}

	// Validate once, here, rather than per-strategy. The ChromeDP and
	// Selenium fallbacks drive a real browser, which resolves DNS and opens
	// sockets itself — utils.SafeDialerControl guards the Go transport and
	// never sees those connections. Without this check, a target the HTTP
	// strategy refuses simply falls through to strategy 2 and is fetched
	// anyway.
	if err := utils.ValidateSafeURL(url); err != nil {
		return nil, fmt.Errorf("fetch blocked by SSRF policy: %w", err)
	}
	// Each strategy's failure is carried into the final error rather than
	// left in the breadcrumb lines below. Those go through the bare logger,
	// so they have no request_id and can only be tied back to the request
	// that failed by timestamp — which is no help once traffic is
	// concurrent. "all strategies failed" on its own is equally unhelpful:
	// a missing Chrome binary and a bot wall are the same sentence.
	var reasons []string
	note := func(strategy string, err error) {
		reasons = append(reasons, fmt.Sprintf("%s: %v", strategy, err))
	}

	// Strategy 1: HTTP Client (Fastest)
	doc, err := b.FetchDocumentHTTP(url)
	if err == nil {
		if validator(doc) {
			slog.Info(fmt.Sprintf("[BaseScraper] HTTP Success: %s", url))
			return doc, nil
		}
		slog.Info("[BaseScraper] HTTP yielded invalid content (validator failed), trying fallbacks...")
		note("http", fmt.Errorf("validator rejected content"))
	} else {
		slog.Info(fmt.Sprintf("[BaseScraper] HTTP Failed: %v", err))
		note("http", err)
	}

	// Strategy 2: ChromeDP (Headless)
	slog.Info(fmt.Sprintf("[BaseScraper] Trying ChromeDP: %s", url))
	doc, err = b.FetchDocumentChromeDP(url)
	if err == nil && validator(doc) {
		slog.Info("[BaseScraper] ChromeDP Success")
		return doc, nil
	}
	if err != nil {
		slog.Info(fmt.Sprintf("[BaseScraper] ChromeDP Failed: %v", err))
		note("chromedp", err)
	} else {
		note("chromedp", fmt.Errorf("validator rejected content"))
	}

	// Strategy 3: Selenium (Full Browser)
	slog.Info(fmt.Sprintf("[BaseScraper] Trying Selenium: %s", url))
	doc, err = b.FetchDocumentSelenium(url)
	if err == nil && validator(doc) {
		slog.Info("[BaseScraper] Selenium Success")
		return doc, nil
	}
	if err != nil {
		slog.Info(fmt.Sprintf("[BaseScraper] Selenium Failed: %v", err))
		note("selenium", err)
	} else {
		note("selenium", fmt.Errorf("validator rejected content"))
	}

	return nil, fmt.Errorf("all strategies failed for %s (%s)", url, strings.Join(reasons, "; "))
}

func IsValidDocument(doc *goquery.Document) bool {
	if doc == nil {
		return false
	}
	// Basic heuristics
	title := strings.TrimSpace(doc.Find("title").Text())
	body := strings.TrimSpace(doc.Find("body").Text())

	// Check for common blocking titles/text
	lowerTitle := strings.ToLower(title)
	if strings.Contains(lowerTitle, "robot check") ||
		strings.Contains(lowerTitle, "captcha") ||
		strings.Contains(lowerTitle, "access denied") {
		return false
	}

	return len(body) > 200 // Arbitrary small size check
}

// FetchDocumentHTTP fetches the URL and returns a GoQuery document via standard HTTP
func (b *BaseScraper) FetchDocumentHTTP(url string) (*goquery.Document, error) {
	if err := utils.ValidateSafeURL(url); err != nil {
		return nil, fmt.Errorf("fetch blocked by SSRF policy: %w", err)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Common headers to mimic a real browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-User", "?1")

	res, err := b.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("status code error: %d %s", res.StatusCode, res.Status)
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}

	return doc, nil
}
