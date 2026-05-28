package myntra_scraper

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
)

// MyntraScraper implements the scrapers.Scraper interface (CanScrape +
// ScrapeProduct) without importing the scrapers package, so the rest of
// the codebase can dispatch it through that interface while this package
// stays free of scrapers/base coupling.
type MyntraScraper struct {
	base *baseScraper
}

// NewMyntraScraper constructs a fresh Myntra scraper. The returned value
// implements scrapers.Scraper structurally.
func NewMyntraScraper() *MyntraScraper {
	return &MyntraScraper{
		base: newBaseScraper(),
	}
}

// CanScrape reports whether the given URL is a Myntra URL. This is also
// used by the API layer to decide between this package and the generic
// scrapers.GetScraper factory.
func (s *MyntraScraper) CanScrape(url string) bool {
	return IsMyntraURL(url)
}

// IsMyntraURL is the package-level helper used by the API router to pick
// between this scraper and the generic factory before constructing a
// scraper instance.
func IsMyntraURL(url string) bool {
	return strings.Contains(url, "myntra.com")
}

// myxJSONRegex captures the JSON object assigned to window.__myx (NOT
// window.__myx_seo__ / window.__myx_deviceType__). The negative lookahead-ish
// trick is done by requiring a space (or =) directly after `myx`, since Go's
// regexp doesn't support lookarounds. The body is matched non-greedy and we
// anchor on `</script>` to avoid grabbing content from subsequent scripts.
var myxJSONRegex = regexp.MustCompile(`(?s)window\.__myx\s*=\s*(\{.*?\})\s*;?\s*</script>`)

// imgPathRegex captures the meaningful part of any Myntra asset URL, i.e.
// `v1/assets/images/<...>`. The CDN params before it (e.g. `h_($height),...`)
// can be safely stripped and replaced with real dimensions.
var imgPathRegex = regexp.MustCompile(`v\d+/assets/images/[^"'\s)]+`)

// myntraProductIDRegex matches a Myntra PDP URL and captures the numeric
// product id. Myntra PDP URLs always have a digit-run of >=5 chars somewhere
// in the path, e.g. `/jeggings/sassafras/.../10308613/buy` or `/31638495?...`.
// Anything without that (e.g. `/men-tshirts`, `/dresses`, `/p/foo`) is a
// category / listing / static page and not a product detail page. Without this
// check, Myntra still serves a 200 with an og:title + og:image on those URLs,
// which previously caused the scraper to "succeed" with the brand logo as the
// product image and a generic "Buy Latest ... Online" title.
var myntraProductIDRegex = regexp.MustCompile(`myntra\.com/(?:[^?#]*/)?(\d{5,})(?:/buy)?(?:[/?#]|$)`)

// myntraLogoMarker is the path of Myntra's brand-logo asset that gets returned
// as og:image on category / listing / homepage URLs. We never want to treat
// that as a product image.
const myntraLogoMarker = "constant.myntassets.com/www/data/portal/mlogo"

