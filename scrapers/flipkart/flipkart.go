package flipkart

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers/base"
)

type FlipkartScraper struct {
	*base.BaseScraper
}

func NewFlipkartScraper() *FlipkartScraper {
	return &FlipkartScraper{
		BaseScraper: base.NewBaseScraper(),
	}
}

func (s *FlipkartScraper) CanScrape(url string) bool {
	return strings.Contains(url, "flipkart.com")
}

func (s *FlipkartScraper) ScrapeProduct(url string) (*models.Product, error) {
	doc, err := s.FetchDocument(url, func(doc *goquery.Document) bool {
		// Check for title class or h1
		return doc.Find("h1").Length() > 0 || doc.Find(".B_NuCI").Length() > 0
	})
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	// 1. Title
	product.Title = strings.TrimSpace(doc.Find(".B_NuCI").Text())
	if product.Title == "" {
		// Fallback for new design
		product.Title = strings.TrimSpace(doc.Find("h1.yhB1nd span").Text())
	}
	if product.Title == "" {
		// Generic fallback
		product.Title = strings.TrimSpace(doc.Find("h1").First().Text())
	}

	// 2. Price
	product.DiscountedPrice = strings.TrimSpace(doc.Find("div._30jeq3._16Jk6d").Text())
	if product.DiscountedPrice == "" {
		product.DiscountedPrice = strings.TrimSpace(doc.Find("div.Nx9bqj.CxhGGd").Text()) // New class
	}

	product.MRP = strings.TrimSpace(doc.Find("div._3I9_wc._2p6lqe").Text())
	if product.MRP == "" {
		product.MRP = strings.TrimSpace(doc.Find("div.yRaY8j.A6ZONS").Text()) // New class
	}

	product.Discount = strings.TrimSpace(doc.Find("div._3Ay6Sb._31Dcoz span").Text())
	if product.Discount == "" {
		product.Discount = strings.TrimSpace(doc.Find("div.UkUFwK.WW8yVX span").Text()) // New class
	}

	// 3. Description
	product.Description = strings.TrimSpace(doc.Find("div._1mXcCf").Text())
	if product.Description == "" {
		product.Description = strings.TrimSpace(doc.Find("div.yN5-Ad").Text()) // Description block
	}

	// 4. Images
	// Flipkart images are often populated dynamically or are in specific containers
	// Looking for the list of thumbnails
	doc.Find("ul._3GnUWp li._20Gt85").Each(func(i int, s *goquery.Selection) {
		img := s.Find("img").AttrOr("src", "")
		// Transform to high res: often replace 128/128 with original or 832/832
		// URL format: https://rukminim1.flixcart.com/image/128/128/xif0q/...
		if img != "" {
			highRes := strings.Replace(img, "/128/128/", "/832/832/", 1)
			product.Images = append(product.Images, highRes)
		}
	})

	if len(product.Images) == 0 {
		// Try finding the main image
		mainImg := doc.Find("img._396cs4").AttrOr("src", "")
		if mainImg != "" {
			product.Images = append(product.Images, mainImg)
		}
	}

	return product, nil
}
