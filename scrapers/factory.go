package scrapers

import (
	"fmt"

	"github.com/raushankrgupta/web-product-scraper/scrapers/amazon"
	"github.com/raushankrgupta/web-product-scraper/scrapers/flipkart"
	"github.com/raushankrgupta/web-product-scraper/scrapers/myntra"
	"github.com/raushankrgupta/web-product-scraper/scrapers/peterengland"
	"github.com/raushankrgupta/web-product-scraper/scrapers/tatacliq"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// GetScraper returns the appropriate scraper and the resolved URL
func GetScraper(url string) (Scraper, string, error) {
	// Resolve shortened URLs (e.g., amzn.in, bit.ly)
	resolvedURL, err := utils.ResolveShortenedURL(url)
	if err != nil {
		return nil, url, fmt.Errorf("error resolving url: %v", err)
	}

	// Register scrapers here
	scrapers := []Scraper{
		amazon.NewAmazonScraper(),
		flipkart.NewFlipkartScraper(),
		myntra.NewMyntraScraper(),
		tatacliq.NewTataCliqScraper(),
		peterengland.NewPeterEnglandScraper(),
	}

	for _, s := range scrapers {
		if s.CanScrape(resolvedURL) {
			return s, resolvedURL, nil
		}
	}

	return nil, resolvedURL, fmt.Errorf("no scraper found for url: %s", resolvedURL)
}
