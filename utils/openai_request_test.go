package utils

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raushankrgupta/web-product-scraper/config"
)

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
)

// TestOpenAIEdit_SendsRealImageTypes is the regression guard for the failure
// that stopped every OpenAI generation:
//
//	openai 400 unsupported_file_mimetype: Invalid file 'image[0]':
//	unsupported mimetype ('application/octet-stream')
//
// mw.CreateFormFile labels every part octet-stream, and fetchedImage.mime holds
// a short name ("png") that extForMIME did not recognise, so a PNG also went up
// named .jpg. Both the Content-Type and the extension must match the bytes.
func TestOpenAIEdit_SendsRealImageTypes(t *testing.T) {
	type part struct{ contentType, filename string }
	var got []part

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("request is not multipart: %v", err)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("reading part: %v", err)
				return
			}
			if p.FormName() == "image[]" {
				got = append(got, part{p.Header.Get("Content-Type"), p.FileName()})
			}
		}
		img := base64.StdEncoding.EncodeToString(pngBytes)
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": img}}})
	}))
	defer srv.Close()

	prevURL, prevKey := openAIEditsURL, config.OpenAIAPIKey
	defer func() { openAIEditsURL, config.OpenAIAPIKey = prevURL, prevKey }()
	openAIEditsURL, config.OpenAIAPIKey = srv.URL, "test-key"

	_, err := openaiEdit(context.Background(), "test", "a prompt", "gpt-image-2", "medium", "flash",
		[]openAIInputImage{
			// mime deliberately holds the short name fetchImageLogged writes.
			{img: fetchedImage{mime: "jpeg", data: jpegBytes}, name: "person1"},
			{img: fetchedImage{mime: "png", data: pngBytes}, name: "style"},
		})
	if err != nil {
		t.Fatalf("openaiEdit: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d image parts, want 2", len(got))
	}
	want := []part{{"image/jpeg", ".jpg"}, {"image/png", ".png"}}
	for i := range want {
		if got[i].contentType != want[i].contentType {
			t.Errorf("part %d Content-Type = %q, want %q", i, got[i].contentType, want[i].contentType)
		}
		if !strings.HasSuffix(got[i].filename, want[i].filename) {
			t.Errorf("part %d filename = %q, want a %s extension", i, got[i].filename, want[i].filename)
		}
	}
	for _, p := range got {
		if p.contentType == "application/octet-stream" {
			t.Error("an image part was sent as application/octet-stream — OpenAI rejects that")
		}
	}
}
