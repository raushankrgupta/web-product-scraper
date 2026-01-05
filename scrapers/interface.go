package scrapers

import "github.com/raushankrgupta/web-product-scraper/models"

// Scraper defines the interface for all product scrapers
type Scraper interface {
	// CanScrape checks if the scraper can handle the given URL
	CanScrape(url string) bool
	// ScrapeProduct scrapes the product details from the given URL
	ScrapeProduct(url string) (*models.Product, error)
}
