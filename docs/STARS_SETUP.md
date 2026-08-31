# Star Economy — Setup Guide

Everything you need to do to take the star system live, in the order you need
to do it. Nothing here is optional unless it says so.

**Repos**
- Backend — `~/Fitly/web-product-scraper`
- App — `~/Fitly/fitly-app`

---

## 0. What was built

Users buy **stars** with real money through Google Play and spend them on
try-on generations. Prices, model mapping and free-tier rules all live in one
reviewable file: **`config/stars.json`**.

### Pricing as shipped

| Pack | Price | Stars | ₹/star |
|---|---|---|---|
| `stars_40` | ₹49 | 40 | ₹1.23 |
| `stars_150` | ₹149 | 150 | ₹0.99 |
| `stars_450` | ₹399 | 450 | ₹0.89 |
| `stars_1000` | ₹799 | 1000 | ₹0.80 |

| Try-on | Standard (flash) | Pro |
|---|---|---|
| Individual | ★10 | ★25 |
| Couple | ★14 | ★30 |
| Group | ★18 | ★36 |

### Free tier

- **5 free try-ons** on signup (Standard quality, individual only)
- **1 free try-on per day** after that (same restrictions)
- Free usage **pauses** while the balance is ≥ 10 stars (the cheapest tier),
  and resumes on its own when it drops below
- A user who deletes their account and signs up again gets **1** welcome
  try-on instead of 5, but keeps the normal 1/day
- Guests keep 1 free try-on per day

Free entitlements are a **separate counter from stars**, not a star grant. That
is deliberate: granting 50 stars instead of "5 free try-ons" would let someone
spend the welcome bonus on two Pro renders that cost us more than they are
worth.

---

## 1. Google Play Console

### 1.1 Payments profile

**Monetize → Payments profile.** Individual accounts can sell in-app products;
you do not need a company. You will need your legal name, an address Google
verifies, your PAN, and an Indian bank account. Payout threshold is $100
equivalent, paid around the 15th of the following month.

Enter your **GSTIN** if you have one. GST registration for services is
mandatory above ₹20 lakh turnover and optional below it; foreign-user revenue
is an export of services and is treated differently. **Confirm your specific
position with a CA** — this is the one part of this document I would not act on
without professional advice.

The "12 testers for 14 days" rule for new individual accounts does not apply to
you; the app is already in production.

### 1.2 Create the in-app products

**Monetize → Products → In-app products → Create product.** Make four, all of
type **Consumable**. The product IDs must match `config/stars.json` **exactly** —
a mismatch means the purchase verifies and then gets rejected as an unknown
product.

| Product ID | Name | Price (India) |
|---|---|---|
| `stars_40` | 40 Stars | ₹49 |
| `stars_150` | 150 Stars | ₹149 |
| `stars_450` | 450 Stars | ₹399 |
| `stars_1000` | 1000 Stars | ₹799 |

Set each to **Active**. Play's minimum IAP price in India is ₹10, so ₹49 is fine.

> Changing a product ID after launch orphans the SKU and strands anyone
> mid-purchase. Pick these now and leave them alone.

### 1.3 Service account (purchase verification)

The backend verifies every purchase token directly with Google. Without this,
`/billing/purchase` returns 503 and nothing can be bought.

1. **Google Cloud Console** → the project linked to your Play account →
   *IAM & Admin → Service Accounts* → **Create service account**
   (name it e.g. `play-billing-verifier`).
2. On that account: **Keys → Add key → Create new key → JSON**. Download it.
3. **Google Cloud Console → APIs & Services → Enable APIs** → enable
   **Google Play Android Developer API**.
4. **Play Console → Users and permissions → Invite new user** → paste the
   service account's email → grant **View financial data, orders, and
   cancellation survey responses** and **Manage orders and subscriptions**,
   scoped to your app.

> Permissions take up to 24 hours to propagate. If verification 403s right
> after setup, wait before debugging anything else.

