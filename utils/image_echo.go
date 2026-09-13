package utils

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math/bits"
)

// Detecting a provider that hands back one of its own inputs.
//
// Gemini's flash tier does this on tightly-cropped portraits: asked to put a
// full-length dress on a head-and-shoulders photo, it returns the customer's
// own picture, re-encoded and slightly re-cropped. There is nothing in the
// response to distinguish it from a real result — it is a 200 with image
// bytes, a plausible size, and no finish reason — so it reaches the customer
// as a successful try-on of the clothes they were already wearing.
//
// That is the worst possible failure for the guest funnel: it silently spends
// the one free generation a first-time user gets, and they leave believing the
// product does not work rather than seeing an error and trying another photo.
//
// The comparison is perceptual rather than byte-wise because the echo is never
// byte-identical: the example that prompted this was a 960x1280 JPEG in and an
// 864x1184 PNG out.

// ErrInputEcho marks a generation that came back as a copy of one of its own
// inputs. Classified as a model quirk rather than a content refusal: the
// customer's photo was acceptable to the provider — it answered — so a second
// vendor is a genuinely different roll and worth the fallback.
var ErrInputEcho = errors.New("provider returned an input image unchanged")

// hashBits is the side length of the grid each hash is reduced to. 8 gives a
// 64-bit hash, which is the usual size for this family and leaves plenty of
// room between "the same picture" and "the same person wearing something
// else".
const hashBits = 8

// echoDistanceMax is the Hamming distance at or below which two images are
// treated as the same picture. Both hashes must agree before anything is
// rejected.
//
// Measured on real generations rather than guessed:
//
//	                                     dHash  aHash
//	echo (flash returned the input)          3      1
//	genuine try-on, same person/pose        21     32
//	two unrelated images                    30     26
//
// 6 sits about a factor of three below the nearest genuine result, so a real
// try-on would have to become dramatically more conservative than any observed
// before it tripped. Erring high here costs a customer a good image; erring low
// costs them their free generation and their first impression.
const echoDistanceMax = 6

// echoedInput reports which input, if any, the generated image is a copy of.
//
// Returns "" when the output is genuinely new, and also whenever the
// comparison cannot be made — an image we fail to decode (WebP, say, which the
// standard library does not read) must not be able to reject a generation that
// may well be fine. Failing open keeps a detector for a model quirk from
// becoming a new source of outages.
func (r *resolvedTryOn) echoedInput(generated []byte) string {
	gen, ok := perceptualHashes(generated)
	if !ok {
		return ""
	}

	for i, p := range r.People {
		if p.Photo != nil && gen.matches(p.Photo.data) {
			return personLabel(i, len(r.People)) + " photo"
		}
		for n, g := range p.Garments {
			if gen.matches(g.data) {
				return personLabel(i, len(r.People)) + " garment " + string(rune('1'+n))
			}
		}
	}
	return ""
}

func personLabel(i, total int) string {
	if total == 1 {
		return "customer"
	}
	return "customer " + string(rune('1'+i))
}

// imageHashes is one image reduced to a difference hash and an average hash.
// Two independent reductions are used because each is blind to something: a
// dHash ignores overall brightness, an aHash ignores local structure, and an
// echo is the one case that scores low on both.
type imageHashes struct {
	dHash uint64
	aHash uint64
}

// matches reports whether other is perceptually the same picture.
func (h imageHashes) matches(other []byte) bool {
	o, ok := perceptualHashes(other)
	if !ok {
		return false
	}
	return bits.OnesCount64(h.dHash^o.dHash) <= echoDistanceMax &&
		bits.OnesCount64(h.aHash^o.aHash) <= echoDistanceMax
}

func perceptualHashes(data []byte) (imageHashes, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return imageHashes{}, false
	}

	// One extra column so each row has a right-hand neighbour to difference
	// against; the average hash ignores it.
	grid := downscaleGray(img, hashBits+1, hashBits)

	var d, a uint64
	var sum int
	for y := 0; y < hashBits; y++ {
		for x := 0; x < hashBits; x++ {
			sum += grid[y*(hashBits+1)+x]
		}
	}
	avg := sum / (hashBits * hashBits)

	for y := 0; y < hashBits; y++ {
		for x := 0; x < hashBits; x++ {
			left := grid[y*(hashBits+1)+x]
			right := grid[y*(hashBits+1)+x+1]
			d <<= 1
			if left > right {
				d |= 1
			}
			a <<= 1
			if left > avg {
				a |= 1
			}
		}
	}
	return imageHashes{dHash: d, aHash: a}, true
}

// downscaleGray reduces img to a w x h grid of grey levels by averaging each
// source block.
//
// Box averaging rather than nearest-neighbour sampling: a re-encoded copy of
// an image differs from the original in exactly the high-frequency detail that
// point sampling keeps and averaging discards, so sampling would make the
// echo it is meant to catch look less similar than it is.
func downscaleGray(img image.Image, w, h int) []int {
	b := img.Bounds()
	out := make([]int, w*h)
	if b.Dx() == 0 || b.Dy() == 0 {
		return out
	}

	for cy := 0; cy < h; cy++ {
		y0 := b.Min.Y + cy*b.Dy()/h
		y1 := b.Min.Y + (cy+1)*b.Dy()/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for cx := 0; cx < w; cx++ {
			x0 := b.Min.X + cx*b.Dx()/w
			x1 := b.Min.X + (cx+1)*b.Dx()/w
			if x1 <= x0 {
				x1 = x0 + 1
			}

			sum, n := 0, 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					cr, cg, cb, _ := img.At(x, y).RGBA()
					// Rec. 601 luma on the 16-bit values RGBA() returns,
					// scaled back down to 0-255.
					sum += int((299*cr + 587*cg + 114*cb) / 1000 >> 8)
					n++
				}
			}
			if n > 0 {
				out[cy*w+cx] = sum / n
			}
		}
	}
	return out
}
