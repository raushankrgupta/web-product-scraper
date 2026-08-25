package utils

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fetchImage used to call http.Get, which uses http.DefaultClient — a client
// with NO timeout. One stalled S3 or CDN connection blocked a try-on request
// forever, independent of the Gemini deadline.
func TestImageFetchClientHasATimeout(t *testing.T) {
	if imageFetchClient.Timeout == 0 {
		t.Fatal("imageFetchClient has no timeout — that is the http.DefaultClient bug")
	}
	if imageFetchClient.Timeout > 30*time.Second {
		t.Errorf("imageFetchClient timeout is %s, too long to bound a request", imageFetchClient.Timeout)
	}
}

// The parent Gemini context must be able to cancel an in-flight download.
func TestFetchImageRespectsContextCancellation(t *testing.T) {
	stall := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stall // never responds
	}))
	defer func() { close(stall); srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := fetchImage(ctx, srv.URL+"/slow.jpg")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the context expired")
	}
	if elapsed > 3*time.Second {
		t.Errorf("fetchImage took %s — the context deadline was ignored", elapsed)
	}
}

func TestFetchImageRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := fetchImage(context.Background(), srv.URL+"/x.jpg"); err == nil {
		t.Fatal("expected an error for a 403")
	}
}

// An endpoint that streams forever must not be an unbounded allocation.
func TestFetchImageBoundsResponseSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 40; i++ { // 40 MiB, over the 25 MiB cap
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	if _, err := fetchImage(context.Background(), srv.URL+"/huge.jpg"); err == nil {
		t.Fatal("expected an error for an oversized image")
	}
}

func TestFetchImageSuccess(t *testing.T) {
	want := "GIF89a-pretend-image"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(want))
	}))
	defer srv.Close()

	got, err := fetchImage(context.Background(), srv.URL+"/ok.jpg")
	if err != nil {
		t.Fatalf("fetchImage: %v", err)
	}
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The MIME sniffer is what stopped PNG and WebP payloads being labelled
// "jpeg" — the legacy generator hardcoded that.
func TestFetchImageLoggedSniffsMIME(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D}, "png"},
		{"webp", []byte{0x52, 0x49, 0x46, 0x46, 0x00}, "webp"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, "jpeg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(c.body)
			}))
			defer srv.Close()

			_, mime, err := fetchImageLogged(context.Background(), "test", srv.URL+"/x")
			if err != nil {
				t.Fatalf("fetchImageLogged: %v", err)
			}
			if mime != c.want {
				t.Errorf("mime = %q, want %q", mime, c.want)
			}
		})
	}
}
