// Command backfill_tombstones repairs soft-deleted user rows whose email was
// never renamed to a tombstone.
//
// Why this exists: the tombstone rename landed on 2026-08-25 (commit 5ec7c39).
// Every account deleted before that day was left with status="deleted" but the
// original address still sitting in `email`, and no `deleted_email` audit copy.
// Those rows are what the admin dashboard shows as "deleted" next to a real
// gmail address. They are not just cosmetic: while the address is still held by
// a dead row, the unique index on `email` blocks a clean re-signup, and
// LoginHandler answers "Account deleted. Please sign up again" — an instruction
// the user cannot follow.
//
// SignupHandler and GoogleLoginHandler each self-heal one such row when its
// owner comes back, so this tool is the sweep for everyone who hasn't.
//
// Usage:
//
//	go run ./tools/backfill_tombstones            # dry run: report only
//	go run ./tools/backfill_tombstones -apply     # perform the rename
//	go run ./tools/backfill_tombstones -apply -uri mongodb://... -db fitly
//
// Dry run is the default on purpose: this writes to the users collection.
// Re-running is safe — rows that already carry a tombstone are skipped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func main() {
	apply := flag.Bool("apply", false, "write the changes; without it the tool only reports")
	uri := flag.String("uri", "", "MongoDB URI (defaults to MONGO_URI / .env)")
	dbName := flag.String("db", "", "database name (defaults to DB_NAME / .env)")
	flag.Parse()

	config.LoadConfig()
	if *uri == "" {
		*uri = config.MongoURI
	}
	if *dbName == "" {
		*dbName = config.DBName
	}

	if err := utils.ConnectMongo(*uri); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = utils.Client.Disconnect(ctx)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := run(ctx, utils.GetCollection(*dbName, "users"), *apply); err != nil {
		log.Fatalf("backfill: %v", err)
	}
}

func run(ctx context.Context, users *mongo.Collection, apply bool) error {
	// Every deleted row is fetched and filtered in Go rather than with a
	// `$not: /^deleted\+/` query: the population is tiny, and sharing
	// utils.IsTombstoneEmail with the handlers means the tool and the app can
	// never disagree about what counts as already-renamed.
	cursor, err := users.Find(ctx, bson.M{"status": "deleted"})
	if err != nil {
		return fmt.Errorf("find deleted users: %w", err)
	}
	var deleted []models.User
	if err := cursor.All(ctx, &deleted); err != nil {
		return fmt.Errorf("decode deleted users: %w", err)
	}

	var stale []models.User
	for _, u := range deleted {
		if !utils.IsTombstoneEmail(u.Email) {
			stale = append(stale, u)
		}
	}

	fmt.Printf("%d deleted account(s); %d still holding the original address\n\n",
		len(deleted), len(stale))
	if len(stale) == 0 {
		return nil
	}

	if !apply {
		fmt.Println("DRY RUN — re-run with -apply to write these changes:")
	}

	var failed int
	for _, u := range stale {
		tombstone := utils.TombstoneEmail(u.Email, u.ID)

		set := bson.M{
			"email":         tombstone,
			"deleted_email": u.Email,
		}
		// These rows predate `deleted_at`, so a future purge job would never
		// see them. Best available estimate is when the row was last written,
		// which for a legacy deletion is the deletion itself.
		estimated := ""
		if u.DeletedAt.IsZero() {
			at := u.UpdatedAt
			if at.IsZero() {
				at = u.CreatedAt
			}
			if !at.IsZero() {
				set["deleted_at"] = at
				estimated = fmt.Sprintf(" (deleted_at estimated as %s)", at.Format(time.RFC3339))
			}
		}

		fmt.Printf("  %s  deleted %s  %s -> %s%s\n",
			u.ID.Hex(), stamp(u.DeletedAt), u.Email, tombstone, estimated)
		if !apply {
			continue
		}

		if _, err := users.UpdateOne(ctx, bson.M{"_id": u.ID}, bson.M{"$set": set}); err != nil {
			// One bad row must not abort the sweep — a duplicate-key here
			// would mean a live account already owns the tombstone address,
			// which is worth reporting but is not a reason to leave the rest
			// of the backlog unrepaired.
			failed++
			fmt.Fprintf(os.Stderr, "    FAILED %s: %v\n", u.ID.Hex(), err)
			if !isDuplicateKey(err) {
				return fmt.Errorf("update %s: %w", u.ID.Hex(), err)
			}
		}
	}

	if apply {
		fmt.Printf("\nrenamed %d, failed %d\n", len(stale)-failed, failed)
	}
	return nil
}

// stamp formats a deletion date for the report, tolerating the legacy rows
// that never had one.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "  (unknown)  "
	}
	return t.Format("2006-01-02")
}

// isDuplicateKey reports whether err is a unique-index violation (E11000).
func isDuplicateKey(err error) bool {
	var we mongo.WriteException
	if errors.As(err, &we) {
		for _, e := range we.WriteErrors {
			if e.Code == 11000 {
				return true
			}
		}
	}
	return false
}
