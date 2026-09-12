package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
)

func TestBuildAppConfig_Shape(t *testing.T) {
	config.LinkImportMode = "device"
	config.ServerScrapeMode = "deprecated"
	config.ServerScrapeSunset = "2026-11-30"
	config.LinkImportBlockedHosts = nil
	config.LinkImportMaxImages = 6
	config.LinkImportMaxCandidates = 60
	config.LinkImportNoticeVersion = 1
	config.MinAppVersion = "2.3.4"

	c := buildAppConfig(time.Unix(0, 0), models.DefaultAppUpdateSettings())
	if c.Schema != 2 || c.LinkImport.Mode != "device" || c.ServerScrape.Mode != "deprecated" || c.ServerScrape.Sunset != "2026-11-30" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.LinkImport.BlockedHosts == nil {
		t.Fatal("blocked_hosts must be an empty array, not null")
	}
	b, _ := json.Marshal(c)
	if !json.Valid(b) {
		t.Fatal("not valid json")
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	li := m["link_import"].(map[string]any)
	for _, k := range []string{"mode", "max_images", "max_candidates", "min_image_px", "upload_max_edge", "jpeg_quality", "blocked_hosts", "notice_version"} {
		if _, ok := li[k]; !ok {
			t.Errorf("link_import.%s missing", k)
		}
	}
}

func TestAppConfigHandler_ETagAndCache(t *testing.T) {
	config.LinkImportMode = "device"
	rr := httptest.NewRecorder()
	AppConfigHandler(rr, httptest.NewRequest(http.MethodGet, "/app/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", cc)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequest(http.MethodGet, "/app/config", nil)
	req.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	AppConfigHandler(rr2, req)
	if rr2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 with matching ETag, got %d", rr2.Code)
	}
}

func TestBuildAppConfig_AppUpdateDefaultsToOff(t *testing.T) {
	c := buildAppConfig(time.Unix(0, 0), models.DefaultAppUpdateSettings())
	if c.AppUpdate.Mode != models.UpdateModeOff {
		t.Fatalf("default update mode = %q, want off", c.AppUpdate.Mode)
	}
	if c.AppUpdate.AndroidStoreURL == "" {
		t.Error("android_store_url must always be populated: a prompt with nowhere to send the user is worse than no prompt")
	}

	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	au, ok := m["app_update"].(map[string]any)
	if !ok {
		t.Fatal("app_update block missing from the response")
	}
	for _, k := range []string{
		"mode", "latest_version", "min_supported_version", "android_store_url",
		"title", "message", "cta_label", "dismiss_label", "remind_after_hours", "revision",
	} {
		if _, ok := au[k]; !ok {
			t.Errorf("app_update.%s missing", k)
		}
	}
}

func TestSanitizeAppUpdate(t *testing.T) {
	t.Run("unknown mode resolves to off", func(t *testing.T) {
		got := sanitizeAppUpdate(models.AppUpdateSettings{Mode: "FORCE-ish"})
		if got.Mode != models.UpdateModeOff {
			t.Fatalf("mode = %q, want off", got.Mode)
		}
	})

	t.Run("mode is case and whitespace tolerant", func(t *testing.T) {
		got := sanitizeAppUpdate(models.AppUpdateSettings{Mode: "  Force "})
		if got.Mode != models.UpdateModeForce {
			t.Fatalf("mode = %q, want force", got.Mode)
		}
	})

	t.Run("floor is clamped to what is actually on the store", func(t *testing.T) {
		// A min_supported newer than latest would force every user to update
		// to a version that does not exist yet — an unopenable app.
		got := sanitizeAppUpdate(models.AppUpdateSettings{
			Mode:                models.UpdateModeForce,
			LatestVersion:       "2.6.0",
			MinSupportedVersion: "9.9.9",
		})
		if got.MinSupportedVersion != "2.6.0" {
			t.Fatalf("min_supported_version = %q, want clamped to 2.6.0", got.MinSupportedVersion)
		}
	})

	t.Run("highlights are trimmed and capped", func(t *testing.T) {
		got := sanitizeAppUpdate(models.AppUpdateSettings{
			Mode:       models.UpdateModeSoft,
			Highlights: []string{" one ", "", "two", "three", "four", "five", "six"},
		})
		if len(got.Highlights) != 5 {
			t.Fatalf("highlights = %d, want 5", len(got.Highlights))
		}
		if got.Highlights[0] != "one" {
			t.Errorf("highlights[0] = %q, want trimmed", got.Highlights[0])
		}
	})

	t.Run("nonsense remind window falls back", func(t *testing.T) {
		got := sanitizeAppUpdate(models.AppUpdateSettings{Mode: models.UpdateModeSoft, RemindAfterHours: -3})
		if got.RemindAfterHours != 24 {
			t.Fatalf("remind_after_hours = %d, want 24", got.RemindAfterHours)
		}
	})
}
