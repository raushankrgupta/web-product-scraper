package scrapers

import (
	"fmt"

	"github.com/raushankrgupta/web-product-scraper/scrapers/amazon"
)

// GetScraper returns the appropriate scraper for the given URL
func GetScraper(url string) (Scraper, error) {
	// Register scrapers here
	// In a more complex system, we might use a map or a registration function
	scrapers := []Scraper{
		amazon.NewAmazonScraper(),
	}

	for _, s := range scrapers {
		if s.CanScrape(url) {
			return s, nil
		}
	}

	return nil, fmt.Errorf("no scraper found for url: %s", url)
}
