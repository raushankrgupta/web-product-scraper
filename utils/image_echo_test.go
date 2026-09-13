package utils

import (
	"math/bits"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures are the real generations that exposed this: a guest try-on
// whose result came back as the customer's own photo, and a signed-in one on
// the same route that genuinely changed the clothes. They are downscaled to
// 256px because the hash reduces everything to an 8x8 grid anyway.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "echo", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func TestEchoedInputCatchesAnEchoedCustomerPhoto(t *testing.T) {
	r := &resolvedTryOn{People: []resolvedPerson{{
		Photo:    &fetchedImage{data: fixture(t, "customer.jpg")},
		Garments: []fetchedImage{{data: fixture(t, "garment.jpg")}},
	}}}

	if got := r.echoedInput(fixture(t, "echo.jpg")); got != "customer photo" {
		t.Fatalf("echoed customer photo not detected: got %q, want %q", got, "customer photo")
	}
}

func TestEchoedInputAcceptsAGenuineTryOn(t *testing.T) {
	// Same person, same pose, same background — only the clothes changed.
	// This is the case a too-eager detector would wrongly reject, which costs
	// a customer a good image.
	r := &resolvedTryOn{People: []resolvedPerson{{
		Photo:    &fetchedImage{data: fixture(t, "customer_b.jpg")},
		Garments: []fetchedImage{{data: fixture(t, "garment.jpg")}},
	}}}

	if got := r.echoedInput(fixture(t, "genuine.jpg")); got != "" {
		t.Fatalf("genuine try-on wrongly flagged as an echo: %q", got)
	}
}

func TestEchoedInputCatchesAnEchoedGarment(t *testing.T) {
	r := &resolvedTryOn{People: []resolvedPerson{{
		Photo:    &fetchedImage{data: fixture(t, "customer.jpg")},
		Garments: []fetchedImage{{data: fixture(t, "garment.jpg")}},
	}}}

	if got := r.echoedInput(fixture(t, "garment.jpg")); got != "customer garment 1" {
		t.Fatalf("echoed garment not detected: got %q", got)
	}
}

// Undecodable bytes must not be able to reject a generation: the detector
// exists to catch a model quirk, not to become a new source of failures.
func TestEchoedInputFailsOpenOnUndecodableImages(t *testing.T) {
	r := &resolvedTryOn{People: []resolvedPerson{{
		Photo: &fetchedImage{data: []byte("not an image")},
	}}}

	if got := r.echoedInput([]byte("also not an image")); got != "" {
		t.Fatalf("undecodable input produced a rejection: %q", got)
	}
	if got := r.echoedInput(fixture(t, "genuine.jpg")); got != "" {
		t.Fatalf("undecodable stored input produced a rejection: %q", got)
	}
}

// The margin between an echo and a genuine result is the whole basis for
// echoDistanceMax. If a change to the hash narrows it, this fails before the
// threshold silently stops separating the two.
func TestEchoDetectionKeepsItsMargin(t *testing.T) {
	hash := func(name string) imageHashes {
		h, ok := perceptualHashes(fixture(t, name))
		if !ok {
			t.Fatalf("could not hash %s", name)
		}
		return h
	}
	dist := func(a, b imageHashes) (int, int) {
		return bits.OnesCount64(a.dHash ^ b.dHash), bits.OnesCount64(a.aHash ^ b.aHash)
	}

	echoD, echoA := dist(hash("customer.jpg"), hash("echo.jpg"))
	genD, genA := dist(hash("customer_b.jpg"), hash("genuine.jpg"))

	if echoD > echoDistanceMax || echoA > echoDistanceMax {
		t.Errorf("echo no longer inside the threshold: dHash=%d aHash=%d, max=%d",
			echoD, echoA, echoDistanceMax)
	}
	// Both hashes have to clear the bar for a rejection, so the genuine
	// result only needs one of them comfortably outside it.
	if genD <= echoDistanceMax && genA <= echoDistanceMax {
		t.Errorf("genuine try-on is inside the threshold: dHash=%d aHash=%d, max=%d",
			genD, genA, echoDistanceMax)
	}
	t.Logf("echo dHash=%d aHash=%d | genuine dHash=%d aHash=%d | threshold=%d",
		echoD, echoA, genD, genA, echoDistanceMax)
}
