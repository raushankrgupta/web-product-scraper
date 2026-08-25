package generic

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/raushankrgupta/web-product-scraper/models"
)

// parse runs the extraction pipeline against a literal HTML document, which
// is what the network path would have handed FetchDocument.
func parse(t *testing.T, html, pageURL string) *models.Product {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse test html: %v", err)
	}
	p := &models.Product{}
	applyJSONLD(doc, p, pageURL)
	applyOpenGraph(doc, p, pageURL)
	applyTwitterCard(doc, p, pageURL)
	applyFallbacks(doc, p, pageURL)
	p.Images = dedupeStrings(p.Images)
	return p
}

// Shopify stores (thehouseofrare, pasted twice in the production log) emit
// complete JSON-LD. This one file is what turns that domain from
// "no scraper found" into a product.
func TestJSONLDProduct(t *testing.T) {
	html := `<html><head>
	<script type="application/ld+json">
	{
	  "@context": "https://schema.org",
	  "@type": "Product",
	  "name": "Nega Men's Polo Dusky Pink",
	  "description": "A soft cotton polo.",
	  "image": ["https://cdn.shopify.com/a.jpg", "https://cdn.shopify.com/b.jpg"],
	  "brand": {"@type": "Brand", "name": "Rare Rabbit"},
	  "offers": {"@type": "Offer", "price": "1799.00", "priceCurrency": "INR", "highPrice": "2499.00"}
	}
	</script></head><body></body></html>`

	p := parse(t, html, "https://thehouseofrare.com/products/nega-mens-polo-dusky-pink")

	if p.Title != "Nega Men's Polo Dusky Pink" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.DiscountedPrice != "1799.00" {
		t.Errorf("DiscountedPrice = %q, want 1799.00", p.DiscountedPrice)
	}
	if p.MRP != "2499.00" {
		t.Errorf("MRP = %q, want 2499.00", p.MRP)
	}
	if len(p.Images) != 2 {
		t.Errorf("Images = %v, want 2", p.Images)
	}
}

// Prices arrive as JSON numbers about as often as strings.
func TestJSONLDNumericPrice(t *testing.T) {
	html := `<html><head><script type="application/ld+json">
	{"@type":"Product","name":"Thing","offers":{"price":1499.5}}
	</script></head></html>`

	p := parse(t, html, "https://example.com/p")
	if p.DiscountedPrice != "1499.5" {
		t.Errorf("DiscountedPrice = %q, want 1499.5", p.DiscountedPrice)
	}
}

// Many sites wrap everything in an @graph and put Product alongside
// BreadcrumbList / Organization nodes.
func TestJSONLDGraphWrapper(t *testing.T) {
	html := `<html><head><script type="application/ld+json">
	{"@context":"https://schema.org","@graph":[
	  {"@type":"WebSite","name":"Some Store"},
	  {"@type":"Product","name":"Graph Product","image":"https://cdn.example.com/g.jpg",
	   "offers":[{"price":"999"}]}
	]}
	</script></head></html>`

	p := parse(t, html, "https://example.com/p")
	if p.Title != "Graph Product" {
		t.Errorf("Title = %q, want the Product node's name", p.Title)
	}
	if p.DiscountedPrice != "999" {
		t.Errorf("DiscountedPrice = %q", p.DiscountedPrice)
	}
}

// An array of top-level nodes is equally common.
func TestJSONLDArrayOfNodes(t *testing.T) {
	html := `<html><head><script type="application/ld+json">
	[{"@type":"Organization","name":"Store"},
	 {"@type":"Product","name":"Array Product","image":"https://cdn.example.com/a.jpg"}]
	</script></head></html>`

	p := parse(t, html, "https://example.com/p")
	if p.Title != "Array Product" {
		t.Errorf("Title = %q", p.Title)
	}
}

