package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetGuards() {
	inflight.mu.Lock()
	inflight.items = map[string]time.Time{}
	inflight.mu.Unlock()

	failures.mu.Lock()
	failures.counts = map[string][]time.Time{}
	failures.mu.Unlock()

	resultsMu.Lock()
	results = map[string]cachedResult{}
	resultsMu.Unlock()
}

// withUser builds a request already carrying the user id AuthMiddleware would
// have injected.
func withUser(userID, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/try-on", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), UserIDKey, userID))
}

// The production log shows 30 requests covering 11 unique pairs. Two
// concurrent identical requests must result in exactly one upstream call.
func TestTryOnGuardRejectsConcurrentDuplicates(t *testing.T) {
	resetGuards()

	var calls int32
	release := make(chan struct{})
	handler := TryOnGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release // hold the first request open
		w.WriteHeader(http.StatusOK)
	}))

	body := `{"person_id":"p1","product_id":"x1"}`

	first := httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handler.ServeHTTP(first, withUser("u1", body))
	}()

	// Wait until the first request is actually inside the handler.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, withUser("u1", body))

	if second.Code != http.StatusConflict {
		t.Errorf("duplicate request status = %d, want 409", second.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler ran %d times, want 1 — the duplicate must not reach the upstream", got)
	}

	close(release)
	wg.Wait()
}

// Whitespace and key ordering must not defeat the fingerprint.
func TestTryOnGuardFingerprintIsCanonical(t *testing.T) {
	a := fingerprintBody([]byte(`{"person_id":"p1","product_id":"x1"}`))
	b := fingerprintBody([]byte("{\n  \"product_id\": \"x1\",\n  \"person_id\": \"p1\"\n}"))
	if a != b {
		t.Errorf("logically identical bodies fingerprinted differently: %s vs %s", a, b)
	}

	c := fingerprintBody([]byte(`{"person_id":"p1","product_id":"x2"}`))
	if a == c {
		t.Error("different products must fingerprint differently")
	}
}

// A different (person, product) pair must not be blocked by an in-flight one.
func TestTryOnGuardAllowsDifferentRequests(t *testing.T) {
	resetGuards()

	handler := TryOnGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, body := range []string{
		`{"person_id":"p1","product_id":"x1"}`,
		`{"person_id":"p1","product_id":"x2"}`,
		`{"person_id":"p2","product_id":"x1"}`,
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, withUser("u1", body))
		if rec.Code != http.StatusOK {
			t.Errorf("body %s got %d, want 200", body, rec.Code)
		}
	}
}

// The handler must still see a readable body after the guard buffered it.
func TestTryOnGuardRestoresBody(t *testing.T) {
	resetGuards()

	var seen map[string]string
	handler := TryOnGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), withUser("u1", `{"person_id":"p9","product_id":"x9"}`))

	if seen["person_id"] != "p9" || seen["product_id"] != "x9" {
		t.Errorf("handler saw %v, want the original body", seen)
	}
}

// Three failures inside the window and the fourth attempt is refused locally.
func TestTryOnGuardThrottlesAfterRepeatedFailures(t *testing.T) {
	resetGuards()

	var calls int32
	handler := TryOnGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		// Distinct bodies so in-flight dedup isn't what's being measured.
		handler.ServeHTTP(rec, withUser("u1", `{"person_id":"p1","product_id":"x`+string(rune('0'+i))+`"}`))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("attempt %d: got %d, want 500", i, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, withUser("u1", `{"person_id":"p1","product_id":"x9"}`))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("4th attempt status = %d, want 429", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("handler ran %d times, want 3 — the 4th must be refused locally", got)
	}

	// A different user must be unaffected.
	other := httptest.NewRecorder()
	handler.ServeHTTP(other, withUser("u2", `{"person_id":"p1","product_id":"x1"}`))
	if other.Code == http.StatusTooManyRequests {
		t.Error("one user's failures throttled another user")
	}
}

func TestTryOnGuardSuccessClearsFailures(t *testing.T) {
	resetGuards()

	failures.RecordFailure("u1")
	failures.RecordFailure("u1")

	handler := TryOnGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), withUser("u1", `{"a":1}`))

	if failures.Throttled("u1") {
		t.Error("a success should clear the user's failure streak")
	}
	if len(failures.counts["u1"]) != 0 {
		t.Errorf("failure counts not cleared: %v", failures.counts["u1"])
	}
}

// Multipart (guest) requests must pass straight through — buffering the body
// to hash it would corrupt the upload.
func TestTryOnGuardSkipsMultipart(t *testing.T) {
	resetGuards()

	var gotBody string
	handler := TryOnGuardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 11)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodPost, "/try-on/guest", strings.NewReader("hello world"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	r = r.WithContext(context.WithValue(r.Context(), UserIDKey, "guest:abc"))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("multipart request got %d, want 200", rec.Code)
	}
	if gotBody != "hello world" {
		t.Errorf("handler saw %q, want the untouched multipart body", gotBody)
	}
}

func TestResultCacheRoundTrip(t *testing.T) {
	resetGuards()

	key := tryOnCacheKey("u1", "p1", "prod1", "", "")
	if _, ok := lookupTryOnResult(key); ok {
		t.Fatal("empty cache returned a hit")
	}

	rememberTryOnResult(key, "generated_images/abc.jpg")

	got, ok := lookupTryOnResult(key)
	if !ok || got != "generated_images/abc.jpg" {
		t.Fatalf("lookup = (%q, %v), want the stored key", got, ok)
	}

	// A different user must not hit another user's cached result.
	if _, ok := lookupTryOnResult(tryOnCacheKey("u2", "p1", "prod1", "", "")); ok {
		t.Error("cache leaked a result across users")
	}

	// Nor may a different styling note. "Same person, same garment, but on a
	// rooftop at night" is a different image, and serving the cached daylight
	// one would look like the note was ignored.
	if _, ok := lookupTryOnResult(tryOnCacheKey("u1", "p1", "prod1", "", "on a rooftop at night")); ok {
		t.Error("cache ignored the special request when keying a result")
	}
}

func TestResultCacheExpires(t *testing.T) {
	resetGuards()

	key := tryOnCacheKey("u1", "p1", "prod1", "", "")
	resultsMu.Lock()
	results[key] = cachedResult{objectKey: "old.jpg", at: time.Now().Add(-resultCacheTTL - time.Minute)}
	resultsMu.Unlock()

	if _, ok := lookupTryOnResult(key); ok {
		t.Error("an expired entry was served from the cache")
	}
}

func TestInflightEntriesExpire(t *testing.T) {
	resetGuards()

	if !inflight.TryAcquire("k") {
		t.Fatal("first acquire failed")
	}
	if inflight.TryAcquire("k") {
		t.Fatal("second acquire succeeded while the first was held")
	}

	// Simulate a handler that died without releasing.
	inflight.mu.Lock()
	inflight.items["k"] = time.Now().Add(-inflightTTL - time.Second)
	inflight.mu.Unlock()

	if !inflight.TryAcquire("k") {
		t.Error("a stale in-flight entry permanently blocked the key")
	}
}
