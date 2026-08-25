# Which sites are users actually pasting?

`api/handler.go` writes every failed scrape to `products` with
`status: "failed"` and a `scrape_error`. That collection is the only honest
record of demand for new adapters — it is what users tried, not what we
guessed they would try.

Run this against production Mongo to rank the domains worth writing an
adapter for, before writing one:

```js
// mongosh "$MONGO_URI"
use fitly

db.products.aggregate([
  { $match: { status: "failed" } },
  { $addFields: {
      host: {
        $arrayElemAt: [
          { $split: [
              { $arrayElemAt: [ { $split: ["$url", "://"] }, 1 ] },
              "/" ] },
          0
        ]
      }
  }},
  { $group: {
      _id: "$host",
      attempts: { $sum: 1 },
      users:    { $addToSet: "$user_id" },
      last:     { $max: "$created_at" },
      reasons:  { $addToSet: { $substrCP: ["$scrape_error", 0, 40] } }
  }},
  { $project: {
      attempts: 1, last: 1, reasons: 1,
      unique_users: { $size: "$users" }
  }},
  { $sort: { attempts: -1 } },
  { $limit: 25 }
])
```

Read the result this way:

- **`scraper_not_found`** — the domain never reached an adapter. Since the
  generic JSON-LD/OpenGraph fallback landed, this should be rare; when it
  does appear the URL is usually malformed, not the site.
- **`scrape_failed`** — routing worked but extraction didn't. Check which
  adapter ran (the `adapter=` field on the scrape log line): `generic` failing
  means the site needs a real adapter; a named adapter failing means that
  adapter's selectors have rotted.

Rank by `unique_users`, not `attempts` — one user retrying six times is not
six users wanting a site.
