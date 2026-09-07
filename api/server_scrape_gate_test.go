package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
)

func TestServerScrapeGate_Enabled(t *testing.T) {
	config.ServerScrapeMode = "enabled"
	rr := httptest.NewRecorder()
	var lb strings.Builder
	if serverScrapeGate(rr, httptest.NewRequest("POST", "/product/details", nil), &lb, "u", "https://www.amazon.in/dp/X", "app") {
		t.Fatal("enabled mode must not handle the request")
	}
	if rr.Header().Get("Deprecation") != "" {
		t.Error("enabled mode must not advertise deprecation")
	}
}

func TestServerScrapeGate_Deprecated(t *testing.T) {
	config.ServerScrapeMode = "deprecated"
	config.ServerScrapeSunset = "2026-11-30"
	rr := httptest.NewRecorder()
	var lb strings.Builder
	if serverScrapeGate(rr, httptest.NewRequest("POST", "/product/details", nil), &lb, "u", "https://www.amazon.in/dp/X", "app") {
		t.Fatal("deprecated mode must still let the scrape run")
	}
	if rr.Header().Get("Deprecation") != "true" || rr.Header().Get("Sunset") != "2026-11-30" {
		t.Errorf("headers = %v", rr.Header())
	}
}

// The reason code is a contract with the legacy app: anything it does not
// recognise becomes scrape_failed, which opens the retry + screenshot sheet.
func TestServerScrapeGate_Disabled(t *testing.T) {
	config.ServerScrapeMode = "disabled"
	config.ServerScrapeSunset = ""
	for _, flow := range []string{"app", "guest"} {
		rr := httptest.NewRecorder()
		var lb strings.Builder
		if !serverScrapeGate(rr, httptest.NewRequest("POST", "/product/details", nil), &lb, "u", "https://www.amazon.in/dp/X", flow) {
			t.Fatalf("[%s] disabled mode must handle the request", flow)
		}
		if rr.Code != http.StatusGone {
			t.Errorf("[%s] status = %d, want 410", flow, rr.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("[%s] body not json: %v", flow, err)
		}
		if body["reason"] != "update_required" {
			t.Errorf("[%s] reason = %q", flow, body["reason"])
		}
		if !strings.Contains(body["error"], "update TryOnFusion") {
			t.Errorf("[%s] message should tell the user to update: %q", flow, body["error"])
		}
	}
	config.ServerScrapeMode = "deprecated"
}

func TestTermsMentionGrievanceAndLinkImport(t *testing.T) {
	config.GrievanceOfficerName = "Test Officer"
	config.GrievanceEmail = "legal@example.com"
	md := termsOfServiceMarkdown()
	for _, want := range []string{"## 4. Link Import Tool", "## 5. Intellectual Property Complaints and Takedown", "## 6. Grievance Officer", "Test Officer", "legal@example.com", "36 hours"} {
		if !strings.Contains(md, want) {
			t.Errorf("terms missing %q", want)
		}
	}
	if strings.Contains(md, "%!") {
		t.Error("fmt verb mismatch in terms template")
	}
}
