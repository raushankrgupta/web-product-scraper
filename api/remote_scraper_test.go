package api

import (
	"context"
	"errors"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
)

func withServerB(t *testing.T, enabled bool, url string, fn func()) {
	t.Helper()
	oldEnabled, oldURL := config.ServerBEnabled, config.ServerBScrapeURL
	defer func() { config.ServerBEnabled, config.ServerBScrapeURL = oldEnabled, oldURL }()
	config.ServerBEnabled, config.ServerBScrapeURL = enabled, url
	fn()
}

// SERVER_B_ENABLED=false means "use this server only" — even for a Myntra URL,
// which is the one case the offload path exists for, and even with a URL still
// configured.
func TestDelegateToServerBRespectsFlag(t *testing.T) {
	const myntraURL = "https://www.myntra.com/shirts/roadster/roadster-men-blue-shirt/1234567/buy"

	withServerB(t, true, "https://b.example.com/internal/scrape", func() {
		if !delegateToServerB(myntraURL) {
			t.Fatal("enabled B should take a Myntra URL")
		}
	})

	withServerB(t, false, "https://b.example.com/internal/scrape", func() {
		if delegateToServerB(myntraURL) {
			t.Fatal("disabled B must not be delegated to, even with a URL configured")
		}
	})
}

// callServerB is the choke point: nothing may dial a stood-down B, and the
// refusal must be a plain error the caller can fall back from — not a dial
// against a hostname we already know is dead.
func TestCallServerBRefusesWhenDisabled(t *testing.T) {
	withServerB(t, false, "https://dead-tunnel.trycloudflare.com/internal/scrape", func() {
		resp, err := callServerB(context.Background(), "u1", "https://www.myntra.com/x/1/buy", false)
		if resp != nil {
			t.Fatal("expected no response from a disabled server B")
		}
		if !errors.Is(err, errServerBDisabled) {
			t.Fatalf("err = %v, want errServerBDisabled", err)
		}
	})
}

// A disabled B is a deliberate operating state, not an outage: /health must
// not report it as a failed dependency, because that is what makes a real
// failure invisible among the noise.
func TestDisabledServerBIsNotDegraded(t *testing.T) {
	withServerB(t, false, "https://dead-tunnel.trycloudflare.com/internal/scrape", func() {
		ok, reason := checkServerB(context.Background())
		if !ok {
			t.Fatalf("disabled B reported unhealthy: %s", reason)
		}
		if reason != "disabled (scraping locally)" {
			t.Errorf("reason = %q, want it to say the path is disabled", reason)
		}
	})

	withServerB(t, false, "", func() {
		ok, reason := checkServerB(context.Background())
		if !ok || reason != "not configured (scraping locally)" {
			t.Errorf("unconfigured B: ok=%v reason=%q", ok, reason)
		}
	})
}
