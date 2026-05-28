package api

import (
	"github.com/raushankrgupta/web-product-scraper/myntra_scraper"
	"github.com/raushankrgupta/web-product-scraper/scrapers"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// selectScraper resolves the user-supplied URL (following shorteners /
// share deeplinks) and picks the right scraper for it.
//
// Myntra is intentionally routed to the dedicated myntra_scraper package so
// its anti-bot fetch logic (proxy egress, custom validators, IP-block
// detection, distinct ChromeDriver port range, distinct Chromium
// user-data-dir) can evolve without touching scrapers/base — which is
// shared by every other site (Amazon, Flipkart, Tata CLiQ, Peter England).
// Anything that is not a Myntra URL is dispatched through the existing
// scrapers.GetScraper factory exactly as before, with no behaviour change.
//
// The URL is resolved once here. The factory's own ResolveShortenedURL
// call then becomes a no-op redirect chain for the non-Myntra path.
func selectScraper(productURL string) (scrapers.Scraper, string, error) {
	resolvedURL, err := utils.ResolveShortenedURL(productURL)
	if err != nil {
		return nil, productURL, err
	}
	if myntra_scraper.IsMyntraURL(resolvedURL) {
		return myntra_scraper.NewMyntraScraper(), resolvedURL, nil
	}
	return scrapers.GetScraper(resolvedURL)
}
