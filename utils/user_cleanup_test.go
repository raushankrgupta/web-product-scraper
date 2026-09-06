package utils

import "testing"

// Wardrobe images are shared: the same retailer photo backs every user who
// saved that product. Deleting one on an account closure would blank it out
// for everyone else, so the purge filters against an allow-list of prefixes
// that are genuinely single-user.
func TestIsPurgeableKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"person_images/9f1c-uuid.jpg", true},
		{"generated_images/abc123.png", true},
		{"product_uploads/uuid.webp", true},

		{"", false},
		{"   ", false},
		{"https://cdn.retailer.com/img/1.jpg", false},
		{"http://cdn.retailer.com/img/1.jpg", false},
		// Scraped product folders are shared between users.
		{"myntra_12345/0.jpg", false},
		{"flipkart_abc/1.jpg", false},
		// Guest uploads belong to no account.
		{"guest_uploads/person_1.jpg", false},
		// Prefix must anchor at the start.
		{"other/person_images/x.jpg", false},
	}

	for _, tt := range tests {
		if got := isPurgeableKey(tt.key); got != tt.want {
			t.Errorf("isPurgeableKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestDedupeStringsPreservesOrder(t *testing.T) {
	got := dedupeStrings([]string{"b", "a", "b", "c", "a"})
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupeStrings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeStrings = %v, want %v", got, want)
		}
	}
}