### 1.4 Real-time Developer Notifications (RTDN)

This is what credits UPI payments that settle after the app closes, and what
claws back refunds. **Both matter in India** — a large share of Play purchases
there are UPI or netbanking, which come back `PENDING` and complete later.

1. **Google Cloud Console → Pub/Sub → Create topic**, e.g. `play-rtdn`.
2. Grant `google-play-developer-notifications@system.gserviceaccount.com` the
   **Pub/Sub Publisher** role on that topic.
3. Create a **push subscription** on the topic with endpoint:
   ```
   https://www.tryonfusion.com/billing/play-rtdn?token=<PLAY_RTDN_TOKEN>
   ```
4. **Play Console → Monetize → Monetization setup → Real-time developer
   notifications** → paste the topic name → **Send test notification**.
   You should see `play rtdn test notification received` in the app logs.

The `?token=` is mandatory. Google will push to any public URL, so without it
anyone who finds the endpoint can forge a refund and zero out a balance. The
handler refuses every request when `PLAY_RTDN_TOKEN` is unset.

> If Pub/Sub is more than you want to operate right now, the backend also
> polls `voidedpurchases.list` hourly and re-checks pending purchases, so
> refunds and settlements still reconcile — just up to an hour late. RTDN
> makes it immediate. Set it up.

### 1.5 License testers

**Play Console → Setup → License testing** → add your Gmail. This gives you the
real purchase flow without being charged.

---

## 2. Backend

### 2.1 New environment variables

Add to `.env` on the server (all documented in `.env.example`):

```bash
# The service account JSON from step 1.3, inline. Preferred over a file —
# nothing to mount into the container.
PLAY_SERVICE_ACCOUNT_JSON='{"type":"service_account", ... }'

# Shared secret for the RTDN push URL. Must match the ?token= in step 1.4.
PLAY_RTDN_TOKEN="$(openssl rand -hex 32)"

# Salts the email hash used for returning-user detection.
STARS_IDENTITY_PEPPER="$(openssl rand -hex 32)"
```

> **`STARS_IDENTITY_PEPPER` is effectively permanent.** Changing it makes every
> existing user look brand new, which hands them all a fresh 5-try-on welcome
> bonus. Set it once, back it up with your other secrets, never rotate it.
> If unset it falls back to `JWT_SECRET`, which works but couples two secrets
> that should be rotatable independently.

`STARS_CONFIG_PATH` is optional — see §4.

### 2.2 Deployment changes

**None required.** Specifically:

- **Docker** — `config/stars.json` is compiled into the binary with `go:embed`,
  so the existing `COPY . .` + `go build` already ships it. No volume, no new
  `COPY` line.
- **Caddy** — the catch-all `reverse_proxy app:8080` already routes
  `/billing/*` including the RTDN webhook. No change.
- **MongoDB** — the four new collections (`star_balances`, `star_ledger`,
  `star_purchases`, `signup_identities`) and their indexes are created at boot
  by `EnsureIndexes`. No migration to run.
- **No transactions needed** — every balance change is a single atomic
  `findOneAndUpdate` against one document, so this works on standalone Mongo as
  well as on a replica set.

The only real change is that **a bad `stars.json` is now fatal at boot**. That
is deliberate: every other setting degrades to a default when it is wrong, but a
typo that prices a Pro generation at 1 star instead of 25 is a silent, uncapped
bill. Validate before deploying:

```bash
go run ./tools/stars_check
```