// Meesho-style SPAs typically have no JSON-LD but do have OpenGraph, because
// the share links have to render a preview.
func TestOpenGraphFallback(t *testing.T) {
	html := `<html><head>
	<meta property="og:title" content="Cotton Kurta Set">
	<meta property="og:description" content="Comfortable everyday wear">
	<meta property="og:image" content="//images.meesho.com/images/products/1/x.jpg">
	<meta property="product:price:amount" content="549">
	</head></html>`

	p := parse(t, html, "https://www.meesho.com/s/p/an2fpi")

	if p.Title != "Cotton Kurta Set" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.DiscountedPrice != "549" {
		t.Errorf("DiscountedPrice = %q", p.DiscountedPrice)
	}
	// Protocol-relative URLs must be absolutised or the image fetch fails.
	if len(p.Images) != 1 || !strings.HasPrefix(p.Images[0], "https://") {
		t.Errorf("Images = %v, want one absolute https URL", p.Images)
	}
}

func TestTwitterCardFallback(t *testing.T) {
	html := `<html><head>
	<meta name="twitter:title" content="Savana Dress">
	<meta name="twitter:image" content="/media/dress.jpg">
	</head></html>`

	p := parse(t, html, "https://sharein.savana.com/db/details/2216402")

	if p.Title != "Savana Dress" {
		t.Errorf("Title = %q", p.Title)
	}
	if len(p.Images) != 1 || p.Images[0] != "https://sharein.savana.com/media/dress.jpg" {
		t.Errorf("Images = %v, want the root-relative src resolved against the page", p.Images)
	}
}

// Last resort: page title plus the largest images.
func TestTitleAndLargestImageFallback(t *testing.T) {
	html := `<html><head><title>Blue Shirt | BrandCo</title></head><body>
	<img src="/logo.png" width="40" height="40">
	<img src="/hero.jpg" width="1200" height="1600">
	<img src="/thumb.jpg" width="80" height="80">
	</body></html>`

	p := parse(t, html, "https://shop.example.com/p/blue-shirt")

	if p.Title != "Blue Shirt" {
		t.Errorf("Title = %q, want the part before the separator", p.Title)
	}
	if len(p.Images) == 0 || p.Images[0] != "https://shop.example.com/hero.jpg" {
		t.Errorf("Images = %v, want the largest image first", p.Images)
	}
}

// data: URIs are inline placeholders, never product photos.
func TestFallbackSkipsDataURIs(t *testing.T) {
	html := `<html><head><title>X</title></head><body>
	<img src="data:image/gif;base64,R0lGOD" width="900" height="900">
	<img src="/real.jpg" width="100" height="100">
	</body></html>`

	p := parse(t, html, "https://example.com/p")
	for _, img := range p.Images {
		if strings.HasPrefix(img, "data:") {
			t.Errorf("a data: URI was collected as a product image: %s", img)
		}
	}
}

func TestJSONLDPrefersProductOverOtherTypes(t *testing.T) {
	html := `<html><head>
	<script type="application/ld+json">{"@type":"BreadcrumbList","name":"Home > Shirts"}</script>
	<script type="application/ld+json">{"@type":"Product","name":"Real Product"}</script>
	</head></html>`

	p := parse(t, html, "https://example.com/p")
	if p.Title != "Real Product" {
		t.Errorf("Title = %q, want the Product node", p.Title)
	}
}

func TestCanScrapeIsAlwaysTrue(t *testing.T) {
	s := NewGenericScraper()
	for _, u := range []string{"https://anything.example", "not-a-url", ""} {
		if !s.CanScrape(u) {
			t.Errorf("CanScrape(%q) = false; the fallback must accept everything", u)
		}
	}
}

func TestAbsURL(t *testing.T) {
	base := "https://shop.example.com/products/x"
	cases := map[string]string{
		"https://cdn.example.com/a.jpg": "https://cdn.example.com/a.jpg",
		"//cdn.example.com/b.jpg":       "https://cdn.example.com/b.jpg",
		"/c.jpg":                        "https://shop.example.com/c.jpg",
		"d.jpg":                         "https://shop.example.com/products/d.jpg",
		"":                              "",
	}
	for in, want := range cases {
		if got := absURL(base, in); got != want {
			t.Errorf("absURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "b", "a", "", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("dedupeStrings = %v, want [a b c]", got)
	}
}
