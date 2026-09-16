package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
)

func TestGetAppHandler_RoutesByDevice(t *testing.T) {
	InvalidateAppUpdateCache()
	prevID := config.AppleAppID
	config.AppleAppID = "6740000000"
	t.Cleanup(func() { config.AppleAppID = prevID; InvalidateAppUpdateCache() })

	get := func(ua, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/get"+query, nil)
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		GetAppHandler(rec, req)
		return rec
	}

	android := get("Mozilla/5.0 (Linux; Android 14; Pixel 8)", "?ref=abc2345")
	if android.Code != http.StatusFound {
		t.Fatalf("android: want 302, got %d", android.Code)
	}
	if loc := android.Header().Get("Location"); !strings.Contains(loc, "play.google.com") || !strings.Contains(loc, "referrer=ABC2345") {
		t.Fatalf("android must go to Play with the referrer, got %q", loc)
	}

	iphone := get("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)", "")
	if iphone.Code != http.StatusFound || iphone.Header().Get("Location") != "https://apps.apple.com/app/id6740000000" {
		t.Fatalf("iphone without a code must go straight to the App Store, got %d %q", iphone.Code, iphone.Header().Get("Location"))
	}

	// A code would be lost in the App Store redirect, so it is shown first.
	iphoneRef := get("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)", "?ref=abc2345")
	body := iphoneRef.Body.String()
	if iphoneRef.Code != http.StatusOK || !strings.Contains(body, "ABC2345") || !strings.Contains(body, "apps.apple.com") {
		t.Fatalf("iphone with a code must see it with an App Store button, got %d", iphoneRef.Code)
	}
	if strings.Contains(body, "Google Play") {
		t.Fatal("an iPhone visitor must not be offered Google Play")
	}

	desktop := get("Mozilla/5.0 (Windows NT 10.0)", "")
	if desktop.Code != http.StatusOK || !strings.Contains(desktop.Body.String(), "Google Play") {
		t.Fatalf("desktop gets the chooser page, got %d", desktop.Code)
	}
}

func TestSanitizeAppUpdate_IOSVersionsAreIndependent(t *testing.T) {
	out := sanitizeAppUpdate(models.AppUpdateSettings{
		Mode:                   models.UpdateModeForce,
		LatestVersion:          "2.6.0",
		MinSupportedVersion:    "2.6.0",
		IOSLatestVersion:       "2.5.1",
		IOSMinSupportedVersion: "2.9.0",
	})
	if out.IOSMinSupportedVersion != "2.5.1" {
		t.Fatalf("iOS floor above the iOS ceiling must be clamped, got %q", out.IOSMinSupportedVersion)
	}
	if out.MinSupportedVersion != "2.6.0" {
		t.Fatalf("android floor must be untouched, got %q", out.MinSupportedVersion)
	}

	floorOnly := sanitizeAppUpdate(models.AppUpdateSettings{
		Mode: models.UpdateModeForce, IOSMinSupportedVersion: "2.5.0",
	})
	if floorOnly.IOSMinSupportedVersion != "" {
		t.Fatal("an iOS floor without an iOS latest version could only lock users out")
	}
}
