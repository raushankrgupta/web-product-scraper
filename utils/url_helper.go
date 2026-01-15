package utils

import (
	"net/http"
	"time"
)

// ResolveShortenedURL follows redirects to find the final URL
func ResolveShortenedURL(url string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Keep following redirects
			return nil
		},
	}

	// Create a custom request to set headers
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return url, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		// Fallback to GET if HEAD fails or returns non-200 (Note: client follows redirects, so 200 is expected at end)
		// But HEAD sometimes doesn't follow redirects well or servers block it.
		// Try GET
		req, err = http.NewRequest("GET", url, nil)
		if err != nil {
			return url, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

		resp, err = client.Do(req)
		if err != nil {
			return url, err
		}
	}
	defer resp.Body.Close()

	return resp.Request.URL.String(), nil
}
