package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
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

	c := buildAppConfig(time.Unix(0, 0))
	if c.Schema != 1 || c.LinkImport.Mode != "device" || c.ServerScrape.Mode != "deprecated" || c.ServerScrape.Sunset != "2026-11-30" {
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