// extractMyntraProductID returns the numeric Myntra product id from a PDP
// URL, or "" if the URL is not a PDP.
func extractMyntraProductID(rawURL string) string {
	m := myntraProductIDRegex.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// normalizeMyntraURL strips Myntra's social-share `/mailers/...` prefix and
// drops tracking query params from share deeplinks. The clean PDP URL is
// less likely to be routed through Myntra's "share landing" path (which
// occasionally serves a stripped HTML body) and is also what `og:url` on
// the real PDP points to, so it gives us the most consistent response.
//
// /mailers/<slug>/<id>/buy?utm_source=...   -> /<slug>/<id>/buy
// /<slug>/<id>/buy?utm_source=...          -> /<slug>/<id>/buy
func normalizeMyntraURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Host == "" || !strings.Contains(u.Host, "myntra.com") {
		return rawURL
	}
	if strings.HasPrefix(u.Path, "/mailers/") {
		u.Path = strings.TrimPrefix(u.Path, "/mailers")
	}
	// Keep render-affecting params, drop tracking ones. The whitelist is
	// safer than a blacklist because Myntra periodically introduces new
	// utm-style flags.
	keep := map[string]bool{
		"storeContext": true,
		"size":         true,
		"variant":      true,
	}
	q := u.Query()
	for k := range q {
		if !keep[k] {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *MyntraScraper) ScrapeProduct(rawURL string) (*models.Product, error) {
	canonURL := normalizeMyntraURL(rawURL)
	if canonURL != rawURL {
		fmt.Printf("[MyntraScraper] canonicalised URL: %s -> %s\n", rawURL, canonURL)
	}
	if extractMyntraProductID(canonURL) == "" {
		return nil, fmt.Errorf("myntra: url is not a product page (no product id found in path): %s", canonURL)
	}

	doc, err := s.base.FetchDocument(canonURL, validateMyntraDoc)
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	html, _ := doc.Html()

	// Primary path: extract the inline window.__myx JSON, which carries the
	// full PDP payload (name, price, all album images, description, etc.).
	if jsonStr := extractMyxJSON(html); jsonStr != "" {
		if err := populateFromMyxJSON(product, jsonStr); err != nil {
			fmt.Printf("[MyntraScraper] window.__myx JSON parse failed: %v (len=%d)\n", err, len(jsonStr))
		}
	} else {
		fmt.Printf("[MyntraScraper] window.__myx JSON not found in HTML (len=%d)\n", len(html))
	}

	// Fallback: pull whatever we can from OG / Twitter / <title> tags. These
	// are present even on Myntra's bot-challenge / stripped responses, so they
	// keep the scrape useful instead of failing the whole request.
	applyMetaFallbacks(doc, product)

	// Last resort: scan the HTML for any product image URLs we missed (e.g.
	// when the JSON shape changes but image paths are still embedded).
	if len(product.Images) == 0 {
		product.Images = extractImagesFromHTML(html)
	}

	product.Images = normalizeMyntraImages(product.Images)

	if product.Title == "" {
		return nil, fmt.Errorf("failed to extract product details (title is empty)")
	}

	return product, nil
}

// validateMyntraDoc tightens the "did we actually get the PDP" check. The old
// version returned true on the mere substring `window.__myx`, which also
// matches `window.__myx_deviceType__` and `window.__myx_seo__`. Bot-challenge
// pages occasionally include those globals without `pdpData`, which would
// short-circuit ChromeDP/Selenium fallbacks for nothing.
func validateMyntraDoc(doc *goquery.Document) bool {
	text := doc.Text()
	if strings.Contains(text, "window.__myx =") && strings.Contains(text, "pdpData") {
		return true
	}
	// OG title + image is enough to build a usable product, so accept it
	// rather than spinning up ChromeDP for a page that already has the data.
	ogTitle, _ := doc.Find(`meta[property="og:title"]`).Attr("content")
	ogImage, _ := doc.Find(`meta[property="og:image"]`).Attr("content")
	return ogTitle != "" && ogImage != ""
}

func extractMyxJSON(html string) string {
	match := myxJSONRegex.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func populateFromMyxJSON(product *models.Product, jsonStr string) error {
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &root); err != nil {
		return err
	}
	pd, ok := root["pdpData"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("pdpData missing from window.__myx payload")
	}

	product.Title = getString(pd, "name")
	if product.Title == "" {
		product.Title = getString(pd, "title")
	}

	product.MRP = formatPriceWithCurrency(extractPrice(pd["mrp"]))
	product.DiscountedPrice = formatPriceWithCurrency(extractPrice(pd["price"]))

	if d := getString(pd, "discountDisplayLabel"); d != "" {
		product.Discount = d
	}

	product.Description = getString(pd, "productDetails")
	if product.Description == "" {
		product.Description = getString(pd, "description")
	}

	if media, ok := pd["media"].(map[string]interface{}); ok {
		if albums, ok := media["albums"].([]interface{}); ok {
			for _, album := range albums {
				albumMap, ok := album.(map[string]interface{})
				if !ok {
					continue
				}
				// Skip the `animatedImage` album (mp4-ish thumbnails) - those
				// aren't usable as product images.
				if getString(albumMap, "name") == "animatedImage" {
					continue
				}
				images, ok := albumMap["images"].([]interface{})
				if !ok {
					continue
				}
				for _, img := range images {
					imgMap, ok := img.(map[string]interface{})
					if !ok {
						continue
					}
					if src := getString(imgMap, "src"); src != "" {
						product.Images = append(product.Images, src)
					}
					if src := getString(imgMap, "secondaryImage"); src != "" {
						product.Images = append(product.Images, src)
					}
				}
			}
		}
	}

	return nil
}

func extractPrice(val interface{}) string {
	switch v := val.(type) {
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("%.0f", v)
	case string:
		return v
	case map[string]interface{}:
		if d, ok := v["discounted"]; ok {
			return fmt.Sprintf("%v", d)
		}
		if m, ok := v["mrp"]; ok {
			return fmt.Sprintf("%v", m)
		}
		if p, ok := v["price"]; ok {
			return fmt.Sprintf("%v", p)
		}
	}
	return ""
}

func formatPriceWithCurrency(price string) string {
	price = strings.TrimSpace(price)
	if price == "" || strings.Contains(price, "Rs") || strings.Contains(price, "₹") {
		return price
	}
	return "Rs. " + price
}

func applyMetaFallbacks(doc *goquery.Document, product *models.Product) {
	if product.Title == "" {
		if ogTitle, ok := doc.Find(`meta[property="og:title"]`).Attr("content"); ok {
			product.Title = strings.TrimSpace(ogTitle)
		}
	}
	if product.Title == "" {
		if t, ok := doc.Find(`meta[name="twitter:title"]`).Attr("content"); ok {
			product.Title = strings.TrimSpace(t)
		}
	}
	if product.Title == "" {
		product.Title = strings.TrimSpace(doc.Find("title").Text())
		product.Title = strings.TrimSuffix(product.Title, " | Myntra")
	}

	if product.Description == "" {
		if d, ok := doc.Find(`meta[property="og:description"]`).Attr("content"); ok {
			product.Description = strings.TrimSpace(d)
		}
	}
	if product.Description == "" {
		if d, ok := doc.Find(`meta[name="description"]`).Attr("content"); ok {
			product.Description = strings.TrimSpace(d)
		}
	}

	if len(product.Images) == 0 {
		if img, ok := doc.Find(`meta[property="og:image"]`).Attr("content"); ok && img != "" {
			product.Images = append(product.Images, img)
		}
		if img, ok := doc.Find(`meta[name="twitter:image"]`).Attr("content"); ok && img != "" {
			product.Images = append(product.Images, img)
		}
	}
}

func extractImagesFromHTML(html string) []string {
	matches := imgPathRegex.FindAllString(html, -1)
	seen := make(map[string]bool, len(matches))
	var out []string
	for _, m := range matches {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, "https://assets.myntassets.com/h_1440,q_100,w_1080/"+m)
	}
	return out
}

// normalizeMyntraImages does three things that the prior versions skipped and
// which broke / polluted Myntra scrapes:
//
//  1. Replaces the `($height)` / `($width)` / `($qualityPercentage)` template
//     placeholders that Myntra ships in `media.albums[].images[].src`. Without
//     this, every image URL we returned was an unrenderable string like
//     `.../h_($height),q_($qualityPercentage),w_($width)/v1/...`.
//  2. Upgrades `http://` to `https://` (clients on mobile reject mixed content)
//     and drops thumbnail/c_fill chains so we always serve the high-res frame.
//  3. Drops Myntra's brand-logo asset (`constant.myntassets.com/.../mlogo.png`)
//     because it appears as `og:image` on listing / category / 404 pages and
//     would otherwise pose as a product image in the wardrobe.
func normalizeMyntraImages(images []string) []string {
	if len(images) == 0 {
		return images
	}
	const cdnPrefix = "https://assets.myntassets.com/h_1440,q_100,w_1080/"

	seen := make(map[string]bool, len(images))
	out := make([]string, 0, len(images))
	for _, src := range images {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		if strings.Contains(src, myntraLogoMarker) {
			continue
		}
		var normalized string
		// Prefer the canonical `v<N>/assets/images/...` suffix so we strip
		// any chained CDN transformations (e.g. the og:image thumbnail).
		if m := imgPathRegex.FindString(src); m != "" {
			normalized = cdnPrefix + m
		} else {
			// Unknown shape (non-myntassets host etc.). Keep as-is but at
			// least upgrade the scheme.
			normalized = strings.Replace(src, "http://", "https://", 1)
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
