package alert

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestTelegram points the transport at a local test server and makes
// backoff instant.
func newTestTelegram(t *testing.T, h http.HandlerFunc) (*telegram, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tg := newTelegram("token", "-100123")
	tg.sleep = func(time.Duration) {}
	// Rewrite the request host to the test server.
	tg.client = &http.Client{
		Timeout:   5 * time.Second,
		Transport: rewriteTransport{host: strings.TrimPrefix(srv.URL, "http://")},
	}
	return tg, srv
}

type rewriteTransport struct{ host string }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = r.host
	return http.DefaultTransport.RoundTrip(req)
}

func TestTelegramSendSuccess(t *testing.T) {
	var gotBody atomic.Value
	tg, _ := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody.Store(string(buf))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	if err := tg.Send("hello <b>world</b>"); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	body, _ := gotBody.Load().(string)
	if !strings.Contains(body, `"parse_mode":"HTML"`) {
		t.Errorf("request should use HTML parse mode, got: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"-100123"`) {
		t.Errorf("request should carry the chat id, got: %s", body)
	}
}

func TestTelegramRetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int32
	tg, _ := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	if err := tg.Send("x"); err != nil {
		t.Fatalf("Send() = %v, want nil after retries", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("made %d attempts, want 3", got)
	}
}

// A malformed message must be dropped, not retried forever — retrying it
// would starve every subsequent alert behind it.
func TestTelegramDoesNotRetry4xx(t *testing.T) {
	var calls int32
	tg, _ := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"can't parse entities"}`))
	})

	if err := tg.Send("x"); err == nil {
		t.Fatal("expected an error for a 400")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("made %d attempts on a 400, want 1 (no retry)", got)
	}
}

func TestTelegramHonoursRetryAfterOnce(t *testing.T) {
	var calls int32
	var slept time.Duration

	tg, _ := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok":false,"parameters":{"retry_after":7}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	tg.sleep = func(d time.Duration) { slept += d }

	if err := tg.Send("x"); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if slept < 7*time.Second {
		t.Errorf("slept %s, want at least the 7s Telegram asked for", slept)
	}
}

func TestTelegramGivesUpOnRepeatedRateLimits(t *testing.T) {
	tg, _ := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"parameters":{"retry_after":1}}`))
	})
	tg.sleep = func(time.Duration) {}

	if err := tg.Send("x"); err == nil {
		t.Fatal("expected an error after repeated rate limits")
	}
}
