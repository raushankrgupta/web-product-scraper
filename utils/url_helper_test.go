package utils

import "testing"

func TestNormalizeProductURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			// The exact failure from the production log:
			// parse "\nhttps://...\n ": net/url: invalid control character in URL
			name: "leading newline and trailing whitespace",
			in:   "\nhttps://www.meesho.com/s/p/c4jdu0\n ",
			want: "https://www.meesho.com/s/p/c4jdu0",
		},
		{
			name: "strips utm parameters",
			in:   "https://www.meesho.com/s/p/an2fpi?utm_source=share&utm_medium=whatsapp&utm_campaign=x",
			want: "https://www.meesho.com/s/p/an2fpi",
		},
		{
			name: "strips gclid fbclid srsltid and gad_",
			in:   "https://example.com/p/1?gclid=a&fbclid=b&srsltid=c&gad_source=d",
			want: "https://example.com/p/1",
		},
		{
			// ?variant= selects the SKU on Shopify. Dropping it would
			// silently scrape the wrong colour or size.
			name: "preserves variant while stripping tracking",
			in:   "https://thehouseofrare.com/products/nega-mens-polo-dusky-pink?variant=4242&utm_source=ig",
			want: "https://thehouseofrare.com/products/nega-mens-polo-dusky-pink?variant=4242",
		},
		{
			name: "adds https to a scheme-less paste",
			in:   "www.meesho.com/s/p/abc",
			want: "https://www.meesho.com/s/p/abc",
		},
		{
			name: "takes the first token when a caption follows the link",
			in:   "https://example.com/p/1 check this out",
			want: "https://example.com/p/1",
		},
		{
			name: "drops the fragment",
			in:   "https://example.com/p/1#reviews",
			want: "https://example.com/p/1",
		},
		{
			name: "keeps non-tracking query params",
			in:   "https://example.com/p?color=red&size=M",
			want: "https://example.com/p?color=red&size=M",
		},
		{name: "empty input", in: "   ", wantErr: true},
		{name: "no host", in: "https://", wantErr: true},
		{name: "unsupported scheme", in: "javascript:alert(1)", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProductURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeProductURL(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeProductURL(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeProductURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The same product shared twice from different places must normalise to one
// URL — that is what stops it being scraped (and billed) twice.
func TestNormalizeProductURLDedupesTrackedVariants(t *testing.T) {
	a, err := NormalizeProductURL("https://www.meesho.com/s/p/an2fpi?utm_source=share_whatsapp")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NormalizeProductURL("\nhttps://www.meesho.com/s/p/an2fpi?utm_source=copy_link&utm_medium=x\n")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("expected identical normalisation, got %q and %q", a, b)
	}
}
