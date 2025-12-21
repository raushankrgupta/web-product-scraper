package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Scraper handles the HTML parsing
type Scraper struct {
	Client *http.Client
}

func NewScraper() *Scraper {
	return &Scraper{
		Client: &http.Client{},
	}
}

func (s *Scraper) ScrapeProduct(url string) (*Product, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Important: User-Agent to avoid immediate blocking
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("status code error: %d %s", res.StatusCode, res.Status)
	}

	// Save HTML for debugging variants
	bodyBytes, _ := io.ReadAll(res.Body)
	os.WriteFile("debug.html", bodyBytes, 0644)
	// Create a new reader since we consumed the body
	res.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}

	product := &Product{}

	// 1. Title
	product.Title = strings.TrimSpace(doc.Find("#productTitle").Text())

	// 2. Price
	// Try multiple selectors
	// often .a-price .a-offscreen is hidden, goquery text() gets it.
	price := doc.Find(".period .a-offscreen").First().Text() // deals
	if price == "" {
		price = doc.Find(".a-price .a-offscreen").First().Text()
	}
	if price == "" {
		price = doc.Find("#priceblock_ourprice").Text()
	}
	if price == "" {
		price = doc.Find("#priceblock_dealprice").Text()
	}
	if price == "" {
		price = doc.Find(".apexPriceToPay .a-offscreen").Text()
	}
	if price == "" {
		// Regex fallback for Indian Rupee symbols or Rs.
		// We use a broader regex and look for the first valid match that looks like a price
		re := regexp.MustCompile(`(₹|Rs\.?)\s?[\d,]+(\.\d{2})?`)

		// search in the text of the body, but cleaner
		bodyText := doc.Find("body").Text()
		// Replace newlines to make regex works better on single line if needed, but here simple search is fine
		found := re.FindString(bodyText)
		if found != "" {
			price = strings.TrimSpace(found)
		}
	}
	product.Price = strings.TrimSpace(price)

	// 3. Discount (e.g. -42%)
	discount := doc.Find(".savingsPercentage").First().Text()
	if discount == "" {
		discount = doc.Find(".a-color-price").FilterFunction(func(i int, s *goquery.Selection) bool {
			return strings.Contains(s.Text(), "%") && strings.Contains(s.Text(), "-")
		}).First().Text()
	}
	product.Discount = strings.TrimSpace(discount)

	// 4. Description
	// Try feature bullets first
	var description []string
	doc.Find("#feature-bullets li span.a-list-item").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text != "" {
			description = append(description, text)
		}
	})

	// If bullets empty, try product description
	if len(description) == 0 {
		descText := strings.TrimSpace(doc.Find("#productDescription").Text())
		if descText != "" {
			description = append(description, descText)
		}
	}

	// Fallback: Use "About this item" cleaner logic
	if len(description) == 0 {
		// Find the element text "About this item" and look for the next UL
		doc.Find("h1, h2, h3, h4, b, strong").EachWithBreak(func(i int, s *goquery.Selection) bool {
			if strings.Contains(strings.ToLower(s.Text()), "about this item") {
				// Look for siblings or parent's siblings
				// Strategy: Go up to parent, find feature-bullets or look for next UL

				// Case 1: The header is inside the block
				parent := s.Parent()
				parent.Find("ul li").Each(func(j int, li *goquery.Selection) {
					text := strings.TrimSpace(li.Text())
					// simple filter to avoid menu items
					if text != "" && len(text) > 10 && !strings.Contains(text, "Make sure this fits") {
						description = append(description, text)
					}
				})

				if len(description) > 0 {
					return false
				}

				// Case 2: The header is just above the UL (standard structure)
				// traverse next siblings until we find a UL
				next := s.Parent().Next()
				for k := 0; k < 5; k++ { // try next 5 siblings
					if next.Is("ul") || next.Find("ul").Length() > 0 {
						next.Find("li").Each(func(j int, li *goquery.Selection) {
							text := strings.TrimSpace(li.Text())
							if text != "" && len(text) > 10 {
								description = append(description, text)
							}
						})
						if len(description) > 0 {
							return false
						}
					}
					next = next.Next()
				}

				if len(description) > 0 {
					return false
				}
			}
			return true
		})
	}

	product.Description = strings.Join(description, "\n")

	// 5. Images (Main)
	imageJson := doc.Find("#landingImage").AttrOr("data-a-dynamic-image", "")
	if imageJson == "" {
		imageJson = doc.Find("#imgBlkFront").AttrOr("data-a-dynamic-image", "")
	}
	if imageJson != "" {
		var images map[string]interface{}
		if err := json.Unmarshal([]byte(imageJson), &images); err == nil {
			for url := range images {
				product.Images = append(product.Images, url)
			}
		}
	} else {
		src := doc.Find("#landingImage").AttrOr("src", "")
		if src != "" {
			product.Images = append(product.Images, src)
		}
	}

	// --- Variant / Twister Logic ---
	// Scan ALL scripts to find the scattered data pieces
	var variationValues map[string][]string
	var dimToAsin map[string]string
	var colorImages map[string][]struct {
		HiRes string `json:"hiRes"`
		Large string `json:"large"`
		Thumb string `json:"thumb"`
	}
	var dimensions []string

	// Regex compilation
	reVarValues := regexp.MustCompile(`"variationValues"\s*:\s*({[^}]+})`)
	reDimMap := regexp.MustCompile(`"dimensionToAsinMap"\s*:\s*({[^}]+})`)
	reDims := regexp.MustCompile(`"dimensions"\s*:\s*(\[[^\]]+\])`)
	// Loose regex for colorImages inside huge JSON strings
	// We look for the pattern "colorImages": { ... } inside a block,
	// but since it's often in a string passed to jQuery.parseJSON, checks are complex.
	// We will look for just the "colorImages" key structure in any script.
	// However, the cleanest way for the `jQuery.parseJSON` case is extracting the string arg.
	reBigJson := regexp.MustCompile(`jQuery\.parseJSON\('([^']+)'\)`)

	doc.Find("script").Each(func(i int, s *goquery.Selection) {
		html := s.Text()

		// 1. variationValues
		if variationValues == nil {
			matchVar := reVarValues.FindStringSubmatch(html)
			if len(matchVar) > 1 {
				json.Unmarshal([]byte(matchVar[1]), &variationValues)
			}
		}

		// 2. dimensionToAsinMap
		if dimToAsin == nil {
			matchDim := reDimMap.FindStringSubmatch(html)
			if len(matchDim) > 1 {
				json.Unmarshal([]byte(matchDim[1]), &dimToAsin)
			}
		}

		// 3. dimensions array
		if dimensions == nil {
			matchDims := reDims.FindStringSubmatch(html)
			if len(matchDims) > 1 {
				json.Unmarshal([]byte(matchDims[1]), &dimensions)
			}
		}

		// 4. colorImages
		if colorImages == nil {
			// Check for jQuery.parseJSON pattern
			allMatches := reBigJson.FindAllStringSubmatch(html, -1)
			for _, m := range allMatches {
				if len(m) > 1 {
					jsonStr := m[1]
					if strings.Contains(jsonStr, "colorImages") {
						var data map[string]interface{}
						if err := json.Unmarshal([]byte(jsonStr), &data); err == nil {
							if ci, ok := data["colorImages"]; ok {
								ciBytes, _ := json.Marshal(ci)
								json.Unmarshal(ciBytes, &colorImages)
							}
						}
					}
				}
			}
			// Also check for direct assignment: data["colorImages"] = ... (if valid JSON)
			// Or 'colorImages': { ... } inside another object
			if colorImages == nil && strings.Contains(html, "colorImages") {
				// Fallback: try to find a JSON-like object starting with "colorImages"
				// This is hard to regex robustly.
			}
		}
	})

	// Now construct variants if we have enough data
	if dimToAsin != nil && variationValues != nil {
		if len(dimensions) == 0 {
			dimensions = []string{"size_name", "color_name"}
		}

		sizeIdx := -1
		colorIdx := -1
		for i, d := range dimensions {
			if strings.Contains(d, "size") {
				sizeIdx = i
			}
			if strings.Contains(d, "color") {
				colorIdx = i
			}
		}

		// Only proceed if we can map dimensions
		if sizeIdx != -1 || colorIdx != -1 {
			getValue := func(dimName string, idx int) string {
				vals, ok := variationValues[dimName]
				if !ok || idx >= len(vals) {
					return ""
				}
				return vals[idx]
			}

			for key, asin := range dimToAsin {
				parts := strings.Split(key, "_")
				if len(parts) != len(dimensions) {
					continue
				}

				var sizeVal, colorVal string
				var indices []int
				for _, p := range parts {
					var val int
					fmt.Sscanf(p, "%d", &val)
					indices = append(indices, val)
				}

				if sizeIdx != -1 && sizeIdx < len(indices) {
					sizeVal = getValue(dimensions[sizeIdx], indices[sizeIdx])
				}
				if colorIdx != -1 && colorIdx < len(indices) {
					colorVal = getValue(dimensions[colorIdx], indices[colorIdx])
				}

				var variantImages []string
				// If colorVal is empty, maybe we only have size?
				// ColorImages needs a color name.
				if colorVal != "" {
					if imgs, ok := colorImages[colorVal]; ok {
						for _, img := range imgs {
							if img.HiRes != "" {
								variantImages = append(variantImages, img.HiRes)
							} else if img.Large != "" {
								variantImages = append(variantImages, img.Large)
							}
						}
					}
				}

				product.Variants = append(product.Variants, Variant{
					ASIN:   asin,
					Size:   sizeVal,
					Color:  colorVal,
					Images: variantImages,
				})
			}
		}
	}

	return product, nil
}
