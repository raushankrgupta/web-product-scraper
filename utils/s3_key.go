package utils

import (
	neturl "net/url"
	"strings"
)

// S3KeyFromURL recovers the object key from an S3 URL — presigned or plain.
//
// The subtlety this exists for is percent-encoding. A URL path is encoded; an
// object key is not. Slicing the string after "amazonaws.com/" yields the
// *encoded* path, and handing that back to the SDK as a key encodes it a
// second time, so a key containing `+` (ordinary in Amazon and Myntra image
// filenames) becomes `%2B` on the way out and `%252B` on the way back in. S3
// then answers 404 for an object that is sitting right there, which surfaced
// in production as a try-on quietly generating without one of its garments:
//
//	gemini image fetch failed source=.../product_images/…_41%2BJJwwJjlL.jpg
//	                            error="failed to fetch image, status: 404"
//
// A value that is not one of our S3 URLs is returned untouched — a retailer
// CDN link kept verbatim by the scraper is still the best description of what
// was stored.
func S3KeyFromURL(img string) string {
	const marker = "amazonaws.com/"
	i := strings.Index(img, marker)
	if i < 0 {
		return img
	}

	key := img[i+len(marker):]
	key = strings.SplitN(key, "?", 2)[0] // drop the presign signature
	if key == "" {
		return img
	}
	return decodeS3Key(key)
}

// NormaliseS3Key repairs a stored key that was captured from an encoded URL.
//
// Rows written before S3KeyFromURL existed hold keys like
// `product_images/…_41%2BJJwwJjlL.jpg`, and those 404 forever otherwise: the
// object's real key has a literal `+`. Decoding is safe because every key we
// generate is built from a sanitised filename that contains no `%` at all — so
// a `%` in a stored key can only have come from encoding.
func NormaliseS3Key(key string) string {
	if key == "" || !strings.Contains(key, "%") {
		return key
	}
	return decodeS3Key(key)
}

// decodeS3Key percent-decodes a path segment, leaving it alone if the value is
// not valid encoding. Failing closed matters: a key with a bare `%` that is not
// an escape must keep it rather than being silently mangled into a miss.
func decodeS3Key(key string) string {
	decoded, err := neturl.PathUnescape(key)
	if err != nil {
		return key
	}
	return decoded
}

// SafeS3Filename reduces an arbitrary filename to characters that survive a
// round trip through an S3 key and a URL path unchanged.
//
// Retailer CDNs put `+`, spaces and percent-escapes in image filenames, and
// every one of those means something different in a URL than it does in a key.
// Rather than encode carefully at each of the several places a key becomes a
// URL, the ambiguity is removed once, here, when the key is minted.
func SafeS3Filename(name string) string {
	// A name lifted from a URL path may still be encoded; decode first so
	// `%2B` collapses to `+` and then to `_`, instead of surviving as three
	// separate characters.
	name = decodeS3Key(name)

	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
