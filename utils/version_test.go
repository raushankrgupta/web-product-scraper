package utils

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		why  string
	}{
		{"2.5.1", "2.5.1", 0, "identical"},
		{"2.5.1", "2.6.0", -1, "older minor"},
		{"2.6.0", "2.5.1", 1, "newer minor"},
		// The case a string comparison gets wrong, and the reason this
		// function exists at all.
		{"2.9.9", "2.10.0", -1, "double-digit segment sorts numerically"},
		{"2.10.0", "2.9.9", 1, "double-digit segment sorts numerically"},
		{"2.6", "2.6.0", 0, "missing segments are zero"},
		{"2.6.0", "2.6", 0, "missing segments are zero"},
		{"3", "2.99.99", 1, "major wins"},
		{"2.5.1-beta.2", "2.5.1", 0, "pre-release suffix is ignored, not ordered"},
		{"", "2.6.0", -1, "empty is oldest"},
		{"", "", 0, "two empties are equal"},
		// Garbage must never read as "older than the minimum", because that
		// is the input that would force an update nobody can satisfy.
		{"not-a-version", "2.6.0", -1, "unparsable segments count as zero"},
		{"2.x.1", "2.0.1", 0, "unparsable middle segment counts as zero"},
	}

	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d (%s)", c.a, c.b, got, c.want, c.why)
		}
	}
}

func TestCompareVersions_IsAntisymmetric(t *testing.T) {
	// A comparator that disagrees with itself when the arguments are swapped
	// would let a device be simultaneously above the floor and below it.
	pairs := [][2]string{
		{"2.5.1", "2.6.0"}, {"2.10.0", "2.9.9"}, {"2.6", "2.6.0"}, {"", "1.0.0"},
	}
	for _, p := range pairs {
		forward, backward := CompareVersions(p[0], p[1]), CompareVersions(p[1], p[0])
		if forward != -backward {
			t.Errorf("CompareVersions(%q,%q)=%d but reversed=%d", p[0], p[1], forward, backward)
		}
	}
}
