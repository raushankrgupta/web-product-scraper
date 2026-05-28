package myntra_scraper

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// chromeUserDataDir is the Chromium profile directory used by this package.
// Intentionally distinct from scrapers/base's /tmp/chrome-user-data so the
// two packages can run their ChromeDP allocators concurrently without
// Chromium's exclusive-lock on the profile dir making one of them fail.
const chromeUserDataDir = "/tmp/chrome-user-data-myntra"

// FetchDocumentChromeDP fetches the URL using ChromeDP (headless Chromium)
// and returns the parsed document.
func (b *baseScraper) FetchDocumentChromeDP(url string) (*goquery.Document, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	chromePath := "/usr/bin/chromium"
	if envPath := os.Getenv("CHROME_BIN"); envPath != "" {
		chromePath = envPath
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("user-data-dir", chromeUserDataDir),
		chromedp.ExecPath(chromePath),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"),
	)

	// Route Chromium through the same proxy as the HTTP client so the
	// fallback strategies actually egress from a different IP than the
	// one Myntra has blocked. Without this, ChromeDP and Selenium hit
	// the same datacenter IP as the HTTP path and get the same
	// maintenance page back.
	if proxy := ScraperProxyRaw(); proxy != "" {
		opts = append(opts, chromedp.Flag("proxy-server", proxy))
		// Some proxies (ScrapingBee in particular) terminate TLS with
		// a self-signed cert. Allow that to ride only when the user
		// explicitly opts in.
		if strings.EqualFold(os.Getenv("SCRAPER_PROXY_INSECURE_TLS"), "1") ||
			strings.EqualFold(os.Getenv("SCRAPER_PROXY_INSECURE_TLS"), "true") {
			opts = append(opts, chromedp.Flag("ignore-certificate-errors", true))
		}
	}

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	headers := map[string]interface{}{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.5",
		"Connection":                "keep-alive",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
	}

	if err := chromedp.Run(taskCtx, network.SetExtraHTTPHeaders(network.Headers(headers))); err != nil {
		return nil, fmt.Errorf("chromedp header error: %w", err)
	}

	var htmlContent string
	err := chromedp.Run(taskCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(time.Duration(5+rand.Float64()*5)*time.Second), // Random delay
		chromedp.OuterHTML("html", &htmlContent),
	)

	if err != nil {
		return nil, fmt.Errorf("chromedp navigation error: %w", err)
	}

	return goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
}
