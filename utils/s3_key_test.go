package utils

import "testing"

// The production failure this whole file exists for: an Amazon image filename
// contains a literal `+`, presigning encodes it to `%2B`, and slicing the key
// back out of that URL without decoding produced a key that S3 answered 404
// for — silently dropping a garment from a try-on the user had paid for.
func TestS3KeyFromURLDecodesPercentEscapes(t *testing.T) {
	const presigned = "https://tryonfusion.s3.ap-south-1.amazonaws.com/" +
		"product_images/1779810615045362697_41%2BJJwwJjlL.jpg" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=deadbeef"

	got := S3KeyFromURL(presigned)
	want := "product_images/1779810615045362697_41+JJwwJjlL.jpg"
	if got != want {
		t.Fatalf("S3KeyFromURL = %q, want %q", got, want)
	}
}

func TestS3KeyFromURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain key needs no decoding",
			in:   "https://b.s3.ap-south-1.amazonaws.com/product_images/plain.jpg",
			want: "product_images/plain.jpg",
		},
		{
			name: "encoded space",
			in:   "https://b.s3.ap-south-1.amazonaws.com/product_images/a%20b.jpg",
			want: "product_images/a b.jpg",
		},
		{
			name: "retailer CDN url is left alone",
			in:   "https://assets.myntassets.com/v1/images/style/1.jpg",
			want: "https://assets.myntassets.com/v1/images/style/1.jpg",
		},
		{
			name: "bare key is left alone",
			in:   "product_uploads/abc123.jpg",
			want: "product_uploads/abc123.jpg",
		},
		{
			name: "invalid escape survives rather than being mangled",
			in:   "https://b.s3.ap-south-1.amazonaws.com/product_images/100%_off.jpg",
			want: "product_images/100%_off.jpg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := S3KeyFromURL(tc.in); got != tc.want {
				t.Errorf("S3KeyFromURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Rows written before the fix hold the encoded form permanently. Presigning
// them unchanged double-encodes and 404s forever, so a stored key carrying
// escapes has to be decoded on the way out.
func TestNormaliseS3KeyRepairsStoredKeys(t *testing.T) {
	got := NormaliseS3Key("product_images/1779810615045362697_41%2BJJwwJjlL.jpg")
	want := "product_images/1779810615045362697_41+JJwwJjlL.jpg"
	if got != want {
		t.Fatalf("NormaliseS3Key = %q, want %q", got, want)
	}

	// A key with nothing to repair must come back byte-identical.
	const clean = "product_uploads/1788882201934689861_photo.jpg"
	if got := NormaliseS3Key(clean); got != clean {
		t.Errorf("NormaliseS3Key(%q) = %q, want it unchanged", clean, got)
	}
}

func TestSafeS3Filename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// The character that caused the outage, and its encoded form, both
		// collapse to something that means the same thing in a key and a URL.
		{"41+JJwwJjlL.jpg", "41_JJwwJjlL.jpg"},
		{"41%2BJJwwJjlL.jpg", "41_JJwwJjlL.jpg"},
		{"summer dress.jpg", "summer_dress.jpg"},
		{"already-safe_1.jpg", "already-safe_1.jpg"},
		{"a?b=c.jpg", "a_b_c.jpg"},
	}
	for _, tc := range cases {
		if got := SafeS3Filename(tc.in); got != tc.want {
			t.Errorf("SafeS3Filename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
