package api

import (
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// tombstoneEmail renames a deleted account's address so the original is free
// for a genuine re-signup while the row (and its audit trail) is preserved.
//
// The user id is part of the name, so two deletions of the same address in
// the same second can't collide on a unique index.
//
// Shape: deleted+<userid>@<domain> — keeping the domain makes the tombstones
// greppable by provider, and the plus-addressing form is obviously synthetic.
func tombstoneEmail(original string, userID primitive.ObjectID) string {
	at := strings.LastIndex(original, "@")
	if at <= 0 || at == len(original)-1 {
		// Not a parseable address (or already blank) — synthesise one so we
		// still never leave the raw value in `email`.
		return fmt.Sprintf("deleted+%s@invalid.local", userID.Hex())
	}
	domain := original[at+1:]
	return fmt.Sprintf("deleted+%s@%s", userID.Hex(), domain)
}