### 2.3 New endpoints

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/billing/status` | user | Balance, free allowance, upstream health |
| GET | `/billing/catalog` | user | Packs, tier prices, quality names |
| POST | `/billing/purchase` | user | Verify a Play token and credit stars |
| GET | `/billing/ledger` | user | Transaction history |
| POST | `/billing/play-rtdn` | `?token=` | Google Pub/Sub push |

Try-on endpoints now accept `quality` (`"flash"` \| `"pro"`) and
`idempotency_key`, and return **402** with `{required, balance, shortfall,
packs}` when the user cannot pay.

`/health` gained a `billing` block showing whether Play verification and RTDN
are configured — check it after deploying.

---

## 3. App

### 3.1 New dependency

`react-native-iap@16.4.1` is installed and registered as an Expo config plugin
in `app.json`. It contains **native code**, so:

- **Expo Go will not work.** You need a development build or an EAS build.
- Rebuild the native project after pulling:
  ```bash
  npx expo prebuild --clean
  npx expo run:android
  ```

### 3.2 In-app purchases only work from a Play-installed build

This trips everyone up once. A locally-installed debug APK **cannot** complete a
purchase, even with a license tester account. To test the real flow:

1. Bump `versionCode` in `app.json` (currently `19`).
2. Build an AAB: `eas build --platform android --profile production`
3. Upload it to **Internal testing** in Play Console.
4. Install it from the Play internal-testing link on a device signed in as a
   license tester.

Static SKU `android.test.purchased` works from a debug build if you only want to
smoke-test the plumbing first.

### 3.3 What changed in the app

| File | Change |
|---|---|
| `src/config/stars.ts` | Client display catalogue + fallback prices |
| `src/services/BillingService.ts` | Play purchase flow, restore, listeners |
| `src/services/ApiService.ts` | Catalogue/purchase/ledger calls, 402 helpers, longer timeouts |
| `src/hooks/useTryOnQuality.ts` | Quality state, cost label, 402 → store routing |
| `src/components/QualitySelector.tsx` | Standard/Pro picker with star prices |
| `src/components/StarBalancePill.tsx` | Replaces `QuotaPill` (deleted) |
| `app/store.tsx` | Star store |
| `app/star-history.tsx` | Transaction history |
| `app/tryon/{individual,couple,group}.tsx` | Quality selector + billing errors |
| `app/_layout.tsx` | Opens billing at app start, restores unfinished purchases |

Client try-on timeouts were raised (150s single / 210s multi) because the Pro
tier's server budget is 90s/150s. A client timeout below the server's would
abort a generation the user has already paid for.

---

## 4. Changing prices later

Everything is in **`config/stars.json`**. Two ways to change it:

**Rebuild** (normal): edit the file, run `go run ./tools/stars_check`, deploy.

**Without rebuilding** (hotfix): put an edited copy on the server and set
`STARS_CONFIG_PATH=/path/to/stars.json`, then restart.

The app reads prices from `/billing/catalog`, so a repricing reaches users on
their next app launch **without a store release**. Only the fallback table in
`src/config/stars.ts` needs updating to match, and that is display-only — the
charge is always computed server-side.

### The margin checker

```
$ go run ./tools/stars_check

