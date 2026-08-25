package utils

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// trackingParams are query keys that identify the *referral*, never the
// product. Stripping them fixes a real dedup problem visible in production:
// the same Meesho product arrived twice with different utm_source values and
// was treated as two distinct scrapes.
//
// Note what is NOT here: `variant`. On Shopify stores that parameter selects
// the actual SKU, so dropping it would silently scrape the wrong colour or
// size. Only this allow-list is removed — never "all query params".
var trackingParams = map[string]bool{
	"gclid": true, "fbclid": true, "srsltid": true, "igshid": true,
	"mc_cid": true, "mc_eid": true, "ref_src": true, "_branch_match_id": true,
}

// NormalizeProductURL cleans a user-pasted product URL.
//
// A URL pasted from a share sheet arrives with a leading newline and trailing
// whitespace often enough that it killed a real request:
//
//	parse "\nhttps://www.meesho.com/s/p/c4jdu0?utm_source=...\n ":
//	net/url: invalid control character in URL
//
// So: trim, take the first whitespace-delimited token (share text frequently
// appends a caption after the link), default a missing scheme to https, and
// strip tracking parameters.
func NormalizeProductURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	// Cut at the first internal whitespace — "look at this <url> nice na?"
	if i := strings.IndexAny(s, " \t\r\n"); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "", fmt.Errorf("empty url")
	}

	// A scheme-less paste ("www.meesho.com/...") is still a URL to a user.
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("url has no host: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported url scheme %q", u.Scheme)
	}

	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || strings.HasPrefix(lk, "gad_") || trackingParams[lk] {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""

	return u.String(), nil
}

// ResolveShortenedURL follows redirects to find the final URL
// ResolveShortenedURL follows redirects to find the final URL
func ResolveShortenedURL(url string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Keep following redirects
			return nil
		},
	}

	// Use GET directly. HEAD is often blocked or treated suspiciously by anti-bot systems.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return url, err
	}

	// Mimic a real browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := client.Do(req)
	if err != nil {
		return url, err
	}
	defer resp.Body.Close()

	return resp.Request.URL.String(), nil
}
