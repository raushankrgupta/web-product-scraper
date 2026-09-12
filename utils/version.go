package utils

import (
	"strconv"
	"strings"
)

// CompareVersions compares two dotted version strings numerically.
//
// Returns -1 when a < b, 0 when they are equal, and 1 when a > b.
//
// Numeric rather than lexicographic because the difference matters exactly
// where this is used: "2.10.0" is newer than "2.9.9", and a string comparison
// says the opposite. Missing segments are treated as zero, so "2.6" and
// "2.6.0" are the same version.
//
// Anything unparsable in a segment counts as zero rather than as an error.
// That is deliberate: the only consumer is the update gate, and the safe
// answer to "is this garbage version older than the minimum?" is "no". A
// version string we cannot read must never be the reason a user is locked out
// of the app.
func CompareVersions(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)

	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}

	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// splitVersion turns "2.5.1-beta.2" into [2, 5, 1].
//
// A pre-release or build suffix is dropped rather than ordered. Semver says
// 2.5.1-beta precedes 2.5.1, but nothing here ships pre-release builds to the
// store, and the cost of getting that ordering subtly wrong (a build that
// thinks it is older than itself, and forces an update on loop) is far higher
// than the cost of ignoring it.
func splitVersion(v string) []int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}

	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 {
			n = 0
		}
		out = append(out, n)
	}
	return out
}
