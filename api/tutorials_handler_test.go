package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
)

func loadFeedFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/tutorials_feed.xml")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return data
}

// The fixture is the live feed as of 2026-09-07: one Short.
func TestParseTutorialFeed(t *testing.T) {
	title, videos, err := parseTutorialFeed(loadFeedFixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if title != "TryOnFusion: Virtual Try-On" {
		t.Errorf("title = %q", title)
	}
	if len(videos) != 1 {
		t.Fatalf("videos = %d, want 1", len(videos))
	}
	v := videos[0]
	if v.ID != "kQAttI5WVzw" || v.Title != "TryOnFusion: Demo" {
		t.Errorf("video = %+v", v)
	}
	if !v.IsShort || !strings.Contains(v.URL, "/shorts/") {
		t.Errorf("shorts not detected from alternate link: %+v", v)
	}
	if !strings.HasPrefix(v.EmbedURL, "https://www.youtube-nocookie.com/embed/kQAttI5WVzw") {
		t.Errorf("embed url = %q", v.EmbedURL)
	}
	if v.ThumbnailURL == "" || v.Views != 11 || v.PublishedAt.IsZero() {
		t.Errorf("metadata missing: %+v", v)
	}
}

func TestParseTutorialFeed_Hidden(t *testing.T) {
	_, videos, err := parseTutorialFeed(loadFeedFixture(t), map[string]bool{"kQAttI5WVzw": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 0 {
		t.Errorf("hidden video still listed")
	}
}

func TestParseTutorialFeed_Garbage(t *testing.T) {
	if _, _, err := parseTutorialFeed([]byte("<html>not a feed"), nil); err == nil {
		t.Error("expected parse error")
	}
}

// A failed refresh must serve the previous copy, marked stale, not a 503.
func TestTutorialsHandler_ServesStaleOnFailure(t *testing.T) {
	orig := fetchTutorialFeed
	defer func() { fetchTutorialFeed = orig; tutorials = &tutorialsCache{} }()
	tutorials = &tutorialsCache{}
	config.TutorialsCacheSecs = 0 // force a refresh on every call
	config.YouTubeChannelID = "UCtest"
	config.YouTubeChannelHandle = "@test"

	fixture := loadFeedFixture(t)
	fetchTutorialFeed = func(context.Context, string) ([]byte, error) { return fixture, nil }
	rr := httptest.NewRecorder()
	TutorialsHandler(rr, httptest.NewRequest(http.MethodGet, "/tutorials", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("first fetch status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"stale":false`) {
		t.Errorf("fresh response marked stale: %s", rr.Body.String())
	}
	if rr.Header().Get("ETag") == "" || rr.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("headers = %v", rr.Header())
	}

	fetchTutorialFeed = func(context.Context, string) ([]byte, error) { return nil, errors.New("youtube down") }
	tutorials.fetchedAt = time.Time{} // expire
	rr2 := httptest.NewRecorder()
	TutorialsHandler(rr2, httptest.NewRequest(http.MethodGet, "/tutorials", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("stale fetch status %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), `"stale":true`) || !strings.Contains(rr2.Body.String(), "kQAttI5WVzw") {
		t.Errorf("stale copy not served: %s", rr2.Body.String())
	}
}

func TestTutorialsHandler_503WhenNeverFetched(t *testing.T) {
	orig := fetchTutorialFeed
	defer func() { fetchTutorialFeed = orig; tutorials = &tutorialsCache{} }()
	tutorials = &tutorialsCache{}
	fetchTutorialFeed = func(context.Context, string) ([]byte, error) { return nil, errors.New("down") }
	rr := httptest.NewRecorder()
	TutorialsHandler(rr, httptest.NewRequest(http.MethodGet, "/tutorials", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}
