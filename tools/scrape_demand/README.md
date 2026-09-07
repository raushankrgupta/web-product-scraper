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


## Link Import (device-side) analytics

From app 2.4.0 products arrive with `source: "link_import"` (see
`models.Product`): the user picked images in the in-app browser and the phone
uploaded them. The server never fetched the page, so there is no
`scrape_error`; what exists is `page_host` and `import_method`.

Imports per store, last 30 days:

```js
db.products.aggregate([
  { $match: { source: "link_import", created_at: { $gte: new Date(Date.now() - 30*864e5) } } },
  { $group: { _id: "$page_host", imports: { $sum: 1 }, users: { $addToSet: "$user_id" },
              methods: { $addToSet: "$import_method" } } },
  { $project: { imports: 1, methods: 1, unique_users: { $size: "$users" } } },
  { $sort: { imports: -1 } }, { $limit: 25 }
])
```

Source mix per day (how fast legacy server scraping is dying — the number
that decides when `SERVER_SCRAPE_MODE=disabled` is safe):

```js
db.products.aggregate([
  { $match: { created_at: { $gte: new Date(Date.now() - 30*864e5) } } },
  { $group: { _id: { day: { $dateToString: { format: "%Y-%m-%d", date: "$created_at" } }, source: "$source" },
              n: { $sum: 1 } } },
  { $sort: { "_id.day": 1 } }
])
```

Legacy clients that hit the disabled endpoint are recorded with
`failure_reason: "update_required"`; count them the same way.
