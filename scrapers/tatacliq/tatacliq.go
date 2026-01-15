package tatacliq

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers/base"
)

type TataCliqScraper struct {
	*base.BaseScraper
}

func NewTataCliqScraper() *TataCliqScraper {
	return &TataCliqScraper{
		BaseScraper: base.NewBaseScraper(),
	}
}

func (s *TataCliqScraper) CanScrape(url string) bool {
	return strings.Contains(url, "tatacliq.com")
}

func (s *TataCliqScraper) ScrapeProduct(url string) (*models.Product, error) {
	doc, err := s.FetchDocument(url, func(doc *goquery.Document) bool {
		// Needs strictly dynamic content
		return doc.Find(".ProductDescriptionPage__productName").Length() > 0 || doc.Find(".ProductDetailsMainCard__productName").Length() > 0
	})
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	// TataCliq uses dynamic content heavily, but some basics might be in meta tags or specific divs

	// 1. Title
	product.Title = strings.TrimSpace(doc.Find("h1.ProductDescriptionPage__productName").Text())
	if product.Title == "" {
		// Fallback checks
		product.Title = strings.TrimSpace(doc.Find(".ProductDetailsMainCard__productName").Text())
	}

	// 2. Price
	product.DiscountedPrice = strings.TrimSpace(doc.Find(".ProductDescriptionPage__price").Text())
	if product.DiscountedPrice == "" {
		product.DiscountedPrice = strings.TrimSpace(doc.Find(".ProductDetailsMainCard__price").Text())
	}

	product.MRP = strings.TrimSpace(doc.Find(".ProductDescriptionPage__mrp").Text())
	if product.MRP == "" {
		product.MRP = strings.TrimSpace(doc.Find(".ProductDetailsMainCard__mrp").Text())
	}

	product.Discount = strings.TrimSpace(doc.Find(".ProductDescriptionPage__discount").Text())

	// 3. Description
	product.Description = strings.TrimSpace(doc.Find(".ProductDescriptionPage__productDescription").Text())
	if product.Description == "" {
		product.Description = strings.TrimSpace(doc.Find(".ProductDetailsMainCard__description").Text())
	}

	// 4. Images
	// TataCliq images are tricky with basic scraping.
	// Often found in .ImageGallery__image or similar
	doc.Find("img.ImageGallery__image").Each(func(i int, s *goquery.Selection) {
		src := s.AttrOr("src", "")
		if src != "" {
			product.Images = append(product.Images, src)
		}
	})

	// Also check for meta image
	if len(product.Images) == 0 {
		metaImg := doc.Find("meta[property='og:image']").AttrOr("content", "")
		if metaImg != "" {
			product.Images = append(product.Images, metaImg)
		}
	}

	return product, nil
}
