package api

import (
	"strings"
	"testing"
	"time"
)

func formOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// A request identical to what the legacy (<= 2.3.4) app sends — only the
// files, none of the provenance fields — must produce exactly the record it
// always produced.
func TestParseImportMeta_LegacyDefaults(t *testing.T) {
	meta, err := parseImportMeta(formOf(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Source != "user_upload" || meta.Title != "User Uploaded Product" || meta.SourceURL != "" || meta.PageHost != "" {
		t.Fatalf("legacy defaults changed: %+v", meta)
	}
}

func TestParseImportMeta_LinkImport(t *testing.T) {
	meta, err := parseImportMeta(formOf(map[string]string{
		"source":        "link_import",
		"source_url":    "https://www.myntra.com/tshirts/x/p/123?utm_source=share&fbclid=abc#photo",
		"title":         "  Roadster Men\tSolid   Tee\x00 ",
		"import_method": "TAP",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Source != "link_import" {
		t.Errorf("source = %q", meta.Source)
	}
	if meta.SourceURL != "https://www.myntra.com/tshirts/x/p/123" {
		t.Errorf("tracking params/fragment not stripped: %q", meta.SourceURL)
	}
	if meta.PageHost != "myntra.com" {
		t.Errorf("page_host = %q", meta.PageHost)
	}
	if meta.Title != "Roadster Men Solid Tee" {
		t.Errorf("title not sanitised: %q", meta.Title)
	}
	if meta.ImportMethod != "tap" {
		t.Errorf("import_method = %q", meta.ImportMethod)
	}
}

func TestParseImportMeta_LinkImportRequiresURL(t *testing.T) {
	if _, err := parseImportMeta(formOf(map[string]string{"source": "link_import"})); err == nil {
		t.Fatal("expected error for missing source_url")
	}
	if _, err := parseImportMeta(formOf(map[string]string{
		"source": "link_import", "source_url": "javascript:alert(1)",
	})); err == nil {
		t.Fatal("expected error for javascript: scheme")
	}
}

func TestParseImportMeta_RejectsUnknownSource(t *testing.T) {
	if _, err := parseImportMeta(formOf(map[string]string{"source": "scraped_by_server"})); err == nil {
		t.Fatal("expected error for unknown source")
	}
}

// A source_url on a plain gallery upload is not provenance — it is ignored.
func TestParseImportMeta_UserUploadIgnoresURL(t *testing.T) {
	meta, err := parseImportMeta(formOf(map[string]string{
		"source": "user_upload", "source_url": "https://example.com/p/1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if meta.SourceURL != "" {
		t.Errorf("source_url stored on user_upload: %q", meta.SourceURL)
	}
}

func TestParseImportMeta_TitleTruncatedAndDefaulted(t *testing.T) {
	long := strings.Repeat("a", 500)
	meta, _ := parseImportMeta(formOf(map[string]string{
		"source": "link_import", "source_url": "https://shop.example/p", "title": long,
	}))
	if len([]rune(meta.Title)) != maxImportTitleLen {
		t.Errorf("title length = %d, want %d", len([]rune(meta.Title)), maxImportTitleLen)
	}
	meta, _ = parseImportMeta(formOf(map[string]string{
		"source": "link_import", "source_url": "https://shop.example/p", "title": "\x01\x02",
	}))
	if meta.Title != "Imported product" {
		t.Errorf("empty-after-sanitise title should default, got %q", meta.Title)
	}
}

func TestParseImportMeta_UnknownMethodDropped(t *testing.T) {
	meta, _ := parseImportMeta(formOf(map[string]string{
		"source": "link_import", "source_url": "https://shop.example/p", "import_method": "telepathy",
	}))
	if meta.ImportMethod != "" {
		t.Errorf("unknown import_method kept: %q", meta.ImportMethod)
	}
}

func TestUploadLimiter_TripsAtLimit(t *testing.T) {
	l := newKeyedLimiter()
	for i := 0; i < uploadRatePerUser; i++ {
		if !l.allow("u1", uploadRatePerUser, time.Hour) {
			t.Fatalf("request %d unexpectedly limited", i+1)
		}
	}
	if l.allow("u1", uploadRatePerUser, time.Hour) {
		t.Fatal("request past the limit was allowed")
	}
	if !l.allow("u2", uploadRatePerUser, time.Hour) {
		t.Fatal("limiter leaked across keys")
	}
}
