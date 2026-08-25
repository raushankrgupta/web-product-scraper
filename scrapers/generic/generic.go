// Package generic implements a last-resort product scraper that works on any
// site emitting standard product metadata.
//
// Seven of the nine scrape attempts in the production log died at routing
// with "no scraper found for url" — meesho.com, thehouseofrare.com and
// sharein.savana.com. Writing a bespoke adapter per domain does not scale
// against what users actually paste. Almost every commerce site publishes the
// same three things for Google and for social previews:
//
//  1. JSON-LD  <script type="application/ld+json"> with "@type":"Product"
//  2. OpenGraph og:title / og:image / og:price:amount
//  3. Twitter card twitter:title / twitter:image
//
// This scraper reads them in that order and falls back to <title> plus the
// largest image on the page. Registered LAST in the factory, so every
// site-specific adapter still wins.
package generic

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/scrapers/base"
)

type GenericScraper struct {
	*base.BaseScraper
}

func NewGenericScraper() *GenericScraper {
	return &GenericScraper{BaseScraper: base.NewBaseScraper()}
}

// CanScrape always returns true — this is the fallback. The factory relies on
// registration order to keep it last.
func (s *GenericScraper) CanScrape(string) bool { return true }

func (s *GenericScraper) ScrapeProduct(pageURL string) (*models.Product, error) {
	doc, err := s.FetchDocument(pageURL, func(d *goquery.Document) bool {
		// Accept the page as soon as it carries *any* product signal.
		return hasProductSignal(d)
	})
	if err != nil {
		return nil, err
	}

	product := &models.Product{}

	applyJSONLD(doc, product, pageURL)
	applyOpenGraph(doc, product, pageURL)
	applyTwitterCard(doc, product, pageURL)
	applyFallbacks(doc, product, pageURL)

	product.Images = dedupeStrings(product.Images)

	if product.Title == "" && len(product.Images) == 0 {
		return nil, fmt.Errorf("no product metadata found on %s", pageURL)
	}
	return product, nil
}

func hasProductSignal(d *goquery.Document) bool {
	if d.Find(`script[type="application/ld+json"]`).Length() > 0 {
		return true
	}
	if d.Find(`meta[property="og:title"], meta[property="og:image"]`).Length() > 0 {
		return true
	}
	return strings.TrimSpace(d.Find("title").Text()) != ""
}

// ---------------------------------------------------------------- JSON-LD

