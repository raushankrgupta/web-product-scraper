package base

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

// browserHeadless reports whether ChromeDP should drive Chromium headlessly.
// Defaults to true so the Dockerised server (no display) is unchanged. Set
// CHROME_HEADLESS=false (also accepts 0/no/off) when running locally with
// `go run main.go` to render the page in a real, visible browser window —
// this is what gets past the bot challenges that block headless requests.
func browserHeadless() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHROME_HEADLESS"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// resolveChromePath returns the Chrome/Chromium binary to launch. CHROME_BIN
// wins if set (Docker sets it to /usr/bin/chromium); otherwise we probe the
// usual install paths so a plain `go run main.go` on a dev machine (which
// typically has Google Chrome, not /usr/bin/chromium) works out of the box.
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

// antiDetectScript masks the automation signals sites fingerprint on.
const antiDetectScript = `
Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
window.chrome = window.chrome || { runtime: {} };
Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
`

// FetchDocumentChromeDP loads the URL in a real Chromium instance and returns
// the fully-rendered page. Headless on the server; run locally with
// CHROME_HEADLESS=false to render in a visible window before reading the DOM.
func (b *BaseScraper) FetchDocumentChromeDP(url string) (*goquery.Document, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	headless := browserHeadless()
	chromePath := resolveChromePath()

	// Set up browser options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// enable-automation (on by default in chromedp) sets
		// navigator.webdriver=true and the automation banner; turn it off.
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("user-data-dir", "/tmp/chrome-user-data"),
		chromedp.ExecPath(chromePath),
		chromedp.WindowSize(1920, 1080),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"),
	)

	if headless {
		fmt.Printf("[BaseScraper] ChromeDP headless render via %s\n", chromePath)
	} else {
		opts = append(opts, chromedp.Flag("start-maximized", true))
		fmt.Printf("[BaseScraper] ChromeDP HEADFUL render via %s (CHROME_HEADLESS=false)\n", chromePath)
	}

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	// Create a new browser context
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Set headers
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

	// Set extra HTTP headers
	if err := chromedp.Run(taskCtx, network.SetExtraHTTPHeaders(network.Headers(headers))); err != nil {
		return nil, fmt.Errorf("chromedp header error: %w", err)
	}

	var htmlContent string
	err := chromedp.Run(taskCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(antiDetectScript).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Evaluate(`window.scrollTo({top: document.body.scrollHeight / 2, behavior: 'smooth'});`, nil),
		chromedp.Sleep(time.Duration(5+rand.Float64()*5)*time.Second), // Random delay
		chromedp.OuterHTML("html", &htmlContent),
	)

	if err != nil {
		return nil, fmt.Errorf("chromedp navigation error: %w", err)
	}

	return goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
}