TYPE        QUALITY  STARS  NET     COST    MARGIN  MULT   VERDICT
couple      flash    14     ₹9.51   ₹3.43   ₹6.08   2.77x  ok
couple      pro      30     ₹20.37  ₹11.79  ₹8.58   1.73x  thin — 35 stars for target
group       flash    18     ₹12.22  ₹3.43   ₹8.79   3.56x  ok
group       pro      36     ₹24.45  ₹11.79  ₹12.66  2.07x  ok
individual  flash    10     ₹6.79   ₹3.43   ₹3.36   1.98x  thin — 11 stars for target
individual  pro      25     ₹16.98  ₹11.79  ₹5.19   1.44x  thin — 35 stars for target
```

Margins are computed at the **cheapest star rate** (₹0.80, what a customer on
the ₹799 pack pays) — the worst case for you. `min_margin_multiple` (1.25×) is
a hard floor that exits non-zero; `target_margin_multiple` (2.0×) only warns.

**Add this to CI** so a repricing can't ship underwater:

```yaml
- run: go run ./tools/stars_check
```

### ⚠️ The model costs are estimates

`est_cost_usd` in `stars.json` is **my estimate**, not your bill: $0.039 for
flash, $0.134 for pro. Replace both with your real numbers — take last month's
Gemini image spend from the Google AI Studio / Vertex billing console and divide
by the number of successful generations. If the real Pro cost is meaningfully
higher, `individual/pro` at 1.44× is the first tier to go underwater.

### ⚠️ Verify the flash model ID

`config/stars.json` maps the Standard tier to `gemini-2.5-flash-image`. The Pro
tier keeps `gemini-3-pro-image-preview`, which is what you were already using
for everything. **Confirm the flash model ID against current Gemini docs before
launch** — if it is wrong, every Standard generation fails. It is a one-line
config change, which is exactly why it lives in config.

---

## 5. Policy and legal

### 5.1 Terms of service

`app/settings/terms.tsx` and the served `/legal/terms-of-service` must state
that stars:

- are a consumable digital item with **no cash value**
- **do not expire**
- **cannot be transferred** between accounts
- are **not refundable once spent**
- are **forfeited when an account is deleted**

That last one is real: account deletion purges the balance (unspent stars are
gone). Say so, and consider warning the user in the deletion flow.

### 5.2 Privacy policy

You now store a **peppered SHA-256 of the email address** for every signup, and
that record deliberately **survives account deletion**. Disclose it under fraud
prevention, along the lines of:

> When you create an account we store a one-way cryptographic hash of your
> email address. We keep this hash after you delete your account, solely to
> prevent repeated abuse of free trial credits. It cannot be reversed to
> recover your email address and is not used for any other purpose.

Not disclosing it is a Play data-safety problem, and it is a fair question for a
user to ask.

### 5.3 Play listing

- Tick **"Contains ads"? No** / **"In-app purchases"? Yes**
- Update the **Data safety** form: purchase history is now collected
- The store listing should mention the price range Play auto-detects

### 5.4 Never do this

Do not add Razorpay, UPI intent, Stripe, or a payment link for stars. Play
policy requires Play Billing for digital goods consumed in the app, and this is
the most common way indie apps get suspended.

---

## 6. Test checklist

Backend, before deploying:

```bash
cd ~/Fitly/web-product-scraper
go build ./... && go vet ./... && go test ./...
go run ./tools/stars_check
```

End to end, on an internal-testing build:

- [ ] New account → shows 5 free try-ons → generate → 4 left
- [ ] Burn all 5 → next day gives 1 free → burn it → generate shows 402 → store opens with the right pack highlighted
- [ ] Buy `stars_40` as a license tester → balance +40 → history shows the purchase
- [ ] With ≥10 stars, the free daily try-on is hidden and the pill shows the balance
- [ ] Spend down below 10 → free daily reappears
- [ ] Pro tier on individual charges 25, not 10
- [ ] Force a generation failure → stars are refunded → history shows a refund row
- [ ] Kill the app mid-purchase → reopen → stars appear (restore path)
- [ ] Pay by **UPI** → confirm it goes pending → confirm stars land after settlement
- [ ] Refund the purchase in Play Console → confirm the balance is clawed back
- [ ] Delete the account → sign up with the same email → **1** welcome try-on, not 5
- [ ] Same, but with a Gmail dot/plus alias (`r.k.gupta+x@gmail.com`) → still 1
- [ ] Guest try-on still works and caps at 1/day

---

## 6b. Testing on a staging server with a sideloaded APK

This is the fast loop — it covers everything except the purchase itself. Do it
first, then do §6 properly on an internal-testing track.

### What works and what doesn't

| | Sideloaded APK + staging server |
|---|---|
| Signup, welcome credits, daily free | ✅ |
| Spending stars, Standard vs Pro | ✅ (via the dev grant endpoint) |
| 402 → store screen with shortfall | ✅ |
| Refund on failed generation | ✅ |
| Delete-and-rejoin returning grant | ✅ |
| Star history | ✅ |
| **Buying a pack** | ❌ **impossible** |
| UPI pending settlement | ❌ |
| Refund / chargeback clawback | ❌ |

**Purchases cannot work on a sideloaded build.** Google Play only sells to an
app installed from a Play track, signed with the matching key. A locally-built
APK gets `BILLING_UNAVAILABLE` or an empty product list. There is no flag or
workaround for this.

> The static SKUs (`android.test.purchased`) *do* respond on a sideloaded
> build, but they return a fake token like
> `inapp:com.raushan26.tryonfusion:android.test.purchased` which our server
> correctly rejects — it asks Google about the token and Google has never
> heard of it. Useful for confirming the app-side sheet opens; useless for
> testing crediting.

### Serve the staging backend over HTTPS, not `ip:port`

A raw `http://<ip>:8080` will fail for two reasons:

