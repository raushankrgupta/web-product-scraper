package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func requestIDForTest(r *http.Request) string {
	return utils.RequestIDFromContext(r.Context())
}

// A wrong verb must produce a logged 405 that names the allowed methods —
// not the silent sub-10µs 401 that made 22 production requests unexplainable.
func TestMethodGuardRejectsWrongVerb(t *testing.T) {
	var handlerRan bool
	h := MethodGuard([]string{http.MethodDelete, http.MethodPost},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlerRan = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/delete-account", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if handlerRan {
		t.Error("the handler must not run for a rejected verb")
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "DELETE") || !strings.Contains(allow, "POST") {
		t.Errorf("Allow header = %q, should list the accepted verbs", allow)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected an error message explaining the rejection")
	}
}

func TestMethodGuardAllowsListedVerbsAndOptions(t *testing.T) {
	for _, m := range []string{http.MethodDelete, http.MethodPost, http.MethodOptions} {
		ran := false
		h := MethodGuard([]string{http.MethodDelete, http.MethodPost},
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/auth/delete-account", nil))

		if !ran {
			t.Errorf("%s was rejected but should be allowed", m)
		}
	}
}

// A panic must become a 500 with a JSON body, not a dead process.
func TestRecoverMiddlewareTurnsPanicInto500(t *testing.T) {
	h := RecoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p *struct{ X int }
		_ = p.X // nil dereference, exactly like the blocked-candidate bug
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/try-on", nil)

	// Must not propagate.
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v — body: %s", err, rec.Body.String())
	}
	if strings.Contains(strings.ToLower(body["error"]), "nil pointer") {
		t.Errorf("internal panic detail leaked to the client: %s", body["error"])
	}
}

func TestRecoverMiddlewarePassesThroughNormalRequests(t *testing.T) {
	h := RecoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418 — the middleware must be transparent", rec.Code)
	}
}

func TestRequestIDMiddlewareStampsAndEchoes(t *testing.T) {
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDForTest(r)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("no request id was placed in the context")
	}
	if echoed := rec.Header().Get("X-Request-ID"); echoed != seen {
		t.Errorf("X-Request-ID header = %q, context = %q — they must match", echoed, seen)
	}
}

func TestRequestIDMiddlewareHonoursInboundID(t *testing.T) {
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDForTest(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "client-supplied-id")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "client-supplied-id" {
		t.Errorf("request id = %q, want the client-supplied value", seen)
	}
}

func TestRequestIDMiddlewareRejectsOversizedInboundID(t *testing.T) {
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDForTest(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", strings.Repeat("A", 500))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(seen) > 64 {
		t.Errorf("an oversized client id (%d chars) was accepted", len(seen))
	}
}

// The tombstone rename is what makes "sign up again" actually possible after
// an account deletion.
func TestTombstoneEmail(t *testing.T) {
	id := primitive.NewObjectID()

	got := tombstoneEmail("someone@gmail.com", id)
	if got == "someone@gmail.com" {
		t.Fatal("the original address must not survive")
	}
	if !strings.HasSuffix(got, "@gmail.com") {
		t.Errorf("tombstone %q should keep the domain", got)
	}
	if !strings.Contains(got, id.Hex()) {
		t.Errorf("tombstone %q should contain the user id so two deletions can't collide", got)
	}

	// Two different users deleting the same address produce distinct
	// tombstones — a unique index on `email` must not reject the second.
	other := tombstoneEmail("someone@gmail.com", primitive.NewObjectID())
	if other == got {
		t.Error("two deletions of the same address collided")
	}
}

func TestTombstoneEmailHandlesMalformedInput(t *testing.T) {
	id := primitive.NewObjectID()
	for _, in := range []string{"", "no-at-sign", "trailing@"} {
		got := tombstoneEmail(in, id)
		if got == in {
			t.Errorf("tombstoneEmail(%q) returned the input unchanged", in)
		}
		if !strings.Contains(got, "@") {
			t.Errorf("tombstoneEmail(%q) = %q, not an address", in, got)
		}
	}
}
