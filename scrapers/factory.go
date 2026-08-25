package scrapers

import (
	"fmt"

	"github.com/raushankrgupta/web-product-scraper/scrapers/amazon"
	"github.com/raushankrgupta/web-product-scraper/scrapers/flipkart"
	"github.com/raushankrgupta/web-product-scraper/scrapers/generic"
	"github.com/raushankrgupta/web-product-scraper/scrapers/myntra"
	"github.com/raushankrgupta/web-product-scraper/scrapers/peterengland"
	"github.com/raushankrgupta/web-product-scraper/scrapers/shopify"
	"github.com/raushankrgupta/web-product-scraper/scrapers/tatacliq"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// registry is the ordered scraper chain. Order is load-bearing:
//
//   - Site-specific adapters come first, so a known retailer always gets its
//     hand-tuned extraction.
//   - shopify is next: it recognises the platform rather than the domain, and
//     covers a long tail of D2C stores through Shopify's own product JSON.
//   - generic is LAST and its CanScrape always returns true. It is the reason
//     an unknown domain now returns a product instead of
//     "no scraper found for url", which is how 7 of 9 production scrapes died.
func registry() []Scraper {
	return []Scraper{
		amazon.NewAmazonScraper(),
		flipkart.NewFlipkartScraper(),
		myntra.NewMyntraScraper(),
		tatacliq.NewTataCliqScraper(),
		peterengland.NewPeterEnglandScraper(),
		shopify.NewShopifyScraper(),
		generic.NewGenericScraper(), // must stay last — CanScrape() is always true
	}
}

// GetScraper returns the appropriate scraper and the resolved URL.
func GetScraper(url string) (Scraper, string, error) {
	// Resolve shortened URLs (e.g., amzn.in, bit.ly)
	resolvedURL, err := utils.ResolveShortenedURL(url)
	if err != nil {
		return nil, url, fmt.Errorf("error resolving url: %v", err)
	}

	for _, s := range registry() {
		if s.CanScrape(resolvedURL) {
			return s, resolvedURL, nil
		}
	}

	return nil, resolvedURL, fmt.Errorf("no scraper found for url: %s", resolvedURL)
}

// ScraperName reports which adapter handled a URL, for logging and for the
// "which domains can we not scrape" digest.
func ScraperName(s Scraper) string {
	switch s.(type) {
	case *amazon.AmazonScraper:
		return "amazon"
	case *flipkart.FlipkartScraper:
		return "flipkart"
	case *myntra.MyntraScraper:
		return "myntra"
	case *tatacliq.TataCliqScraper:
		return "tatacliq"
	case *peterengland.PeterEnglandScraper:
		return "peterengland"
	case *shopify.ShopifyScraper:
		return "shopify"
	case *generic.GenericScraper:
		return "generic"
	default:
		return fmt.Sprintf("%T", s)
	}
}
