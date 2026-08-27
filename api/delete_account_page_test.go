package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
)

// The regression this guards: GET /auth/delete-account answered 405 for every
// Google Play crawl, which fails a Data Safety review quietly.
func TestDeleteAccountRouteServesPageOnGET(t *testing.T) {
	config.ContactEmail = "support@tryonfusion.com"

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			DeleteAccountRoute().ServeHTTP(rec, httptest.NewRequest(method, "/auth/delete-account", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("%s /auth/delete-account = %d, want 200", method, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
		})
	}
}

// The page has to be usable by someone who has lost access to their account,
// and by a reviewer with no credentials at all — so no auth on the GET branch.
func TestDeleteAccountPageNeedsNoAuth(t *testing.T) {
	config.ContactEmail = "support@tryonfusion.com"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/delete-account", nil) // no Authorization header
	DeleteAccountRoute().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unauthenticated GET = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	// A Data Safety reviewer looks for the steps, the contact route and the
	// data-handling statement. If any of these stop rendering, the page still
	// returns 200 but no longer passes review.
	for _, want := range []string{
		"Delete Your TryOnFusion Account",
		"Delete Account",          // the in-app step
		"support@tryonfusion.com", // the no-app-access route
		"What is deleted",
		"What is kept, and for how long",
		deletionWindowDays,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// GET must not have opened a hole in the API: the destructive verbs still go
// through AuthMiddleware.
func TestDeleteAccountAPIVerbsStillRequireAuth(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			DeleteAccountRoute().ServeHTTP(rec, httptest.NewRequest(method, "/auth/delete-account", nil))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated %s = %d, want 401", method, rec.Code)
			}
		})
	}
}
