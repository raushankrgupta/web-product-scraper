package utils

import (
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// tombstonePrefix is the local-part marker that identifies a renamed address.
// It is the single source of truth for both writing tombstones and detecting
// them, so the backfill tool can never disagree with the handlers about what
// "already tombstoned" means.
const tombstonePrefix = "deleted+"

// TombstoneEmail renames a deleted account's address so the original is free
// for a genuine re-signup while the row (and its audit trail) is preserved.
//
// The user id is part of the name, so two deletions of the same address in
// the same second can't collide on a unique index.
//
// Shape: deleted+<userid>@<domain> — keeping the domain makes the tombstones
// greppable by provider, and the plus-addressing form is obviously synthetic.
//
// Lives in utils rather than api because tools/backfill_tombstones has to
// produce byte-identical addresses for rows deleted before the rename existed.
func TombstoneEmail(original string, userID primitive.ObjectID) string {
	at := strings.LastIndex(original, "@")
	if at <= 0 || at == len(original)-1 {
		// Not a parseable address (or already blank) — synthesise one so we
		// still never leave the raw value in `email`.
		return fmt.Sprintf("%s%s@invalid.local", tombstonePrefix, userID.Hex())
	}
	domain := original[at+1:]
	return fmt.Sprintf("%s%s@%s", tombstonePrefix, userID.Hex(), domain)
}

// IsTombstoneEmail reports whether an address has already been renamed by
// TombstoneEmail. Used by the backfill to skip rows that are already correct,
// which is what makes re-running it safe.
func IsTombstoneEmail(email string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(email)), tombstonePrefix)
}
