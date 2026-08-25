// Package shopify scrapes any Shopify storefront through the platform's own
// product JSON endpoint.
//
// Every Shopify store exposes `<product-url>.json`, which returns clean,
// structured product data — title, body_html, vendor, variants (with prices
// and SKUs) and the full image list — with no HTML parsing, no headless
// browser and no anti-bot friction. That covers a very large share of Indian
// D2C fashion (thehouseofrare.com, the domain users pasted twice in the
// production log, is one of them).
package shopify

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
)

type ShopifyScraper struct {
	client *http.Client
}

func NewShopifyScraper() *ShopifyScraper {
	return &ShopifyScraper{
		client: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout: 10 * time.Second,
				TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

// CanScrape matches any URL with a Shopify-shaped product path. Detection is
// deliberately conservative — a false positive here would steal a URL from a
// site-specific adapter. Ordering in the factory also keeps this after the
// named retailers.
func (s *ShopifyScraper) CanScrape(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if !strings.Contains(u.Path, "/products/") {
		return false
	}
	// Known non-Shopify sites that also use /products/ in their paths.
	host := strings.ToLower(u.Host)
	for _, deny := range []string{"amazon.", "flipkart.", "myntra.", "tatacliq.", "meesho."} {
		if strings.Contains(host, deny) {
			return false
		}
	}
	return true
}

// productJSON is the subset of Shopify's product payload we use.
type productJSON struct {
	Product struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		BodyHTML    string `json:"body_html"`
		Vendor      string `json:"vendor"`
		ProductType string `json:"product_type"`
		Tags        any    `json:"tags"`
		Variants    []struct {
			ID              int64  `json:"id"`
			Title           string `json:"title"`
			SKU             string `json:"sku"`
			Price           string `json:"price"`
			CompareAtPrice  string `json:"compare_at_price"`
			Option1         string `json:"option1"`
			Option2         string `json:"option2"`
			FeaturedImageID *int64 `json:"featured_image_id"`
		} `json:"variants"`
		Images []struct {
			ID         int64   `json:"id"`
			Src        string  `json:"src"`
			VariantIDs []int64 `json:"variant_ids"`
		} `json:"images"`
	} `json:"product"`
}

func (s *ShopifyScraper) ScrapeProduct(rawURL string) (*models.Product, error) {
	jsonURL, variantID, err := productJSONURL(rawURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, jsonURL, nil)
	if err != nil {
		return nil, err
	}
	// Shopify serves the JSON to anything, but a browser UA avoids the odd
	// WAF rule on stores behind Cloudflare.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("shopify fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("shopify returned %d for %s", resp.StatusCode, jsonURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var pj productJSON
	if err := json.Unmarshal(body, &pj); err != nil {
		return nil, fmt.Errorf("shopify json decode failed: %w", err)
	}
	if pj.Product.Title == "" {
		return nil, fmt.Errorf("not a shopify product page: %s", rawURL)
	}

	p := &models.Product{
		Title:       pj.Product.Title,
		Description: stripHTML(pj.Product.BodyHTML),
		Category:    pj.Product.ProductType,
	}

	for _, img := range pj.Product.Images {
		if img.Src != "" {
			p.Images = append(p.Images, img.Src)
		}
	}

	imageForVariant := func(vid int64) []string {
		var out []string
		for _, img := range pj.Product.Images {
			for _, id := range img.VariantIDs {
				if id == vid && img.Src != "" {
					out = append(out, img.Src)
				}
			}
		}
		return out
	}

	for _, v := range pj.Product.Variants {
		variant := models.Variant{
			ASIN:   strconv.FormatInt(v.ID, 10),
			Size:   firstNonEmpty(v.Option2, v.Title),
			Color:  v.Option1,
			Images: imageForVariant(v.ID),
		}
		p.Variants = append(p.Variants, variant)

		// The ?variant= parameter selects the actual SKU the user was
		// looking at — this is why NormalizeProductURL deliberately keeps it.
		if variantID != "" && strconv.FormatInt(v.ID, 10) == variantID {
			sel := variant
			p.CurrentSelection = &sel
			p.DiscountedPrice = v.Price
			p.MRP = v.CompareAtPrice
		}
	}

	// No ?variant= in the URL: fall back to the first variant's pricing,
	// which is what the storefront shows by default.
	if p.DiscountedPrice == "" && len(pj.Product.Variants) > 0 {
		p.DiscountedPrice = pj.Product.Variants[0].Price
		p.MRP = pj.Product.Variants[0].CompareAtPrice
	}

	return p, nil
}

// productJSONURL turns a storefront product URL into its .json twin, keeping
// the variant id if one was present.
func productJSONURL(rawURL string) (jsonURL, variantID string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	variantID = u.Query().Get("variant")

	path := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(path, ".json") {
		jsonURL = u.Scheme + "://" + u.Host + path
		return jsonURL, variantID, nil
	}
	if !strings.Contains(path, "/products/") {
		return "", "", fmt.Errorf("not a shopify product path: %s", u.Path)
	}
	// Drop any /collections/<x> prefix — <host>/products/<handle>.json is
	// canonical and always resolves.
	i := strings.Index(path, "/products/")
	path = path[i:]

	return u.Scheme + "://" + u.Host + path + ".json", variantID, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" && v != "Default Title" {
			return v
		}
	}
	return ""
}

// stripHTML flattens Shopify's body_html into plain text. Good enough for a
// description field; we are not rendering it.
func stripHTML(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
				b.WriteRune(' ')
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.ReplaceAll(out, "&nbsp;", " ")
	out = strings.ReplaceAll(out, "&amp;", "&")
	return strings.TrimSpace(out)
}
