package base

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// BaseScraper handles common scraping logic
type BaseScraper struct {
	Client *http.Client
}

// NewBaseScraper creates a new BaseScraper instance
func NewBaseScraper() *BaseScraper {
	return &BaseScraper{
		Client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				ForceAttemptHTTP2:     false,
				TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

// FetchDocument fetches the URL using multiple strategies with a custom validator
func (b *BaseScraper) FetchDocument(url string, validator func(*goquery.Document) bool) (*goquery.Document, error) {
	// Strategy 1: HTTP Client (Fastest)
	doc, err := b.FetchDocumentHTTP(url)
	if err == nil {
		if validator(doc) {
			slog.Info(fmt.Sprintf("[BaseScraper] HTTP Success: %s", url))
			return doc, nil
		} else {
			slog.Info("[BaseScraper] HTTP yielded invalid content (validator failed), trying fallbacks...")
		}
	} else {
		slog.Info(fmt.Sprintf("[BaseScraper] HTTP Failed: %v", err))
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
	}

	return nil, fmt.Errorf("all strategies failed for %s", url)
}

func isValidDocument(doc *goquery.Document) bool {
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
