package myntra

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers/base"
)

type MyntraScraper struct {
	*base.BaseScraper
}

func NewMyntraScraper() *MyntraScraper {
	return &MyntraScraper{
		BaseScraper: base.NewBaseScraper(),
	}
}

func (s *MyntraScraper) CanScrape(url string) bool {
	return strings.Contains(url, "myntra.com")
}

func (s *MyntraScraper) ScrapeProduct(url string) (*models.Product, error) {
	doc, err := s.FetchDocument(url, func(doc *goquery.Document) bool {
		// Check for the script tag containing data OR basic h1
		return strings.Contains(doc.Text(), "window.__myx") || doc.Find("h1").Length() > 0
	})
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	// Improved Myntra scraping: Extract JSON from script
	// Look for script containing "window.__myx = "
	var jsonStr string
	doc.Find("script").Each(func(i int, s *goquery.Selection) {
		text := s.Text()
		if strings.Contains(text, "window.__myx =") {
			// Extract JSON
			start := strings.Index(text, "window.__myx =") + len("window.__myx =")
			// Find end - usually it ends with object structure, might need to trim semicolon
			// But careful as it might be complex.
			// Let's take the rest of the string and try to find proper JSON closure or just trim spaces
			// Actually window.__myx = { ... };
			// We can try to take substring
			sub := text[start:]
			sub = strings.TrimSpace(sub)
			if strings.HasSuffix(sub, ";") {
				sub = sub[:len(sub)-1]
			}
			jsonStr = sub
		}
	})

	if jsonStr != "" {
		// Parsing this huge JSON
		var validJson map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &validJson); err == nil {
			if pd, ok := validJson["pdpData"].(map[string]interface{}); ok {
				product.Title = getString(pd, "name")
				if product.Title == "" {
					product.Title = getString(pd, "title") // sometimes title
				}

				// Price
				product.MRP = fmt.Sprintf("%v", pd["mrp"])
				product.DiscountedPrice = fmt.Sprintf("%v", pd["price"])
				// If price is int, formatted

				// Ensure formatted with currency if just numbers
				if !strings.Contains(product.DiscountedPrice, "Rs") && product.DiscountedPrice != "" {
					product.DiscountedPrice = "Rs. " + product.DiscountedPrice
				}
				if !strings.Contains(product.MRP, "Rs") && product.MRP != "" {
					product.MRP = "Rs. " + product.MRP
				}

				product.Description = getString(pd, "productDetails") // often html

				// Images
				if media, ok := pd["media"].(map[string]interface{}); ok {
					if albums, ok := media["albums"].([]interface{}); ok {
						for _, album := range albums {
							if albumMap, ok := album.(map[string]interface{}); ok {
								if images, ok := albumMap["images"].([]interface{}); ok {
									for _, img := range images {
										if imgMap, ok := img.(map[string]interface{}); ok {
											src := getString(imgMap, "src")
											if src != "" {
												product.Images = append(product.Images, src)
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Fallback to HTML parsing if JSON fails
	if product.Title == "" {
		product.Title = strings.TrimSpace(doc.Find(".pdp-title").Text())
		if product.Title == "" {
			product.Title = strings.TrimSpace(doc.Find(".pdp-name").Text())
		}

		product.DiscountedPrice = strings.TrimSpace(doc.Find(".pdp-price").First().Text())
		product.MRP = strings.TrimSpace(doc.Find(".pdp-mrp").First().Text())
		product.Discount = strings.TrimSpace(doc.Find(".pdp-discount").First().Text())
		product.Description = strings.TrimSpace(doc.Find(".pdp-product-description-content").Text())

		doc.Find(".image-grid-image").Each(func(i int, s *goquery.Selection) {
			style := s.AttrOr("style", "")
			// extract url from background-image: url("...")
			if strings.Contains(style, "url(") {
				start := strings.Index(style, "url(") + 4
				end := strings.Index(style[start:], ")")
				if end != -1 {
					url := style[start : start+end]
					url = strings.Trim(url, "\"'")
					product.Images = append(product.Images, url)
				}
			}
		})
	}

	return product, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