1. **Android blocks cleartext HTTP** since API 28. Every request dies with
   `CLEARTEXT communication not permitted`.
2. **RTDN needs a valid HTTPS certificate**, so a plain IP can never receive
   Play notifications.

The cheapest fix is a Cloudflare tunnel (you already use `cloudflared` for
server B):

```bash
cloudflared tunnel --url http://localhost:8080
# → https://random-words-1234.trycloudflare.com
```

Real certificate, no Android config change, and RTDN-capable later. Note that a
*quick* tunnel's hostname changes on every restart — the same thing that broke
`SERVER_B_SCRAPE_URL` in production — so use a named tunnel if the staging box
is going to live for more than an afternoon.

If you insist on `ip:port`, you must additionally allow cleartext:

```bash
npx expo install expo-build-properties
```
```json
["expo-build-properties", { "android": { "usesCleartextTraffic": true } }]
```

Do not ship that to production.

### Google Sign-In will fail on a local debug build

Your Android OAuth client is registered against the **release** signing
certificate's SHA-1. A locally-built APK is signed with the debug keystore, so
Google returns `DEVELOPER_ERROR`. Two options:

- **Use email/password signup for testing** (simplest, and it exercises the
  same welcome-grant path), or
- Register the debug keystore's fingerprint as an additional Android OAuth
  client in Google Cloud Console:
  ```bash
  keytool -list -v -keystore ~/.android/debug.keystore \
    -alias androiddebugkey -storepass android -keypass android | grep SHA1
  ```

### Staging server setup

Give it its **own database** so test accounts never touch production data:

```bash
DB_NAME="fitly_staging"
ENVIRONMENT="staging"          # required — unlocks /internal/stars
INTERNAL_API_SECRET="$(openssl rand -hex 32)"
STARS_IDENTITY_PEPPER="$(openssl rand -hex 32)"

MONGO_URI="..."                # can be the same cluster, different DB
GEMINI_API_KEY="..."           # real key — generations cost real money
JWT_SECRET="..."
AWS_ACCESS_KEY_ID="..."        # S3 init is fatal at boot; these are required
AWS_SECRET_ACCESS_KEY="..."
AWS_BUCKET_NAME="..."
RESEND_API_KEY="..."           # needed for signup OTP emails
```

`PLAY_SERVICE_ACCOUNT_JSON` can be left empty on staging — `/billing/purchase`
will return 503, which is the correct behaviour when purchases can't be
verified.

### Build the APK against it

```bash
cd ~/Fitly/fitly-app
EXPO_PUBLIC_API_BASE_URL="https://your-tunnel.trycloudflare.com" \
  npx expo run:android --variant release
```

Or add a `staging` profile to `eas.json` with that `EXPO_PUBLIC_API_BASE_URL`
and run `eas build -p android --profile staging --local`.

### The dev grant endpoint

`POST /internal/stars` — only registered when `ENVIRONMENT != "prod"`, and
requires `X-Internal-Secret`. It is how you test paid paths without buying.

