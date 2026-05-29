// Package myntra_scraper is an isolated, self-contained scraper for
// myntra.com. It deliberately does not depend on scrapers/base so that the
// anti-bot tweaks Myntra needs (proxy egress, custom validators, IP-block
// detection, longer/different fetch flow) can evolve without risking the
// other scrapers (Amazon / Flipkart / Tata CLiQ / Peter England) that share
// scrapers/base.
//
// Resources are namespaced to avoid race conditions when this package and
// scrapers/base run concurrently in the same process:
//
//   - Selenium ChromeDriver port range: 4500-4515 (scrapers/base uses 4444-4459).
//   - Chromium --user-data-dir:        /tmp/chrome-user-data-myntra
//     (scrapers/base uses              /tmp/chrome-user-data).
//   - Proxy URL cache:                 each package has its own sync.Once.
//
// Both packages still read the SCRAPER_PROXY_URL env var, but each caches
// its own *url.URL independently, so there is no shared mutable state.
package myntra_scraper

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// ScraperProxyURL returns a parsed *url.URL for the SCRAPER_PROXY_URL env
// var, or nil if no proxy is configured. The proxy is shared by the HTTP
// client, ChromeDP, and Selenium so all three strategies egress from the
// same IP. This is the only reliable way to scrape Myntra, which IP-blocks
// AWS / GCP / Azure datacenter ranges.
//
// Supported forms:
//
//	http://user:pass@proxy.host:8080
//	https://proxy.host:443
//	socks5://user:pass@proxy.host:1080
//
// ScrapingBee proxy-mode URL also works:
//
//	http://YOUR_API_KEY:render_js=true@proxy.scrapingbee.com:8886
//
// The parsed URL is cached so we don't re-parse on every request. The cache
// is package-local; scrapers/base keeps its own independent cache.
var (
	cachedProxyURL  *url.URL
	cachedProxyOnce sync.Once
	cachedProxyRaw  string
)

func ScraperProxyURL() *url.URL {
	cachedProxyOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("SCRAPER_PROXY_URL"))
		if raw == "" {
			return
		}
		cachedProxyRaw = raw
		pu, err := url.Parse(raw)
		if err != nil {
			fmt.Printf("[MyntraScraper] SCRAPER_PROXY_URL invalid (%v); proxy disabled\n", err)
			return
		}
		cachedProxyURL = pu
		fmt.Printf("[MyntraScraper] proxy enabled (host=%s scheme=%s)\n", pu.Host, pu.Scheme)
	})
	return cachedProxyURL
}

// ScraperProxyRaw returns the original SCRAPER_PROXY_URL string. Used by
// ChromeDP / Selenium which need the raw string for `--proxy-server`.
func ScraperProxyRaw() string {
	ScraperProxyURL()
	return cachedProxyRaw
}

// baseScraper handles common HTTP/ChromeDP/Selenium fetch logic for the
// isolated Myntra scraper. It is intentionally not exported - callers
// should use NewMyntraScraper().
type baseScraper struct {
	Client *http.Client
}

