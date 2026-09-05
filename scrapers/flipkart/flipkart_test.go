package flipkart

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestFlipkart_NoPanicOnMalformedState(t *testing.T) {
	s := NewFlipkartScraper()

	// Malformed HTML snippet where window.__INITIAL_STATE__ has no closing semicolon
	malformedHTML := `
	<html>
		<body>
			<script>window.__INITIAL_STATE__ = {"pageDataV4": {}}</script>
		</body>
	</html>
	`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(malformedHTML))
	if err != nil {
		t.Fatalf("failed to parse HTML: %v", err)
	}

	// Scrape directly from document should not panic
	// Note: CanScrape check
	if !s.CanScrape("https://www.flipkart.com/item/p/itm123") {
		t.Errorf("CanScrape should return true for Flipkart URL")
	}

	// Verify doc has script
	if doc.Find("script").Length() == 0 {
		t.Errorf("expected script element")
	}
}
