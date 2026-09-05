package utils

import (
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
)

func setupIdentity(t *testing.T) {
	t.Helper()
	if err := config.LoadStars(); err != nil {
		t.Fatalf("load star config: %v", err)
	}
	config.StarsIdentityPepper = "test-pepper"
}

func TestNormaliseEmailFoldsGmailAliases(t *testing.T) {
	setupIdentity(t)

	// Google routes all of these to one inbox. Treating them as separate
	// identities is what would turn the welcome bonus into a vending machine:
	// one Gmail account could mint unlimited "new users".
	same := []string{
		"rkgupta@gmail.com",
		"r.k.gupta@gmail.com",
		"RKGupta@Gmail.com",
		"rkgupta+tryon@gmail.com",
		"r.k.gupta+anything@googlemail.com",
		"  rkgupta@gmail.com  ",
	}
	want := NormaliseEmail(same[0])
	for _, in := range same[1:] {
		if got := NormaliseEmail(in); got != want {
			t.Errorf("NormaliseEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseEmailLeavesOtherProvidersAlone(t *testing.T) {
	setupIdentity(t)

	// Dot-insensitivity is a Gmail behaviour, not a universal one. Stripping
	// dots elsewhere would merge two genuinely different people into one
	// identity and deny a real user their welcome bonus.
	a := NormaliseEmail("first.last@outlook.com")
	b := NormaliseEmail("firstlast@outlook.com")
	if a == b {
		t.Error("outlook addresses differing only by a dot must stay distinct")
	}
	if a != "first.last@outlook.com" {
		t.Errorf("NormaliseEmail lowercased/trimmed only, got %q", a)
	}
}

func TestEmailIdentityHashIsStableAndPeppered(t *testing.T) {
	setupIdentity(t)

	h1 := EmailIdentityHash("rkgupta@gmail.com")
	h2 := EmailIdentityHash("r.k.gupta+x@gmail.com")
	if h1 != h2 {
		t.Error("aliases of one inbox must hash identically")
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(h1))
	}
	// The stored value must never be reversible to the address by anyone
	// without the pepper.
	config.StarsIdentityPepper = "a-different-pepper"
	if EmailIdentityHash("rkgupta@gmail.com") == h1 {
		t.Error("changing the pepper must change the hash")
	}
}

func TestEmailIdentityHashDistinguishesDifferentPeople(t *testing.T) {
	setupIdentity(t)

	if EmailIdentityHash("alice@gmail.com") == EmailIdentityHash("bob@gmail.com") {
		t.Error("different addresses must not collide")
	}
}

func TestNormaliseEmailHandlesMalformedInput(t *testing.T) {
	setupIdentity(t)

	// Signup validation should catch these, but a panic here would take down
	// account creation.
	for _, in := range []string{"", "not-an-email", "@nodomain.com", "nolocal@", "a@b@c"} {
		_ = NormaliseEmail(in)
		_ = EmailIdentityHash(in)
	}
}