func newBaseScraper() *baseScraper {
	transport := &http.Transport{
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	if pu := ScraperProxyURL(); pu != nil {
		transport.Proxy = http.ProxyURL(pu)
		// ScrapingBee / many residential proxies present a self-signed
		// MITM cert when they decrypt outbound HTTPS. Allow the user to
		// opt into that with SCRAPER_PROXY_INSECURE_TLS=1.
		if strings.EqualFold(os.Getenv("SCRAPER_PROXY_INSECURE_TLS"), "1") ||
			strings.EqualFold(os.Getenv("SCRAPER_PROXY_INSECURE_TLS"), "true") {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	}

	return &baseScraper{
		Client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

// inspectDoc returns a short body sample and the page title for diagnostic
// logging when a strategy returns content that fails the per-scraper
// validator. Without this we used to silently move on to the next strategy
// and the user would only see "all strategies failed" with no clue why.
func inspectDoc(doc *goquery.Document) (int, string) {
	if doc == nil {
		return 0, ""
	}
	body := doc.Text()
	title := strings.TrimSpace(doc.Find("title").Text())
	return len(body), title
}

// looksLikeIPBlock returns true if the document is one of the well-known
// "you're blocked" / "site under maintenance" responses that origin servers
// serve to suspected datacenter traffic. Detecting this short-circuits the
// expensive ChromeDP/Selenium fallbacks (they egress from the same IP and
// will get the same response) and lets us surface a useful error to the
// caller instead of timing out 15s later with "all strategies failed".
func looksLikeIPBlock(doc *goquery.Document) bool {
	if doc == nil {
		return false
	}
	title := strings.ToLower(strings.TrimSpace(doc.Find("title").Text()))
	body := strings.ToLower(doc.Text())

	// Myntra serves a static "Site Maintenance" stub (~328 bytes of text)
	// to AWS IPs. The real page is never <1KB.
	maintenance := strings.Contains(title, "site maintenance") ||
		strings.Contains(title, "under maintenance") ||
		strings.Contains(body, "we are currently performing some maintenance") ||
		strings.Contains(body, "we'll be back shortly")
	if maintenance {
		return true
	}

	// Generic Akamai / Cloudflare / Imperva block pages.
	if strings.Contains(title, "access denied") ||
		strings.Contains(title, "attention required") ||
		strings.Contains(body, "request unsuccessful. incapsula") ||
		strings.Contains(body, "reference #18.") /* Akamai */ {
		return true
	}
	return false
}

// FetchDocument fetches the URL using HTTP -> ChromeDP -> Selenium fallback
// chain. Each strategy's output is run through the per-call validator
// before being accepted, and well-known IP-block stubs short-circuit the
// rest of the chain (it would just hit the same IP and the same stub).
func (b *baseScraper) FetchDocument(rawURL string, validator func(*goquery.Document) bool) (*goquery.Document, error) {
	host := hostOf(rawURL)

	// Strategy 1: HTTP Client (Fastest)
	doc, err := b.FetchDocumentHTTP(rawURL)
	if err == nil {
		if validator(doc) {
			fmt.Printf("[MyntraScraper] HTTP Success: %s\n", rawURL)
			return doc, nil
		}
		bodyLen, titleText := inspectDoc(doc)
		fmt.Printf("[MyntraScraper] HTTP yielded invalid content (validator failed) - bodyTextLen=%d title=%q url=%s\n", bodyLen, titleText, rawURL)

		// If the response is the host's "you are blocked / site under
		// maintenance" stub AND we have no proxy configured, a *headless*
		// browser would hit the exact same IP and get the same stub, so we
		// short-circuit to save ~15s and surface an actionable error.
		//
		// We deliberately do NOT short-circuit in headful render mode
		// (CHROME_HEADLESS=false, i.e. the local cloudflared fallback): a
		// real visible browser on a residential IP is precisely what gets
		// past this block, so let it run ChromeDP below instead of bailing.
		if looksLikeIPBlock(doc) && ScraperProxyURL() == nil && browserHeadless() {
			return nil, fmt.Errorf("scrape blocked by %s (server returned %q in %d bytes) - the host is rejecting this server's IP as datacenter/bot traffic; configure SCRAPER_PROXY_URL (residential proxy or scraping service) or run the local renderer with CHROME_HEADLESS=false to fix", host, titleText, bodyLen)
		}
	} else {
		fmt.Printf("[MyntraScraper] HTTP Failed: %v\n", err)
	}

	// Strategy 2: ChromeDP (Headless)
	fmt.Printf("[MyntraScraper] Trying ChromeDP: %s\n", rawURL)
	doc, err = b.FetchDocumentChromeDP(rawURL)
	if err == nil {
		if validator(doc) {
			fmt.Printf("[MyntraScraper] ChromeDP Success: %s\n", rawURL)
			return doc, nil
		}
		bodyLen, titleText := inspectDoc(doc)
		fmt.Printf("[MyntraScraper] ChromeDP yielded invalid content (validator failed) - bodyTextLen=%d title=%q url=%s\n", bodyLen, titleText, rawURL)
		if looksLikeIPBlock(doc) && ScraperProxyURL() == nil {
			return nil, fmt.Errorf("scrape blocked by %s (ChromeDP also returned %q) - the host is rejecting this server's IP; configure SCRAPER_PROXY_URL to fix", host, titleText)
		}
	} else {
		fmt.Printf("[MyntraScraper] ChromeDP Failed: %v\n", err)
	}

	// Strategy 3: Selenium (Full Browser)
	fmt.Printf("[MyntraScraper] Trying Selenium: %s\n", rawURL)
	doc, err = b.FetchDocumentSelenium(rawURL)
	if err == nil {
		if validator(doc) {
			fmt.Printf("[MyntraScraper] Selenium Success: %s\n", rawURL)
			return doc, nil
		}
		bodyLen, titleText := inspectDoc(doc)
		fmt.Printf("[MyntraScraper] Selenium yielded invalid content (validator failed) - bodyTextLen=%d title=%q url=%s\n", bodyLen, titleText, rawURL)
		if looksLikeIPBlock(doc) {
			return nil, fmt.Errorf("scrape blocked by %s across all strategies (last seen: %q) - configure or rotate SCRAPER_PROXY_URL", host, titleText)
		}
	} else {
		fmt.Printf("[MyntraScraper] Selenium Failed: %v\n", err)
	}

	return nil, fmt.Errorf("all strategies failed for %s", rawURL)
}

// FetchDocumentHTTP fetches the URL via the standard HTTP client (Strategy 1).
func (b *baseScraper) FetchDocumentHTTP(url string) (*goquery.Document, error) {
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
	// Setting `Sec-Fetch-Site: cross-site` and an empty Referer was making
	// Myntra take an anti-bot fast path. Mimic a same-origin navigation
	// from the site's own root, which is what a user pasting a link into
	// a Myntra browser tab actually triggers.
	if host := hostOf(req.URL.String()); host != "" {
		req.Header.Set("Referer", "https://"+host+"/")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	} else {
		req.Header.Set("Sec-Fetch-Site", "cross-site")
	}
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
