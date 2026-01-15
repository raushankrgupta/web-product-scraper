package amazon

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers/base"
)

// AmazonScraper handles the HTML parsing for Amazon
type AmazonScraper struct {
	*base.BaseScraper
}

func NewAmazonScraper() *AmazonScraper {
	return &AmazonScraper{
		BaseScraper: base.NewBaseScraper(),
	}
}

func (s *AmazonScraper) CanScrape(url string) bool {
	return strings.Contains(url, "amazon") || strings.Contains(url, "amzn")
}

func (s *AmazonScraper) ScrapeProduct(url string) (*models.Product, error) {
	doc, err := s.FetchDocument(url, func(doc *goquery.Document) bool {
		return strings.TrimSpace(doc.Find("#productTitle").Text()) != ""
	})
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	// 1. Title
	product.Title = strings.TrimSpace(doc.Find("#productTitle").Text())

	// 2. Price (MRP and Discounted)
	// Initialize variables
	var mrp, discountedPrice string

	// Strategy for Discounted Price (Selling Price)
	// 1. .a-price.a-text-price.a-size-medium.apexPriceToPay .a-offscreen (New design)
	// 2. .priceToPay .a-offscreen
	// 3. #corePriceDisplay_desktop_feature_div .a-price .a-offscreen
	// 4. #priceblock_ourprice
	// 5. #priceblock_dealprice

	discountedPrice = doc.Find(".priceToPay .a-offscreen").First().Text()
	if strings.TrimSpace(discountedPrice) == "" {
		// Fallback to visible price if offscreen is empty
		whole := doc.Find(".priceToPay .a-price-whole").First().Text()
		if whole != "" {
			symbol := doc.Find(".priceToPay .a-price-symbol").First().Text()
			if symbol == "" {
				symbol = "₹"
			}
			discountedPrice = symbol + whole
		}
	}
	if discountedPrice == "" {
		discountedPrice = doc.Find("#corePriceDisplay_desktop_feature_div .a-price.apexPriceToPay .a-offscreen").First().Text()
	}
	if discountedPrice == "" {
		discountedPrice = doc.Find(".a-price.a-text-price.a-size-medium.apexPriceToPay .a-offscreen").First().Text()
	}
	if discountedPrice == "" {
		discountedPrice = doc.Find("#priceblock_dealprice").Text()
	}
	if discountedPrice == "" {
		discountedPrice = doc.Find("#priceblock_ourprice").Text()
	}
	if discountedPrice == "" {
		// Fallback to the first price found in the main area
		discountedPrice = doc.Find(".a-price .a-offscreen").First().Text()
	}
	// Extra fallback for Indian Amazon: .a-price-whole (often just the number, need to add symbol if missing)
	if discountedPrice == "" {
		whole := doc.Find(".a-price-whole").First().Text()
		if whole != "" {
			discountedPrice = "₹" + strings.TrimSuffix(whole, ".")
		}
	}

	// Strategy for MRP (List Price)
	// 1. .basisPrice .a-offscreen
	// 2. span[data-a-strike="true"] .a-offscreen
	// 3. .a-text-price .a-offscreen (but exclude the one that might be the selling price if structure is weird)

	mrp = doc.Find(".basisPrice .a-offscreen").First().Text()
	if mrp == "" {
		// Look for struck-through prices
		mrp = doc.Find("span[data-a-strike='true'] .a-offscreen").First().Text()
	}
	if mrp == "" {
		// Often MRP is in a span with class a-text-price that is NOT the selling price
		// We can look for "M.R.P.:" label and find the price next to it
		doc.Find("span.a-text-price").Each(func(i int, s *goquery.Selection) {
			if mrp != "" {
				return
			}
			// Check if this block contains "M.R.P" in previous sibling or parent text?
			// Or just take the text if it looks like a price and is greater than discounted price (logic hard to do with strings)
			// Usually the first .a-text-price inside the price block that is NOT the main price is the MRP
			text := s.Find(".a-offscreen").Text()
			if text != "" && text != discountedPrice {
				mrp = text
			}
		})
	}

	// Regex Fallback if still empty
	if discountedPrice == "" || mrp == "" {
		bodyText := doc.Find("body").Text()
		rePrice := regexp.MustCompile(`(₹|Rs\.?)\s?[\d,]+(\.\d{2})?`)
		matches := rePrice.FindAllString(bodyText, -1)

		if discountedPrice == "" && len(matches) > 0 {
			discountedPrice = matches[0] // Assume first price found is selling price
		}
		// MRP is usually higher, but hard to guess from regex alone without context.
		// We leave MRP empty if not found via selectors to avoid bad guesses.
	}

	product.DiscountedPrice = strings.TrimSpace(discountedPrice)
	product.MRP = strings.TrimSpace(mrp)

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

	// 5. Category & Subcategory
	var categories []string
	doc.Find("#wayfinding-breadcrumbs_feature_div ul li").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text != "" && text != "›" {
			categories = append(categories, text)
		}
	})
	if len(categories) > 0 {
		product.Category = strings.Join(categories, " > ")
		product.Subcategory = categories[len(categories)-1]
	}

	// 6. Technical Details (Dimensions, Material, Fit Type)
	// Strategy: Look for table rows in productDetails_techSpec_section_1
	doc.Find("#productDetails_techSpec_section_1 tr").Each(func(i int, s *goquery.Selection) {
		key := strings.TrimSpace(s.Find("th").Text())
		val := strings.TrimSpace(s.Find("td").Text())

		if strings.Contains(key, "Dimensions") {
			product.Dimensions = val
		}
		if strings.Contains(key, "Material") || strings.Contains(key, "Fabric") {
			product.Material = val
		}
		if strings.Contains(key, "Fit") {
			product.FitType = val
		}
	})

	// Strategy: Product Facts (New Design)
	doc.Find(".product-facts-detail").Each(func(i int, s *goquery.Selection) {
		key := strings.TrimSpace(s.Find(".a-col-left span").First().Text())
		val := strings.TrimSpace(s.Find(".a-col-right span").First().Text())

		if strings.Contains(key, "Material") {
			product.Material = val
		}
		if strings.Contains(key, "Fit") {
			product.FitType = val
		}
	})

	// Helper to extract key-value from text
	reKeyVal := regexp.MustCompile(`(?i)(?:Material|Fabric|Fit|Fit Type|Dimensions)\s*[-:]\s*(.*)`)

	extractFromText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}

		// Check for Dimensions
		if product.Dimensions == "" && strings.Contains(text, "Dimensions") {
			matches := reKeyVal.FindStringSubmatch(text)
			if len(matches) > 1 {
				product.Dimensions = strings.TrimSpace(matches[1])
			} else if parts := strings.Split(text, ":"); len(parts) > 1 {
				product.Dimensions = strings.TrimSpace(parts[1])
			}
		}

		// Check for Material
		if product.Material == "" && (strings.Contains(text, "Material") || strings.Contains(text, "Fabric")) {
			matches := reKeyVal.FindStringSubmatch(text)
			if len(matches) > 1 {
				product.Material = strings.TrimSpace(matches[1])
			} else {
				parts := strings.Split(text, "-")
				if len(parts) > 1 {
					product.Material = strings.TrimSpace(parts[1])
				} else {
					parts = strings.Split(text, ":")
					if len(parts) > 1 {
						product.Material = strings.TrimSpace(parts[1])
					}
				}
			}
		}

		// Check for Fit Type
		if product.FitType == "" && (strings.Contains(text, "Fit") || strings.Contains(text, "Fit Type")) {
			matches := reKeyVal.FindStringSubmatch(text)
			if len(matches) > 1 {
				product.FitType = strings.TrimSpace(matches[1])
			} else {
				parts := strings.Split(text, "-")
				if len(parts) > 1 {
					product.FitType = strings.TrimSpace(parts[1])
				} else {
					parts = strings.Split(text, ":")
					if len(parts) > 1 {
						product.FitType = strings.TrimSpace(parts[1])
					}
				}
			}
		}
	}

	// Fallback 1: ID-based bullets
	doc.Find("#detailBullets_feature_div li, #feature-bullets li").Each(func(i int, s *goquery.Selection) {
		extractFromText(s.Text())
	})

	// Fallback 2: "About this item" section search (if still missing fields)
	if product.Material == "" || product.FitType == "" || product.Dimensions == "" {
		doc.Find("h1, h2, h3, h4, b, strong").EachWithBreak(func(i int, s *goquery.Selection) bool {
			if strings.Contains(strings.ToLower(s.Text()), "about this item") {
				// Look for siblings or parent's siblings

				// Case 1: The header is inside the block
				parent := s.Parent()
				parent.Find("ul li").Each(func(j int, li *goquery.Selection) {
					extractFromText(li.Text())
				})

				if product.Material != "" && product.FitType != "" {
					return false
				}

				// Case 2: The header is just above the UL
				next := s.Parent().Next()
				for k := 0; k < 5; k++ {
					if next.Is("ul") || next.Find("ul").Length() > 0 {
						next.Find("li").Each(func(j int, li *goquery.Selection) {
							extractFromText(li.Text())
						})
						if product.Material != "" && product.FitType != "" {
							return false
						}
					}
					next = next.Next()
				}
				return false // Stop after finding "About this item"
			}
			return true
		})
	}

	// Clean up Dimensions (remove non-printable chars like \u200e)
	if product.Dimensions != "" {
		// Remove Left-to-Right Mark and other invisible chars
		product.Dimensions = strings.ReplaceAll(product.Dimensions, "\u200e", "")
		product.Dimensions = strings.Join(strings.Fields(product.Dimensions), " ")
	}

	// Fix for Discounted Price if missing but Discount exists
	if product.DiscountedPrice == "" && product.Discount != "" {
		// Try to find the discount element and look near it
		doc.Find(".savingsPercentage, .a-color-price").EachWithBreak(func(i int, s *goquery.Selection) bool {
			if strings.Contains(s.Text(), product.Discount) {
				// Look in parent
				parent := s.Parent()
				// Try to find a price in parent
				price := parent.Find(".a-price .a-offscreen").First().Text()
				if price != "" {
					product.DiscountedPrice = strings.TrimSpace(price)
					return false
				}
				// Try grandparent
				grandparent := parent.Parent()
				price = grandparent.Find(".a-price .a-offscreen").First().Text()
				if price != "" {
					product.DiscountedPrice = strings.TrimSpace(price)
					return false
				}
				// Try siblings of parent
				parent.Siblings().Each(func(j int, sib *goquery.Selection) {
					if product.DiscountedPrice != "" {
						return
					}
					price = sib.Find(".a-price .a-offscreen").First().Text()
					if price != "" {
						product.DiscountedPrice = strings.TrimSpace(price)
					}
				})
				if product.DiscountedPrice != "" {
					return false
				}

				// Try finding just a number if .a-price structure is missing
				// This is risky but better than empty
				return false
			}
			return true
		})
	}

	// 7. Images (Main & Alt Views)
	// Strategy:
	// 1. Look for #altImages or #imageBlock thumbnails.
	// 2. These usually represent different views.
	// 3. Convert thumbnail URL to high-res URL.

	// Helper to convert to high res
	toHighRes := func(url string) string {
		// URLs usually look like: .../I/71sbtz8S+aL._AC_US40_.jpg
		// We want to remove the ._..._.jpg part before the extension
		// Regex to find the pattern `\._.+_\.` and replace with `.`
		// Or simpler: remove everything between `._` and the last `.`

		re := regexp.MustCompile(`\._.+_\.`)
		return re.ReplaceAllString(url, ".")
	}

	var foundImages []string

	// Try Alt Images
	doc.Find("#altImages ul li.item img").Each(func(i int, s *goquery.Selection) {
		src := s.AttrOr("src", "")
		if src != "" {
			foundImages = append(foundImages, toHighRes(src))
		}
	})

	// If alt images not found (single image product?), try landingImage
	if len(foundImages) == 0 {
		imageJson := doc.Find("#landingImage").AttrOr("data-a-dynamic-image", "")
		if imageJson == "" {
			imageJson = doc.Find("#imgBlkFront").AttrOr("data-a-dynamic-image", "")
		}
		if imageJson != "" {
			var images map[string]interface{}
			if err := json.Unmarshal([]byte(imageJson), &images); err == nil {
				// Pick the largest image URL? Or just the first one?
				// The keys are URLs.
				for url := range images {
					foundImages = append(foundImages, url)
					break // Just take one Main image if we rely on this, to avoid duplicates
				}
			}
		} else {
			src := doc.Find("#landingImage").AttrOr("src", "")
			if src != "" {
				foundImages = append(foundImages, src)
			}
		}
	}

	product.Images = foundImages

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

				product.Variants = append(product.Variants, models.Variant{
					ASIN:   asin,
					Size:   sizeVal,
					Color:  colorVal,
					Images: variantImages,
				})
			}
		}
	}

	// Identify Current Selection
	// 1. Try to get ASIN from URL
	// 2. Try to get ASIN from page
	currentASIN := ""
	// Regex for ASIN in URL
	reASIN := regexp.MustCompile(`/(dp|gp/product)/([A-Z0-9]{10})`)
	matchASIN := reASIN.FindStringSubmatch(url)
	if len(matchASIN) > 2 {
		currentASIN = matchASIN[2]
	}

	if currentASIN == "" {
		// Try finding hidden input
		currentASIN = doc.Find("input#ASIN").AttrOr("value", "")
	}

	if currentASIN != "" {
		for _, v := range product.Variants {
			if v.ASIN == currentASIN {
				product.CurrentSelection = &v
				break
			}
		}
	}

	// If still nil, maybe we can scrape the selected options directly
	if product.CurrentSelection == nil {
		// Fallback: Scrape selected size/color
		size := strings.TrimSpace(doc.Find("#variation_size_name .selection").Text())
		color := strings.TrimSpace(doc.Find("#variation_color_name .selection").Text())
		if size != "" || color != "" {
			product.CurrentSelection = &models.Variant{
				ASIN:   currentASIN,
				Size:   size,
				Color:  color,
				Images: product.Images, // Assume main images match current selection if we can't find specific variant images
			}
		}
	}

	// Clear variants list as requested by user
	product.Variants = nil

	return product, nil
}
