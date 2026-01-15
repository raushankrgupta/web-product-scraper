package peterengland

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers/base"
)

type PeterEnglandScraper struct {
	*base.BaseScraper
}

func NewPeterEnglandScraper() *PeterEnglandScraper {
	return &PeterEnglandScraper{
		BaseScraper: base.NewBaseScraper(),
	}
}

func (s *PeterEnglandScraper) CanScrape(url string) bool {
	return strings.Contains(url, "peterengland.abfrl.in") || strings.Contains(url, "peterengland")
}

func (s *PeterEnglandScraper) ScrapeProduct(url string) (*models.Product, error) {
	doc, err := s.FetchDocument(url, func(doc *goquery.Document) bool {
		return doc.Find("h1.pdp-title").Length() > 0 || doc.Find(".ProductDetails__productName").Length() > 0
	})
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	// 1. Title
	product.Title = strings.TrimSpace(doc.Find("h1.pdp-title").Text())
	if product.Title == "" {
		product.Title = strings.TrimSpace(doc.Find(".ProductDetails__productName").Text())
	}
	if product.Title == "" {
		// Fallback to page title "Name Online - ID | Brand"
		pageTitle := doc.Find("title").Text()
		if parts := strings.Split(pageTitle, " Online -"); len(parts) > 1 {
			product.Title = strings.TrimSpace(parts[0])
		}
	}

	// 2. Price
	product.DiscountedPrice = strings.TrimSpace(doc.Find(".pdp-price strong").Text())
	product.MRP = strings.TrimSpace(doc.Find(".pdp-mrp del").Text())

	// Fallbacks
	if product.DiscountedPrice == "" {
		product.DiscountedPrice = strings.TrimSpace(doc.Find(".ProductDetails__price").Text())
	}

	// 3. Description
	product.Description = strings.TrimSpace(doc.Find(".pdp-desc").Text())

	// 4. Images
	doc.Find(".Start-image-gallery img").Each(func(i int, s *goquery.Selection) {
		src := s.AttrOr("src", "")
		if src != "" {
			product.Images = append(product.Images, src)
		}
	})

	// Also check generic carousels
	if len(product.Images) == 0 {
		doc.Find(".slick-track img").Each(func(i int, s *goquery.Selection) {
			src := s.AttrOr("src", "")
			if src != "" {
				product.Images = append(product.Images, src)
			}
		})
	}

	return product, nil
}
