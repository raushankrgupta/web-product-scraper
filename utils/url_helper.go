package utils

import (
	"net/http"
	"time"
)

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