// ldProduct mirrors the subset of schema.org/Product we care about. Fields
// are json.RawMessage where the spec allows several shapes (a string, an
// object, or an array of either) — commerce sites use all of them.
type ldProduct struct {
	Type        json.RawMessage `json:"@type"`
	Graph       []ldProduct     `json:"@graph"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Image       json.RawMessage `json:"image"`
	Brand       json.RawMessage `json:"brand"`
	Category    string          `json:"category"`
	Material    string          `json:"material"`
	Color       string          `json:"color"`
	Offers      json.RawMessage `json:"offers"`
}

type ldOffer struct {
	Price         json.RawMessage `json:"price"`
	PriceCurrency string          `json:"priceCurrency"`
	HighPrice     json.RawMessage `json:"highPrice"`
	LowPrice      json.RawMessage `json:"lowPrice"`
}

func applyJSONLD(doc *goquery.Document, p *models.Product, pageURL string) {
	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		raw := strings.TrimSpace(sel.Text())
		if raw == "" {
			return true
		}
		for _, cand := range flattenLD(raw) {
			if !isProductType(cand.Type) {
				continue
			}
			fillFromLD(cand, p, pageURL)
			// Keep going only if we still lack a title.
			return p.Title == ""
		}
		return true
	})
}

// flattenLD decodes a JSON-LD blob that may be a single object, an array, or
// an @graph wrapper, and returns every node in it.
func flattenLD(raw string) []ldProduct {
	var out []ldProduct

	var one ldProduct
	if err := json.Unmarshal([]byte(raw), &one); err == nil {
		out = append(out, one)
		out = append(out, one.Graph...)
		return out
	}

	var many []ldProduct
	if err := json.Unmarshal([]byte(raw), &many); err == nil {
		for _, m := range many {
			out = append(out, m)
			out = append(out, m.Graph...)
		}
	}
	return out
}

func isProductType(raw json.RawMessage) bool {
	for _, t := range stringsFromJSON(raw) {
		if strings.EqualFold(t, "Product") || strings.EqualFold(t, "ProductGroup") {
			return true
		}
	}
	return false
}

func fillFromLD(l ldProduct, p *models.Product, pageURL string) {
	if p.Title == "" {
		p.Title = strings.TrimSpace(l.Name)
	}
	if p.Description == "" {
		p.Description = strings.TrimSpace(l.Description)
	}
	if p.Category == "" {
		p.Category = strings.TrimSpace(l.Category)
	}
	if p.Material == "" {
		p.Material = strings.TrimSpace(l.Material)
	}

	for _, img := range stringsFromJSON(l.Image) {
		if abs := absURL(pageURL, img); abs != "" {
			p.Images = append(p.Images, abs)
		}
	}

	// offers can be an object or an array of objects.
	for _, offerRaw := range objectsFromJSON(l.Offers) {
		var o ldOffer
		if json.Unmarshal(offerRaw, &o) != nil {
			continue
		}
		if p.DiscountedPrice == "" {
			if v := numberFromJSON(o.Price); v != "" {
				p.DiscountedPrice = v
			} else if v := numberFromJSON(o.LowPrice); v != "" {
				p.DiscountedPrice = v
			}
		}
		if p.MRP == "" {
			if v := numberFromJSON(o.HighPrice); v != "" {
				p.MRP = v
			}
		}
	}
}

// --------------------------------------------------------------- OpenGraph

func applyOpenGraph(doc *goquery.Document, p *models.Product, pageURL string) {
	meta := func(prop string) string {
		v, _ := doc.Find(fmt.Sprintf(`meta[property=%q]`, prop)).First().Attr("content")
		if v == "" {
			v, _ = doc.Find(fmt.Sprintf(`meta[name=%q]`, prop)).First().Attr("content")
		}
		return strings.TrimSpace(v)
	}

	if p.Title == "" {
		p.Title = meta("og:title")
	}
	if p.Description == "" {
		p.Description = meta("og:description")
	}
	if p.DiscountedPrice == "" {
		for _, key := range []string{"product:price:amount", "og:price:amount"} {
			if v := meta(key); v != "" {
				p.DiscountedPrice = v
				break
			}
		}
	}
	// og:image repeats for galleries.
	doc.Find(`meta[property="og:image"], meta[property="og:image:secure_url"]`).Each(func(_ int, sel *goquery.Selection) {
		if v, ok := sel.Attr("content"); ok {
			if abs := absURL(pageURL, strings.TrimSpace(v)); abs != "" {
				p.Images = append(p.Images, abs)
			}
		}
	})
}

func applyTwitterCard(doc *goquery.Document, p *models.Product, pageURL string) {
	meta := func(name string) string {
		v, _ := doc.Find(fmt.Sprintf(`meta[name=%q]`, name)).First().Attr("content")
		return strings.TrimSpace(v)
	}
	if p.Title == "" {
		p.Title = meta("twitter:title")
	}
	if p.Description == "" {
		p.Description = meta("twitter:description")
	}
	if img := meta("twitter:image"); img != "" {
		if abs := absURL(pageURL, img); abs != "" {
			p.Images = append(p.Images, abs)
		}
	}
}

// ---------------------------------------------------------------- fallbacks

func applyFallbacks(doc *goquery.Document, p *models.Product, pageURL string) {
	if p.Title == "" {
		title := strings.TrimSpace(doc.Find("title").Text())
		// Page titles are usually "Product Name | Brand" or "… - Buy Online".
		for _, sep := range []string{" | ", " – ", " — ", " - "} {
			if i := strings.Index(title, sep); i > 0 {
				title = title[:i]
				break
			}
		}
		p.Title = strings.TrimSpace(title)
	}

	if len(p.Images) > 0 {
		return
	}

	// Last resort: the biggest declared <img> on the page. Product photos are
	// the largest images on a PDP; icons and logos are not.
	type scored struct {
		src  string
		area int
	}
	var best []scored
	doc.Find("img").Each(func(_ int, sel *goquery.Selection) {
		src := firstAttr(sel, "src", "data-src", "data-original", "data-lazy-src")
		if src == "" || strings.HasPrefix(src, "data:") {
			return
		}
		w, _ := strconv.Atoi(strings.TrimSuffix(sel.AttrOr("width", "0"), "px"))
		h, _ := strconv.Atoi(strings.TrimSuffix(sel.AttrOr("height", "0"), "px"))
		best = append(best, scored{src: src, area: w * h})
	})
	// Keep at most 5, largest first, preserving document order among equals.
	for i := 0; i < len(best) && len(p.Images) < 5; i++ {
		maxIdx := i
		for j := i + 1; j < len(best); j++ {
			if best[j].area > best[maxIdx].area {
				maxIdx = j
			}
		}
		best[i], best[maxIdx] = best[maxIdx], best[i]
		if abs := absURL(pageURL, best[i].src); abs != "" {
			p.Images = append(p.Images, abs)
		}
	}
}

// ------------------------------------------------------------------ helpers

func firstAttr(sel *goquery.Selection, names ...string) string {
	for _, n := range names {
		if v, ok := sel.Attr(n); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// stringsFromJSON extracts strings from a value that may be a string, an
// array of strings, an object with a "url"/"name" key, or an array of those.
func stringsFromJSON(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}

	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var out []string
		for _, item := range arr {
			out = append(out, stringsFromJSON(item)...)
		}
		return out
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for _, key := range []string{"url", "contentUrl", "name"} {
			if v, ok := obj[key]; ok {
				return stringsFromJSON(v)
			}
		}
	}
	return nil
}

// objectsFromJSON normalises "object or array of objects" into a slice.
func objectsFromJSON(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		return []json.RawMessage{raw}
	}
	return nil
}

// numberFromJSON reads a price that may be a JSON number or a JSON string.
func numberFromJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

// absURL resolves a possibly protocol-relative or root-relative image src
// against the page it came from.
func absURL(pageURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "//") {
		return "https:" + ref
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return base.ResolveReference(rel).String()
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
