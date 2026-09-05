// Command tryon_failures reads the try-on post-mortem collection.
//
// Why this exists: `tryon_failures` was write-only. Every failed generation
// was being recorded with its full inputs "for manual investigation", and the
// only way to investigate was a mongo shell and a remembered field name. A
// diagnostic nobody can read is not a diagnostic.
//
// The default view answers the question that comes first — what is failing,
// and is it our fault? — by grouping on stage and reason:
//
//	precheck  a lookup or validation died before we spent anything (ours)
//	gate      the guard or paywall turned it away (not a fault; a business number)
//	generate  the model did not return an image (the expensive one)
//	store     we produced an image and could not save it (the worst one)
//	refund    stars were held and could not be returned (somebody is owed money)
//
// Usage:
//
//	go run ./tools/tryon_failures                      # last 7 days, grouped
//	go run ./tools/tryon_failures -days 30
//	go run ./tools/tryon_failures -reason image_safety # rows for one reason
//	go run ./tools/tryon_failures -user 66f1...        # one user's history
//	go run ./tools/tryon_failures -stage refund        # who is owed stars
//
// Read-only. It never writes, so it is safe to point at production.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	days := flag.Int("days", 7, "how far back to look")
	stage := flag.String("stage", "", "filter to one stage (precheck|gate|generate|store|refund)")
	reason := flag.String("reason", "", "filter to one reason code; also switches to row output")
	user := flag.String("user", "", "filter to one user id; also switches to row output")
	limit := flag.Int("limit", 20, "rows to print when listing")
	verbose := flag.Bool("v", false, "print the full stored inputs for each row")
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	since := time.Now().AddDate(0, 0, -*days)
	filter := bson.M{"created_at": bson.M{"$gte": since}}
	if *stage != "" {
		filter["stage"] = *stage
	}
	if *reason != "" {
		filter["reason"] = *reason
	}
	if *user != "" {
		filter["user_id"] = *user
	}

	coll := utils.GetCollection(*dbName, models.CollTryOnFailures)

	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("\n%d failures in the last %d days", total, *days)
	if *stage != "" || *reason != "" || *user != "" {
		fmt.Printf(" (filtered)")
	}
	fmt.Printf("\nretention is %d days — anything older has already expired\n\n",
		int(utils.FailureRetention.Hours()/24))

	if total == 0 {
		return
	}

	// A specific reason or user is a request for the rows themselves; anything
	// else is a request for the shape of the problem.
	if *reason != "" || *user != "" {
		listRows(ctx, coll, filter, *limit, *verbose)
		return
	}
	summarise(ctx, coll, filter, total)
}

// summarise groups by stage, then reason, then model.
func summarise(ctx context.Context, coll interface {
	Aggregate(context.Context, interface{}, ...*options.AggregateOptions) (*mongo.Cursor, error)
}, filter bson.M, total int64) {
	pipeline := []bson.M{
		{"$match": filter},
		{"$group": bson.M{
			"_id": bson.M{
				"stage":  "$stage",
				"reason": "$reason",
				"model":  "$model",
			},
			"count": bson.M{"$sum": 1},
			"users": bson.M{"$addToSet": "$user_id"},
			"last":  bson.M{"$max": "$created_at"},
		}},
		{"$sort": bson.M{"count": -1}},
	}

	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("aggregate: %v", err)
	}
	defer cur.Close(ctx)

	var rows []struct {
		ID struct {
			Stage  string `bson:"stage"`
			Reason string `bson:"reason"`
			Model  string `bson:"model"`
		} `bson:"_id"`
		Count int       `bson:"count"`
		Users []string  `bson:"users"`
		Last  time.Time `bson:"last"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("decode: %v", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STAGE\tREASON\tMODEL\tCOUNT\tSHARE\tUSERS\tLAST")
	for _, r := range rows {
		share := float64(r.Count) / float64(total) * 100
		model := r.ID.Model
		if model == "" {
			model = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%.1f%%\t%d\t%s\n",
			r.ID.Stage, r.ID.Reason, model, r.Count, share, len(r.Users),
			r.Last.Local().Format("02 Jan 15:04"))
	}
	w.Flush()

	fmt.Println(`
Reading this:
  a big "gate" share is the paywall working, not an outage
  a big "generate" share of image_safety/prohibited_content means the model is
    refusing our inputs — a second vendor will refuse them too, so the fix is
    the photos, not the provider
  any "refund" row at all means a specific user is owed stars: rerun with
    -stage refund to get the hold ids`)
}

func listRows(ctx context.Context, coll interface {
	Find(context.Context, interface{}, ...*options.FindOptions) (*mongo.Cursor, error)
}, filter bson.M, limit int, verbose bool) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit))
	cur, err := coll.Find(ctx, filter, opts)
	if err != nil {
		log.Fatalf("find: %v", err)
	}
	defer cur.Close(ctx)

	var rows []models.TryOnFailure
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("decode: %v", err)
	}

	for _, f := range rows {
		fmt.Printf("──────────────────────────────────────────────────────────────\n")
		fmt.Printf("%s  %s/%s  %s  user=%s guest=%v\n",
			f.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			f.Stage, f.Reason, f.Route, f.UserID, f.IsGuest)
		fmt.Printf("  status=%d quality=%s model=%s provider=%s took=%dms\n",
			f.HTTPStatus, f.Quality, f.Model, f.Provider, f.DurationMS)
		if f.FinishReason != "" {
			fmt.Printf("  finish_reason=%s\n", f.FinishReason)
		}
		if f.RequestID != "" {
			fmt.Printf("  request_id=%s\n", f.RequestID)
		}
		for _, a := range f.Attempts {
			fmt.Printf("  attempt: %-7s %-28s %-22s %dms\n", a.Provider, a.Model, a.Reason, a.DurationMS)
		}
		if f.RawError != "" {
			fmt.Printf("  error: %s\n", f.RawError)
		}
		if f.Inputs.SpecialRequest != "" {
			fmt.Printf("  note: %q\n", f.Inputs.SpecialRequest)
		}
		if verbose {
			b, _ := json.MarshalIndent(f.Inputs, "  ", "  ")
			fmt.Printf("  inputs: %s\n", b)
		} else {
			fmt.Printf("  inputs: %d person key(s), %d garment key(s)%s\n",
				len(f.Inputs.PersonKeys), len(f.Inputs.GarmentKeys),
				map[bool]string{true: " (-v for all)", false: ""}[len(f.Inputs.PersonKeys)+len(f.Inputs.GarmentKeys) > 0])
		}
	}
	fmt.Printf("──────────────────────────────────────────────────────────────\n")
}
