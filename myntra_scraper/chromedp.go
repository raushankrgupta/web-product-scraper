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
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// chromeUserDataDir is the Chromium profile directory used by this package.
// Intentionally distinct from scrapers/base's /tmp/chrome-user-data so the
// two packages can run their ChromeDP allocators concurrently without
// Chromium's exclusive-lock on the profile dir making one of them fail.
const chromeUserDataDir = "/tmp/chrome-user-data-myntra"

// browserHeadless reports whether ChromeDP should drive Chromium headlessly.
//
// It defaults to true so the Dockerised production server (which has no
// display) keeps working exactly as before. Set CHROME_HEADLESS=false (also
// accepts 0/no/off) when running locally with `go run main.go` so the page is
// loaded in a real, visible browser window. Myntra's bot challenge passes far
// more reliably against a headful browser on a residential IP than against a
// headless one, which is the whole reason for the local-render fallback.
func browserHeadless() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHROME_HEADLESS"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// resolveChromePath returns the Chrome/Chromium binary ChromeDP should launch.
// CHROME_BIN wins if set (Docker sets it to /usr/bin/chromium). Otherwise we
// probe the common install locations and return the first that exists, so a
// plain `go run main.go` on a dev machine works without extra config — most
// desktops ship Google Chrome at /usr/bin/google-chrome rather than the
// /usr/bin/chromium the container uses.
func resolveChromePath() string {
	if p := strings.TrimSpace(os.Getenv("CHROME_BIN")); p != "" {
		return p
	}
	for _, c := range []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "/usr/bin/chromium"
}

// userDataDir lets a local run point Chromium at a specific profile via
// CHROME_USER_DATA_DIR (e.g. to reuse a logged-in Myntra session, which makes
// the scrape look even more like a real user). Defaults to the package-private
// dir so it never collides with scrapers/base's profile.
func userDataDir() string {
	if d := strings.TrimSpace(os.Getenv("CHROME_USER_DATA_DIR")); d != "" {
		return d
	}
	return chromeUserDataDir
}

// proxyInsecureTLS reports whether the user opted into trusting a proxy's
// self-signed MITM certificate (ScrapingBee and many residential proxies
// present one when they decrypt outbound HTTPS).
func proxyInsecureTLS() bool {
	v := os.Getenv("SCRAPER_PROXY_INSECURE_TLS")
	return strings.EqualFold(v, "1") || strings.EqualFold(v, "true")
}

// antiDetectScript is injected before every navigation so the signals Myntra's
// fingerprinting reads (navigator.webdriver, the chrome runtime object, the
// plugin/language lists) look like a normal browser instead of an automated
// one. It mirrors the mask script the Selenium path already uses.
const antiDetectScript = `
Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
window.chrome = window.chrome || { runtime: {} };
Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
`

// FetchDocumentChromeDP loads the URL in a real Chromium instance and returns
// the fully-rendered document.
//
// On the server it runs headless (CHROME_HEADLESS defaults to true). Run it
// locally with CHROME_HEADLESS=false and it opens a visible window and renders
// the page exactly like a human tab before reading the DOM — this is the
// headful flow ported from etl-producer's GetPageDataFromUrl /
// getPageSourceByChromDP, which is the only reliable way past Myntra's bot
// challenge. Either way it waits for the body, nudges lazy content, lets the
// JS settle, then grabs the outer HTML.
func (b *baseScraper) FetchDocumentChromeDP(url string) (*goquery.Document, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	headless := browserHeadless()
	chromePath := resolveChromePath()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// DefaultExecAllocatorOptions turns on enable-automation, which sets
		// navigator.webdriver=true and shows the "controlled by automated
		// software" banner. Myntra keys off both, so switch it back off.
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("user-data-dir", userDataDir()),
		chromedp.ExecPath(chromePath),
		chromedp.WindowSize(1920, 1080),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"),
	)

	if headless {
		fmt.Printf("[MyntraScraper] ChromeDP headless render via %s\n", chromePath)
	} else {
		// Foreground the window so it renders like a real, focused tab.
		opts = append(opts, chromedp.Flag("start-maximized", true))
		fmt.Printf("[MyntraScraper] ChromeDP HEADFUL render via %s (CHROME_HEADLESS=false)\n", chromePath)
	}

	// Route Chromium through the same proxy as the HTTP client so the
	// fallback strategies actually egress from a different IP than the one
	// Myntra has blocked. Without this, ChromeDP hits the same datacenter IP
	// as the HTTP path and gets the same maintenance page back.
	if proxy := ScraperProxyRaw(); proxy != "" {
		opts = append(opts, chromedp.Flag("proxy-server", proxy))
		if proxyInsecureTLS() {
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
		// Mask automation signals before any page script runs.
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(antiDetectScript).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		// Scroll like a human to trigger any lazy-loaded content, then let
		// the SPA hydrate (window.__myx) before snapshotting the DOM.
		chromedp.Evaluate(`window.scrollTo({top: document.body.scrollHeight / 2, behavior: 'smooth'});`, nil),
		chromedp.Sleep(time.Duration(5+rand.Float64()*5)*time.Second), // 5-10s random settle
		chromedp.OuterHTML("html", &htmlContent),
	)
	if err != nil {
		return nil, fmt.Errorf("chromedp navigation error: %w", err)
	}

	return goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
}
