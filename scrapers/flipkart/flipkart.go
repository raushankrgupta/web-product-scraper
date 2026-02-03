package flipkart

import (
	"encoding/json"
	"fmt"
	"regexp"
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

	// 5. JSON State Fallback (Robustness)
	if product.DiscountedPrice == "" || len(product.Images) == 0 {
		doc.Find("script").EachWithBreak(func(i int, s *goquery.Selection) bool {
			text := s.Text()
			if strings.Contains(text, "window.__INITIAL_STATE__") {
				// Extract JSON
				start := strings.Index(text, "window.__INITIAL_STATE__ = ") + len("window.__INITIAL_STATE__ = ")
				sub := text[start:]
				// It usually ends with a semicolon
				end := strings.Index(sub, ";")
				if end != -1 {
					jsonStr := sub[:end]
					var state map[string]interface{}
					if err := json.Unmarshal([]byte(jsonStr), &state); err == nil {
						// Traverse pageData -> pageContext -> analyticsData (or similar)
						// This structure is complex and changes, but let's try to find key fields recursively or known paths

						// Try to find "pageDataV4" or similar
						if pageData, ok := state["pageDataV4"].(map[string]interface{}); ok {
							if pageContext, ok := pageData["pageContext"].(map[string]interface{}); ok {
								if titles, ok := pageContext["titles"].(map[string]interface{}); ok {
									if title, ok := titles["title"].(string); ok && product.Title == "" {
										product.Title = title
									}
								}
								if pricing, ok := pageContext["pricing"].(map[string]interface{}); ok {
									if finalPrice, ok := pricing["finalPrice"].(map[string]interface{}); ok {
										if val, ok := finalPrice["value"].(float64); ok && product.DiscountedPrice == "" {
											product.DiscountedPrice = fmt.Sprintf("₹%.0f", val)
										}
									}
									if mrp, ok := pricing["mrp"].(map[string]interface{}); ok {
										if val, ok := mrp["value"].(float64); ok && product.MRP == "" {
											product.MRP = fmt.Sprintf("₹%.0f", val)
										}
									}
								}
							}
						}

						// Image fallback from state is checking "multimediaComponents" logic which is hard.
						// Instead, valid regex usage for image URLs in the whole script if images are still empty.
						if len(product.Images) == 0 {
							// Look for higher res images in the JSON string
							// Format: http://.../image/{@width}/{@height}/...
							// We want 832/832
							// Regex to find image URLs
							// This is a bit "dirty" but effective
						}
					}
				}
				return false
			}
			return true
		})
	}

	// Regex fallback for Images if still empty
	if len(product.Images) == 0 {
		html, _ := doc.Html()
		// Look for flixcart image URLs
		// pattern: https://rukminim1.flixcart.com/image/.../....jpg (or png/webp)
		// We avoid /128/128/ or thumbnails if possible, but identifying "main" is hard.
		// Let's capture generic patterns and filter.
		// e.g. https://rukminim1.flixcart.com/image/{@width}/{@height}/...
		// or actual dimensions.

		// Simple strategy: Find all flixcart image URLs, deduplicate, and upgrade resolution
		// Avoid regex on full HTML if possible, but here we are desperate.
		// Better: look in known script variables via regex

		reImg := regexp.MustCompile(`https://rukminim[0-9]*\.flixcart\.com/image/[0-9]+/[0-9]+/[^"]+`)
		matches := reImg.FindAllString(html, -1)
		unique := make(map[string]bool)
		for _, m := range matches {
			// Upgrade resolution
			highRes := m
			// Replace resolution parts like /128/128/ with /832/832/
			reRes := regexp.MustCompile(`/[0-9]+/[0-9]+/`)
			highRes = reRes.ReplaceAllString(highRes, "/832/832/")

			if !unique[highRes] {
				unique[highRes] = true
				product.Images = append(product.Images, highRes)
			}
		}
	}

	return product, nil
}
