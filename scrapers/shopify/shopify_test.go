package shopify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanScrape(t *testing.T) {
	s := NewShopifyScraper()

	yes := []string{
		"https://thehouseofrare.com/products/nega-mens-polo-dusky-pink",
		"https://thehouseofrare.com/collections/polos/products/nega?variant=42",
		"https://somed2c.example/products/thing",
	}
	for _, u := range yes {
		if !s.CanScrape(u) {
			t.Errorf("CanScrape(%q) = false, want true", u)
		}
	}

	// Sites with their own adapter must never be stolen by this one.
	no := []string{
		"https://www.amazon.in/dp/B0XYZ",
		"https://www.myntra.com/shirts/cahoot/x/31472271/buy",
		"https://www.flipkart.com/p/itmabc",
		"https://www.meesho.com/s/p/an2fpi",
		"https://example.com/collections/all",
		"::not a url::",
	}
	for _, u := range no {
		if s.CanScrape(u) {
			t.Errorf("CanScrape(%q) = true, want false", u)
		}
	}
}

func TestProductJSONURL(t *testing.T) {
	cases := []struct {
		in        string
		wantURL   string
		wantVar   string
		wantError bool
	}{
		{
			in:      "https://thehouseofrare.com/products/nega-mens-polo",
			wantURL: "https://thehouseofrare.com/products/nega-mens-polo.json",
		},
		{
			// ?variant= is preserved by NormalizeProductURL precisely so it
			// can select the right SKU here.
			in:      "https://thehouseofrare.com/products/nega-mens-polo?variant=44556",
			wantURL: "https://thehouseofrare.com/products/nega-mens-polo.json",
			wantVar: "44556",
		},
		{
			// A /collections/ prefix is dropped — the bare product path is
			// canonical and always resolves.
			in:      "https://store.example/collections/polos/products/nega",
			wantURL: "https://store.example/products/nega.json",
		},
		{
			in:      "https://store.example/products/nega.json",
			wantURL: "https://store.example/products/nega.json",
		},
		{in: "https://store.example/about", wantError: true},
	}

	for _, c := range cases {
		gotURL, gotVar, err := productJSONURL(c.in)
		if c.wantError {
			if err == nil {
				t.Errorf("productJSONURL(%q) = %q, want error", c.in, gotURL)
			}
			continue
		}
		if err != nil {
			t.Errorf("productJSONURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if gotURL != c.wantURL {
			t.Errorf("productJSONURL(%q) url = %q, want %q", c.in, gotURL, c.wantURL)
		}
		if gotVar != c.wantVar {
			t.Errorf("productJSONURL(%q) variant = %q, want %q", c.in, gotVar, c.wantVar)
		}
	}
}

const sampleProduct = `{"product":{
  "id": 111,
  "title": "Nega Men's Polo",
  "body_html": "<p>Soft <b>cotton</b> polo.</p>",
  "vendor": "Rare Rabbit",
  "product_type": "Polo",
  "variants": [
    {"id": 4001, "title": "Pink / M", "sku": "NEGA-M", "price": "1799.00", "compare_at_price": "2499.00", "option1": "Pink", "option2": "M"},
    {"id": 4002, "title": "Pink / L", "sku": "NEGA-L", "price": "1899.00", "compare_at_price": "2599.00", "option1": "Pink", "option2": "L"}
  ],
  "images": [
    {"id": 1, "src": "https://cdn.shopify.com/a.jpg", "variant_ids": [4001]},
    {"id": 2, "src": "https://cdn.shopify.com/b.jpg", "variant_ids": [4002]}
  ]
}}`

// serve spins up a store that answers the .json endpoint.
func serve(t *testing.T, body string, status int) (*ShopifyScraper, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewShopifyScraper(), srv.URL
}

func TestScrapeProduct(t *testing.T) {
	s, base := serve(t, sampleProduct, http.StatusOK)

	p, err := s.ScrapeProduct(base + "/products/nega-mens-polo")
	if err != nil {
		t.Fatalf("ScrapeProduct: %v", err)
	}

	if p.Title != "Nega Men's Polo" {
		t.Errorf("Title = %q", p.Title)
	}
	// body_html must be flattened, not passed through as markup.
	if strings.Contains(p.Description, "<") {
		t.Errorf("Description still contains HTML: %q", p.Description)
	}
	if !strings.Contains(p.Description, "cotton") {
		t.Errorf("Description = %q, want the body text", p.Description)
	}
	if len(p.Images) != 2 {
		t.Errorf("Images = %v, want 2", p.Images)
	}
	if len(p.Variants) != 2 {
		t.Fatalf("Variants = %d, want 2", len(p.Variants))
	}
	// No ?variant= → first variant's pricing, matching the storefront default.
	if p.DiscountedPrice != "1799.00" || p.MRP != "2499.00" {
		t.Errorf("price = %q / %q, want 1799.00 / 2499.00", p.DiscountedPrice, p.MRP)
	}
}

func TestScrapeProductSelectsVariant(t *testing.T) {
	s, base := serve(t, sampleProduct, http.StatusOK)

	p, err := s.ScrapeProduct(base + "/products/nega-mens-polo?variant=4002")
	if err != nil {
		t.Fatalf("ScrapeProduct: %v", err)
	}

	if p.CurrentSelection == nil {
		t.Fatal("CurrentSelection is nil — ?variant= was ignored")
	}
	if p.CurrentSelection.Size != "L" {
		t.Errorf("selected size = %q, want L", p.CurrentSelection.Size)
	}
	if p.DiscountedPrice != "1899.00" {
		t.Errorf("DiscountedPrice = %q, want the selected variant's price", p.DiscountedPrice)
	}
	if len(p.CurrentSelection.Images) != 1 || p.CurrentSelection.Images[0] != "https://cdn.shopify.com/b.jpg" {
		t.Errorf("selected images = %v, want just the variant's image", p.CurrentSelection.Images)
	}
}

func TestScrapeProductNotShopify(t *testing.T) {
	s, base := serve(t, `{"errors":"Not Found"}`, http.StatusOK)

	if _, err := s.ScrapeProduct(base + "/products/missing"); err == nil {
		t.Fatal("expected an error when the payload has no product")
	}
}

func TestScrapeProductNon200(t *testing.T) {
	s, base := serve(t, "nope", http.StatusForbidden)

	if _, err := s.ScrapeProduct(base + "/products/x"); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestStripHTML(t *testing.T) {
	got := stripHTML("<p>Soft&nbsp;<b>cotton</b> polo</p>\n<ul><li>Slim fit</li></ul>")
	if strings.ContainsAny(got, "<>") {
		t.Errorf("stripHTML left markup: %q", got)
	}
	for _, want := range []string{"Soft", "cotton", "Slim fit"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripHTML dropped %q from: %q", want, got)
		}
	}
}