```bash
API=https://your-tunnel.trycloudflare.com
SECRET=<INTERNAL_API_SECRET>

# See a user's balance and identity state
curl -sX POST $API/internal/stars -H "X-Internal-Secret: $SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"action":"inspect","email":"you@example.com"}' | jq

# Give yourself 500 stars
curl -sX POST $API/internal/stars -H "X-Internal-Secret: $SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"action":"grant","email":"you@example.com","stars":500}' | jq

# Drop to 5 stars to test the 402 / store flow on a 10-star tier
curl -sX POST $API/internal/stars -H "X-Internal-Secret: $SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"action":"reset","email":"you@example.com"}'
curl -sX POST $API/internal/stars -H "X-Internal-Secret: $SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"action":"grant","email":"you@example.com","stars":5}' | jq

# Back to a brand-new account (welcome grant re-arms)
curl -sX POST $API/internal/stars -H "X-Internal-Secret: $SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"action":"reset","email":"you@example.com"}'

# Forget the email entirely, so the next signup gets the FULL 5 credits
# again rather than the returning 1. Without this you can only test the
# returning path once, ever, with your own address.
curl -sX POST $API/internal/stars -H "X-Internal-Secret: $SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"action":"forget","email":"you@example.com"}'
```

Every grant writes a ledger row marked `adjustment`, so a hand-adjusted balance
is never indistinguishable from an earned one.

> **This endpoint mints currency.** It is gated twice — `ENVIRONMENT != "prod"`
> *and* the shared secret — but never set `ENVIRONMENT=staging` on the
> production box.

### Suggested staging run

1. Sign up with email/password → expect 5 free try-ons
2. Generate 5 individual/Standard → free credits hit 0
3. Generate again → daily free covers it → generate again → **402 → store opens**
4. `grant` 5 stars → still 402 (below the 10-star tier), free still suppressed? no —
   below threshold, so the daily free returns tomorrow. Confirm the pill copy is honest.
5. `grant` to 500 → pill shows ★500, free try-on disappears (suppression)
6. Generate Standard individual → −10. Pro → −25. Couple Pro → −30. Group Pro → −36
7. Break generation (bad `GEMINI_API_KEY`) → confirm the stars come back and
   the history shows a `refund` row
8. Delete the account in-app → sign up again with the same email → **1** credit
9. `forget` the email → sign up again → **5** credits
10. Check `db.star_balances.find({"held.0": {$exists: true}})` is empty

---

## 7. Operating it

**Watch these Telegram alerts:**

| Alert | Meaning | Action |
|---|---|---|
| `failed to refund star hold` | A user paid for a generation that failed | Refund by hand — the log has the user and amount |
| `failed to consume a credited purchase` | Play may auto-refund it in 3 days | Reconciler retries hourly; investigate if it repeats |
| `purchase token replayed by a different account` | Someone is sharing purchase tokens | Check the two accounts |
| `ledger write failed` | Balance moved without an audit row | Rare; reconcile manually |
| `commit found no matching hold` | A hold was swept while its generation was still running | Raise `billing.hold_expiry_minutes` |

**Useful queries:**

```js
// A user's full history
db.star_ledger.find({user_id: "<id>"}).sort({created_at: -1})

// Revenue this month
db.star_purchases.aggregate([
  {$match: {state: "credited", credited_at: {$gte: ISODate("2026-08-01")}}},
  {$group: {_id: "$product_id", n: {$sum: 1}, stars: {$sum: "$stars"}}}
])

// Suspected welcome-bonus farming
db.signup_identities.find({deleted_count: {$gte: 2}}).sort({signup_count: -1})

// Stuck holds (should be empty; the sweeper runs every 5 min)
db.star_balances.find({"held.0": {$exists: true}})
```

---

## 8. Order of operations

1. Play Console: payments profile, four products, service account, RTDN
2. Backend: add the three env vars, deploy, check `/health` shows
   `billing.play_configured: true`
3. App: bump `versionCode`, EAS build, upload to **Internal testing**
4. Work the test checklist as a license tester
5. Update terms, privacy policy and the Data safety form
6. Promote to production
