# Fitly / TryOnFusion — Rate Limits & Monetization Plan

**Author:** raushankrgupta · **Date:** 2026-08-27 · **Status:** draft for decision

This document answers four questions:

1. How do I cap the free tier at 5 try-ons, properly?
2. Can I let users watch an ad to earn more quota — and how does the ad money actually reach me?
3. Can I just charge users — and how do I receive money as an **individual with no registered company**?
4. What other ways can this product make money?

Assumptions used throughout: **₹88 = $1** (Aug 2026), image generation on
`gemini-3-pro-image-preview` (what `utils/gemini_client.go` currently calls).
Every price and policy figure is sourced at the bottom — re-check them before
you sign anything, they move.

---

## 0. TL;DR — the five decisions

| # | Decision | Recommendation |
|---|---|---|
| 1 | What does "5 free" mean? | **5 lifetime free** on signup + **1/day trickle**, not 5/day. 5/day costs you ₹1,800/month per daily-active user. |
| 2 | Which model generates free-tier images? | **Move free tier off `gemini-3-pro-image-preview`** to the Flash-image tier (~3.5× cheaper). Keep Pro-image as a paid "HD" feature. This single change matters more than everything else in this document. |
| 3 | Rewarded ads for extra quota? | **Yes, but as a retention toy, not revenue.** One rewarded ad in India earns you ~₹0.13–0.35. One try-on costs you ₹3.4–12. Ads *cannot* fund generation. Price it at 3–5 ads per extra try-on and cap it at 2 extra/day. |
| 4 | Taking payments as an individual? | **Yes, no company needed.** Razorpay (or Cashfree/PhonePe PG) onboards a sole proprietor on PAN + Aadhaar + bank account. Add a free Udyam registration to make it clean. **But** — if the client is an Android/iOS app, digital credits must go through Play Billing / Apple IAP, not Razorpay. Razorpay is for the **web** checkout. |
| 5 | Best revenue per try-on? | **Affiliate commissions on the Buy button.** This product scrapes Myntra/Flipkart/Amazon products and shows the user wearing them — that is a purchase-intent machine. Estimated ₹1.5–2.3 per try-on vs ₹0.3 from ads. Aggregators (Cuelinks/EarnKaro/Admitad) sign up individuals on PAN + bank account alone. |

---

## 1. Where the code is today

Facts from the current tree, not guesses:

| Thing | Where | Current behaviour |
|---|---|---|
| Plan constants | `models/user.go:11-16` | `free`, `plus`, `pro`, `guest`; `PlanOrDefault()` falls back to `free` |
| Daily limits | `models/quota.go:22` | free **5**, plus 50, pro 0 (= unlimited), guest 1 |
| Counter storage | `models/quota.go` + `utils/quota.go` | one Mongo doc per `(user_id, YYYY-MM-DD UTC)` in `tryon_quota`, `$inc` upsert |
| Enforcement | `api/middleware.go:157` `QuotaMiddleware` | checks before handler, 429 + `"upsell": true` when exhausted; increments **only on 2xx**, and skips the bump when `X-TryOn-Cached: 1` |
| Abuse guards | `api/tryon_guard.go` | in-flight dedup, 10-min result cache, 3-failures-in-10-min throttle. All **in-process** — they break the moment you run 2 replicas |
| Status API | `api/billing_handler.go` → `GET /billing/status` | returns plan, quota, breaker state |
| Guest sessions | `api/guest_handler.go` | JWT bound to a **client-supplied** `device_id`, 1 try-on/day |
| Index | `utils/indexes.go` | unique `(user_id, date)` on `tryon_quota` — already correct |

So the free limit of 5 **already exists**. What is missing is everything that
happens *after* a user hits it: there is no credit balance, no way to grant
extra quota, no payment path, and the 429 says "Upgrade your plan" while no
plan is purchasable.

### Two holes worth naming now

- **Guest quota is trivially resettable.** `device_id` comes from the client and is trusted (the handler's own comment admits it). Reinstall, or send a random string, and you get another free try-on. At ₹12/image that is an open tap.
- **Free-tier farming.** Email signup is OTP-verified but unlimited — 10 Gmail aliases = 50 free try-ons/day. Any paid tier makes this worth someone's time.

---

## 2. The number that drives every other decision

Cost per generated try-on image:

| Model | Price/image | ₹ @ 88 | Notes |
|---|---|---|---|
| `gemini-3-pro-image-preview` (1K/2K) | $0.134 | **₹11.8** | what you run today |
| `gemini-3-pro-image-preview` (4K) | $0.240 | ₹21.1 | never for free tier |
| Flash-image tier (`gemini-2.5-flash-image` "Nano Banana") | $0.039 | **₹3.4** | **retires 2026-10-02** — migrate to Gemini 3 Flash Image |
| Input images | $0.0011 each | ₹0.10 | negligible (2–4 per request) |
| S3 store + egress | — | ~₹0.10 | negligible |

**Round numbers: ₹12 per try-on today, ₹3.4 if you move the free tier to Flash.**

Now put the free tier against it:

| Free tier design | Model | Cost per daily-active user | Per month |
|---|---|---|---|
| 5 / **day** (today) | Pro-image | ₹60/day | **₹1,800** |
| 5 / **day** | Flash-image | ₹17/day | ₹510 |
| 1 / day | Flash-image | ₹3.4/day | ₹102 |
| **5 lifetime + 1/day** | Flash-image | ₹17 once, then ₹3.4/day | ₹102 after month 1 |

For context: a typical Indian consumer-app subscription lands at ₹99–₹299/month.
**A 5/day free tier on the Pro-image model burns 6–18× your best-case ARPU.**
1,000 daily-active free users would cost you ₹18 lakh/month. This is not a
rate-limit problem, it is a survival problem.

### Recommendation

- **Free:** 5 try-ons on signup (lifetime, generous first impression) + 1/day thereafter. Flash-image model, 1K, watermarked.
- **Guest:** 1 lifetime (not 1/day) — see §8 anti-abuse.
- **Paid:** credits or subscription, Pro-image model, no watermark, 2K.

This keeps the "5 free" promise you asked for while making the marginal cost
of an idle free user ≈ ₹0.

---

## 3. Option A — Rewarded ads for extra quota

### 3.1 How it works end to end

1. You create a **Google AdMob** account (free), register the app, create a **Rewarded** ad unit. You get an ad unit ID.
2. The app SDK (`react-native-google-mobile-ads` for an Expo/RN client) preloads and shows a 15–30s video the user opted into.
3. On completion the SDK fires a client-side reward callback — **do not trust it**, it is spoofable.
4. AdMob **also** calls your server: **Server-Side Verification (SSV)**, a GET to a URL you configure in the AdMob console, carrying `ad_network, ad_unit, reward_amount, reward_item, timestamp, transaction_id, user_id, signature, key_id`.
5. Your server fetches AdMob's public keys (`https://gstatic.com/admob/reward/verifier-keys.json`), picks the key by `key_id`, ECDSA-verifies the signature over the query string up to `&signature=`, and — if valid and `transaction_id` is unseen — credits the user.
6. `transaction_id` is your **idempotency key**. Store it uniquely; a replayed callback must be a no-op.

### 3.2 How the money reaches you

- AdMob pays into your **Google AdSense/AdMob payments profile**, which an **individual in India can hold** — no company required.
- You supply tax info (Form **W-8BEN** as a non-US individual) and, for an India payment country, **GST details** if you have them.
- Payout is **monthly, ~21st, once you cross a US$100 threshold**, by **bank wire/EFT** to a bank account in your name. Below $100 it rolls over.
- You bill nobody and set up no billing — Google is the buyer of your ad inventory. There is no "setting up billing with ad providers"; the flow is Google → your bank account.
- Tax: this is **business/professional income** in your personal ITR. Foreign-currency receipts may need FIRC/FIRA paperwork from your bank; ask a CA whether it qualifies as export of services.

### 3.3 The math, and why it changes the design

India is a tier-2/3 ad market. Rewarded video clears $15–40 eCPM in the US/UK/Japan;
tier-2/3 traffic runs $3–10, and a **non-gaming utility app in India realistically
sits at $1.5–4 eCPM**.

- $1.5–4 eCPM = $0.0015–0.004 per completed view = **₹0.13–₹0.35 per ad**.
- One try-on costs you **₹3.4 (Flash)** or **₹11.8 (Pro)**.

| To fund one try-on | you need |
|---|---|
| Flash-image (₹3.4) | **10–26 completed rewarded ads** |
| Pro-image (₹11.8) | **34–90 completed rewarded ads** |

No user will watch 26 videos for one image. **Rewarded ads cannot pay for
AI image generation.** Anyone telling you "add rewarded ads to monetize" has
not multiplied these two numbers.

### 3.4 So should you still do it? Yes — with the right job description

Ads here are a **retention and conversion device**, not a revenue line:

- They keep a user who hit the wall inside the app instead of bouncing.
- They put the value of a try-on in front of them ("this is worth something") right before you show the paywall.
- The ~₹0.3 they generate offsets ~10% of the cost. Treat that as a discount on your customer-acquisition cost, not as income.

**Design it as:**

- **3 ads → 1 extra try-on** (Flash model only). Your net cost ≈ ₹3.4 − ₹0.6 = **₹2.8** per rewarded try-on.
- **Hard cap 2 rewarded try-ons/day** → worst case ₹5.6/day per ad-farming user.
- Rewarded credits **expire in 24h** so nobody stockpiles.
- Never allow rewarded ads to unlock the **Pro-image** model, HD, or watermark removal — those are the paid ladder.
- Show the paywall *after* the second rewarded ad, not before the first.

Also consider **non-rewarded** ad slots on zero-marginal-cost surfaces — a native
ad in the gallery list, a banner on the wardrobe screen. They earn less per
impression but cost you nothing and don't gate the core action.

---

## 4. Option B — Just charging money

### 4.1 First: which surface is the user paying on?

This is the fork in the road, and it is a **policy** question, not a technical one.

| Surface | What you may use | Fee |
|---|---|---|
| **Website / PWA** | Razorpay, Cashfree, PhonePe PG, UPI — anything | ~2% + GST (cards), **~0% UPI** |
| **Android app (Play Store)** | **Google Play Billing is mandatory** for in-app digital goods. India additionally allows **user-choice billing**: you may offer an alternative processor *alongside* Play Billing, for a **4 percentage-point** fee reduction | **15%** for your first $1M/yr (11% via user-choice billing in India) |
| **iOS app (App Store)** | **Apple IAP is mandatory** for consumables/credits | **15%** under the Small Business Program (<$1M/yr) |

**The trap:** dropping a Razorpay checkout into the Android app to sell credits
is a Play Policy violation and gets apps removed. Selling the *same credits* on
your **website** and having them appear in the app is fine — but you may not
*steer* users to it from inside the app in most regions (India's user-choice
billing is the sanctioned in-app alternative, and it still bills you the
reduced fee).

Note the 15% tier: everyone quotes "Apple/Google take 30%", but at your revenue
level both are **15%**. Store billing is much less punitive than folklore
suggests, and it is dramatically less work than being your own merchant.

### 4.2 Receiving money as an individual with no company — the real answer

**You do not need to register a company.** In India you are, by default, a
**sole proprietor** the moment you earn business income. That is a legal
business form, not an unregistered gap.

**What Razorpay actually needs from you** (business type: Individual / Sole Proprietor):

- Your **PAN** — for a proprietorship, the proprietor's personal PAN *is* the business PAN. No separate business PAN.
- **Aadhaar / Passport / Voter ID** as government photo ID.
- **Bank account** proof (cancelled cheque or statement) — settlements land here.
- **Website/app URL** with visible **Terms, Privacy Policy, Refund/Cancellation Policy, Contact page**. Razorpay checks these; you already serve `/legal/privacy-policy` and `/legal/terms-of-service` — **add a refund policy**, this is the most common rejection reason.
- **GST is only needed once you cross the threshold** (₹20 lakh/yr for services; ₹10 lakh in special-category states). Below that, tick "not registered".
- Timeline: **1–2 working days** with clean documents. Since Jan 2026 Razorpay has a CKYC fast-track that can activate in minutes if you already have a Central KYC record.

**Strongly recommended, still no company:** get a **Udyam (MSME) registration** —
free, online, instant, individual-eligible, needs only PAN + Aadhaar. It gives
you a business identity document that makes opening a **current account** in a
trade name straightforward. Some gateways settle only to a current account;
some accept a savings account for individuals — confirm with the provider
rather than assuming.

**Where the money lands:** Razorpay settles to your bank account on **T+2** by
default, net of fees.

**Fees to expect:**

- UPI: **effectively 0%** for merchants under India's zero-MDR mandate — and UPI will be 80–90% of your volume. This is a huge structural advantage over the 15% store cut.
- Cards / netbanking / wallets: **~2% + 18% GST on the fee**.
- International cards: ~3%.

**Tax you will owe:**

- Payments are **business income** on your personal ITR (**ITR-3**, or **ITR-4** under presumptive taxation u/s **44AD** — 6% of digital receipts deemed as profit, which is very favourable for a software business).
- **Section 194-O**: an e-commerce operator/gateway may deduct **0.1% TDS** on your gross receipts above ₹5 lakh in a financial year (5% if PAN is not furnished). It shows in Form 26AS and you claim it back — not a cost, just cash-flow.
- **GST**: register once services turnover crosses ₹20 lakh. Then 18% GST on Indian B2C sales — which you must build into your prices, not add on top.
- Get a CA once real money starts moving. The above is orientation, not advice.

### 4.3 The alternatives to Razorpay

| Provider | Individual OK? | Fees | When to pick it |
|---|---|---|---|
| **Razorpay** | Yes (proprietor) | ~2% + GST, UPI ~0 | Default for India. Best docs, UPI autopay for subscriptions. |
| **Cashfree** | Yes | similar | Good fallback; often faster onboarding. |
| **PhonePe PG** | Yes | UPI-first | If you are almost entirely UPI. |
| **Stripe India** | Needs a registered entity in practice | 2%+ | Skip for now. |
| **Paddle / Lemon Squeezy / Polar / Dodo** (merchant-of-record) | Often yes for sole traders | ~5% + $0.50 | Only if you sell **internationally** — they become the seller of record and handle global VAT/GST. Note: India-format FIRA/GST export docs are a known weak spot. |
| **UPI autopay mandate (via a PG)** | Yes | low | The right rail for ₹99–₹199/month subscriptions in India. |

**Recommendation:** Razorpay for the web, Play/Apple IAP for the apps, one
shared server-side credit ledger so a credit bought anywhere works everywhere.

### 4.4 What to actually sell, with the margins

At 15% store fee and Flash-image cost of ₹3.4:

| SKU | Price | Net after 15% store | Cost | Margin |
|---|---|---|---|---|
| 20 credits | ₹149 | ₹127 | ₹68 | **₹59 (46%)** |
| 50 credits | ₹299 | ₹254 | ₹170 | ₹84 (33%) |
| Plus — 50/day, monthly | ₹199/mo | ₹169 | usage-capped | depends on use |

On **Razorpay via UPI**, the same ₹149 pack nets you ~₹149 — margin jumps to
**₹81 (54%)**. That is the argument for having a web checkout at all.

**Danger:** the same packs priced against the **Pro-image** model (₹11.8) go
underwater — 20 credits × ₹11.8 = ₹236 cost against ₹127 net. **You cannot
sell cheap credit packs on the Pro-image model.** Either the packs run on
Flash, or the packs cost ₹399+. This is decision #2 from the TL;DR, restated
in rupees.

---

## 5. Every other monetization option, ranked for *this* product

### ⭐ 1. Affiliate commissions — the best fit, and the one you're not using

Your app scrapes a real product from Myntra/Flipkart/Amazon/TataCliq, shows
the user **wearing it**, and then… does nothing commercial. That moment is the
highest purchase-intent moment in Indian fashion e-commerce, and it is
happening inside your app.

- Add a **"Buy on <site>"** button on every try-on result and gallery item, tagged with your affiliate ID.
- Apparel commissions run roughly **4–9%** depending on program and category (verify each rate card — they change often). At an AOV of ₹1,000–1,500 that is **₹50–75 per tracked order**.
- At a conservative **3% try-on → purchase** rate: **₹1.5–2.3 revenue per try-on** — 5–15× what a rewarded ad yields, and it *covers* the ₹3.4 Flash cost at a 5–7% conversion rate.
- **Onboarding is individual-friendly:** Amazon Associates India, Flipkart Affiliate, and aggregators like **Cuelinks / EarnKaro / INRDeals / Admitad** sign up individuals on **PAN + bank account**, no company, no gateway. Aggregators are the fastest start — one signup covers Myntra, Ajio, Flipkart, Nykaa etc.
- Payouts are monthly bank transfers with TDS deducted; same ITR treatment as above.
- **This is the single highest-leverage change in this document after the model switch, and it needs no billing infrastructure at all.**

### 2. Credit packs (consumable IAP) — primary revenue

Covered in §4.4. Best fit for bursty usage; no commitment; no churn management.

### 3. Subscription (Plus / Pro) — best LTV

₹199/mo Plus (50/day, no watermark, HD) and ₹499/mo Pro. Use **UPI autopay**
on web and native store subscriptions in-app. Your `models/user.go` plan
constants already anticipate exactly this.

### 4. Rewarded ads — retention, not revenue

Covered in §3.

### 5. Non-rewarded ads on free surfaces

Native ads in gallery/wardrobe lists. Small but free money; kill them for paid users.

### 6. B2B / API access — highest revenue per unit of effort

Sell the try-on endpoint to small D2C brands and boutiques (₹5,000–50,000/mo
for N generations). Invoiced, paid by NEFT — **no payment gateway needed at
all**, which sidesteps every problem in §4.2. One brand customer can outweigh
thousands of consumers. Your scraper + try-on stack is already the product.

### 7. Shopify / WooCommerce plugin

"Try before you buy" widget for Indian D2C stores, monthly SaaS. Natural
extension of #6; `scrapers/shopify/` already exists.

### 8. Sponsored themes & brand placements

`models/theme.go` already supports themed try-ons. A brand pays to be the
"Diwali Collection" theme. Sell once real DAU exists.

### 9. Cosmetic / utility upsells (no new infra)

Watermark removal, 2K/4K export, priority queue, longer gallery retention,
batch try-on, couple/group modes as premium. These cost you nearly nothing and
convert well — bundle them into Plus rather than selling separately.

### 10. Referral credits

Not revenue — growth. "Invite a friend, both get 5 credits." Cheap CAC given
your ₹3.4 unit cost. Cap it hard.

### 11. ❌ Selling data — do not

You hold user body photos. Selling or sharing that data is a privacy and legal
catastrophe waiting to happen, and would violate DPDP Act obligations. Not a
monetization option. Mentioned only to close the door on it.

---

## 6. Recommended stack

```
Free:   5 lifetime + 1/day · Flash-image · 1K · watermarked
        ↓ (hit the wall)
Ads:    3 ads → 1 extra try-on · max 2/day · 24h expiry
        ↓ (still want more)
Buy:    20 credits ₹149 / 50 credits ₹299   [Play IAP · Apple IAP · Razorpay-web]
        ↓ (heavy user)
Plus:   ₹199/mo · 50/day · Pro-image · HD · no watermark · no ads
Pro:    ₹499/mo · unlimited-ish · everything
        ═══ running underneath all tiers ═══
Affiliate "Buy on Myntra/Amazon" on every result   ← revenue on free users too
B2B API / Shopify plugin                            ← the real business
```

---

## 7. Implementation plan

### Phase 0 — Stop the bleeding (do this first, ~1 day, zero monetization work)

Nothing below matters if a free user costs ₹1,800/month.

1. **Model routing by plan.** In `utils/gemini_client.go`, pick the model from the caller's plan instead of hardcoding `gemini-3-pro-image-preview`. Free/guest/ad-credit → Flash-image tier; plus/pro → Pro-image. Plumb plan through from `GetUserPlanFromContext`.
   - ⚠️ `gemini-2.5-flash-image` **retires 2026-10-02** — target Gemini 3 Flash Image and make the model ID an env var (`GEMINI_IMAGE_MODEL_FREE` / `_PAID`) so you can swap without a deploy.
2. **Cap resolution at 1K for free tier** ($0.134 → the 1K/2K band; never 4K).
3. **Change the free limit shape** in `models/quota.go` from `5/day` to lifetime-5 + 1/day (see Phase 1 — needs the ledger).
4. **Make the guest device_id non-trivial.** Bind the guest JWT to a hash of `device_id + a server salt`, keep a Mongo record of seen device hashes, and make guest quota **lifetime 1**, not 1/day.
5. **Cost alarm.** You already have `utils/alert` + Telegram. Add a daily digest: images generated, estimated ₹ spend, top 10 users by usage. You cannot manage what you do not measure, and this catches an abuse spike in hours rather than at the month-end bill.

### Phase 1 — Credit ledger (foundation for ads *and* payments, ~2 days)

The current `tryon_quota` day-counter cannot express "user has 7 bonus credits".
Add a ledger alongside it; do not replace it.

**New: `models/credits.go`**

```go
// CreditLedger is append-only. Balance is derived, then cached on the user
// doc for a single-read hot path.
type CreditLedger struct {
    ID        primitive.ObjectID `bson:"_id,omitempty"`
    UserID    string             `bson:"user_id"`
    Delta     int                `bson:"delta"`        // +grant / -spend
    Reason    string             `bson:"reason"`       // signup_bonus|ad_reward|purchase|refund|admin|spend
    RefID     string             `bson:"ref_id"`       // idempotency: admob transaction_id, razorpay payment_id, play purchase token
    Source    string             `bson:"source"`       // admob|razorpay|play|apple|system
    ExpiresAt *time.Time         `bson:"expires_at,omitempty"` // ad credits: +24h
    CreatedAt time.Time          `bson:"created_at"`
}

type CreditBalance struct {  // one doc per user, cached projection
    UserID    string    `bson:"_id"`
    Balance   int       `bson:"balance"`
    UpdatedAt time.Time `bson:"updated_at"`
}
```

**New: `utils/credits.go`**

- `GrantCredits(ctx, userID string, n int, reason, refID, source string, ttl *time.Duration) error` — **idempotent on `ref_id`**; a duplicate insert violating the unique index is a success, not an error.
- `ConsumeCredit(ctx, userID) (bool, error)` — atomic `findAndModify` with `balance > 0` guard.
- `ExpireCredits(ctx)` — background sweep for lapsed ad credits.

**Index additions in `utils/indexes.go`:**

```go
{"credit_ledger", mongo.IndexModel{
    Keys: bson.D{{Key: "ref_id", Value: 1}},
    Options: options.Index().SetUnique(true).SetSparse(true)}},
{"credit_ledger", mongo.IndexModel{
    Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}}},
```

**Change `QuotaMiddleware` (`api/middleware.go:157`) into an entitlement check.**
Consumption order, on a successful 2xx only (keep the existing rule — never
charge for a failed generation, and keep the `X-TryOn-Cached` skip):

```
1. daily free allowance remaining?      → consume day counter   (existing path)
2. else bonus credit balance > 0?       → consume one credit    (new)
3. else → 402/429 with a structured upsell payload
```

Make the exhausted response drive the UI:

```json
{ "error": "You're out of try-ons for today.",
  "plan": "free", "daily_remaining": 0, "credits": 0,
  "options": { "watch_ad": {"ads_required": 3, "available_today": 2},
               "packs": [{"sku":"credits_20","price_inr":149}],
               "plans": [{"id":"plus","price_inr":199}] } }
```

Extend `GET /billing/status` (`api/billing_handler.go`) to return `credits`,
`daily_remaining`, and `ads_available_today` so the app can render the wall
before the user hits it.

### Phase 2 — Rewarded ads (~2 days + AdMob review time)

**New: `api/ads_handler.go`**

- `POST /ads/reward-nonce` (auth'd) → mint a short-TTL signed nonce, return it; the app passes it to the SDK as **custom data** so the SSV callback carries it back. Prevents replaying a callback against a different user.
- `GET /ads/admob-ssv` (**public**, no auth — Google calls it):
  1. Fetch + cache `https://gstatic.com/admob/reward/verifier-keys.json` (cache with TTL; refresh on unknown `key_id`).
  2. Take the raw query string **up to but excluding** `&signature=`; ECDSA-verify with the key matching `key_id`.
  3. Reject if `timestamp` is older than ~5 minutes.
  4. Resolve `user_id` from the nonce, not from the raw param.
  5. `GrantCredits(..., refID: transaction_id, source: "admob", ttl: 24h)` — the unique index makes replay a no-op.
  6. Return **200** always on a *verified* callback (Google retries on non-2xx); 403 on a bad signature.
- Enforce **3 ads = 1 credit** server-side (count verified callbacks, grant on every third) and cap at **2 credits/day**.
- Register the route in `main.go` near `/billing/status`, and **exclude it from `AuthMiddleware`**.

**Setup checklist:** AdMob account → app registered → rewarded ad unit → SSV
callback URL set to `https://<your-domain>/ads/admob-ssv` → AdSense payments
profile with W-8BEN + bank details → app-ads.txt served from your domain root
(you already serve `./static`).

### Phase 3 — Payments

**3a. Web first (fastest path to money as an individual, ~3 days)**

1. Razorpay account, business type **Individual/Proprietor**, PAN + Aadhaar + bank + Udyam. Publish a **Refund & Cancellation policy** next to your existing legal pages (`api/legal_handler.go`) — for consumable credits: non-refundable once consumed, refundable if unused within N days. Say it plainly.
2. **New: `api/payments_handler.go`**
   - `POST /billing/order` → create a Razorpay order for a SKU, return `order_id`.
   - `POST /billing/razorpay/webhook` → **verify the HMAC-SHA256 signature against your webhook secret before parsing the body**, then on `payment.captured` call `GrantCredits(refID: razorpay_payment_id, source: "razorpay")`. Never grant credits from the client's success callback — webhook only.
3. Add `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`, `RAZORPAY_WEBHOOK_SECRET` to `config/config.go` and `.env.example`.

**3b. In-app purchases (~4 days + store review)**

- `POST /billing/iap/verify` — accepts `{platform, purchase_token, product_id}`.
  - **Play:** verify with the Google Play Developer API (`purchases.products.get`), then **acknowledge/consume** the purchase — an unacknowledged Play purchase is auto-refunded after 3 days.
  - **Apple:** verify with the App Store Server API / signed transaction JWS.
  - Grant on the verified transaction ID as `ref_id`. Same ledger, same idempotency.
- Also subscribe to **Play RTDN** (Pub/Sub) and **Apple Server Notifications** so refunds and chargebacks claw credits back.
- Individual developer accounts are fine on both stores (Play $25 one-time, Apple $99/yr).

### Phase 4 — Affiliate (~1 day, highest ROI per hour spent)

- Sign up with **Cuelinks or EarnKaro** (individual, PAN + bank) for broad Indian retailer coverage; add **Amazon Associates India** directly.
- Add `utils/affiliate.go`: `Affiliatize(productURL string) string` — a per-domain rewriter that appends your tag or wraps the URL in the aggregator's deep-link format. The scrapers already give you a canonical product URL (`utils/url_helper.go`).
- Surface a **"Buy on <site>"** CTA on the try-on result and every gallery card.
- Disclose affiliate links in your Terms — required by both the programs and basic honesty.

### Phase 5 — Subscriptions

Razorpay UPI autopay on web; native subscriptions in-app. Reuse the plan field
that already exists on `models/user.go`; add `plan_expires_at` and a daily
downgrade sweep.

---

## 8. Anti-abuse (do this alongside Phase 1 — a paid tier makes free-tier farming worth doing)

| Vector | Fix |
|---|---|
| Guest device_id spoofing | Server-salted hash; **lifetime** 1 guest try-on; persist seen hashes |
| Multi-account signup farming | Rate-limit signups per IP/day; add a device-fingerprint collection alongside `users`; require OTP (done) and consider phone verification before granting the lifetime-5 bonus |
| Rewarded ad fraud | SSV only, `transaction_id` idempotency, server-minted nonce binding, timestamp freshness, per-day cap |
| Client-forged purchases | Never trust a client receipt; webhook/server verification only |
| In-process guards break at 2 replicas | `api/tryon_guard.go` maps → **Redis** before you scale out. The file's own comment already flags this |
| Refund abuse | Play RTDN / Apple notifications → negative ledger entry |
| Prompt/upload abuse burning tokens | Size-cap uploads, reject non-person images early (a cheap pre-check is cheaper than a Pro-image generation) |

---

## 9. Sequenced roadmap

| Week | Work | Outcome |
|---|---|---|
| 1 | Phase 0 — model routing, 1K cap, guest hardening, cost alarm | Free-user cost drops ~3.5× |
| 1 | Phase 4 — affiliate links | First revenue, no infra |
| 2 | Phase 1 — credit ledger + entitlement middleware | Can grant/consume quota |
| 3 | Phase 2 — AdMob SSV | Ads path live |
| 3–4 | Phase 3a — Razorpay web checkout | Actually taking money |
| 5–6 | Phase 3b — Play/Apple IAP | In-app purchases live |
| 7+ | Phase 5 subscriptions; B2B pilot | Recurring revenue |

Phases 0 and 4 are independent of everything else and pay for themselves
immediately. Start there.

---

## 10. Open questions to resolve before building

1. **Is the client an app, a website, or both?** This determines whether Razorpay is even usable for credits, and it changes Phase 3 completely. (Guest tokens reference Expo installation IDs, which suggests a React Native app — confirm.)
2. **5 free = per day, or lifetime?** This document recommends lifetime-5 + 1/day on cost grounds. If you want 5/day, it is only viable on the Flash-tier model, and even then it is ₹510/month per daily-active user.
3. **Current actual DAU and try-ons/day?** Every projection here is per-user. Ten real users make this a hobby cost; a thousand make it an emergency.
4. **Do you have a current account, or only savings?** Affects gateway settlement setup.
5. **Are you near the ₹20 lakh GST threshold?** Changes pricing (18% must be inside the price, not on top).
6. **Confirm with a CA:** GST treatment of Play/Apple-collected sales in India, and FIRC/export-of-services treatment of AdMob dollars.

---

## Sources

Prices and policies verified 2026-08-27; all of them change — re-check before committing.

- [Gemini 3 Pro Image pricing](https://www.aifreeapi.com/en/posts/gemini-3-pro-image-preview-pricing) · [Gemini API pricing (official)](https://ai.google.dev/gemini-api/docs/pricing) · [Gemini 2.5 Flash Image pricing & retirement](https://pricepertoken.com/pricing-page/model/google-gemini-2.5-flash-image)
- [AdMob SSV — validate callbacks (Android)](https://developers.google.com/admob/android/ssv) · [Rewarded SSV overview](https://support.google.com/admob/answer/9603226) · [AdMob payment thresholds](https://support.google.com/admob/answer/2772208) · [AdMob payments & transactions](https://support.google.com/admob/answer/2772140)
- [Rewarded video eCPM benchmarks 2026](https://coinis.com/glossary/rewarded-video) · [AdMob eCPM benchmarks](https://www.playwire.com/blog/admob-ecpm-benchmarks-what-publishers-should-expect)
- [Google Play billing requirements for India](https://support.google.com/googleplay/android-developer/answer/13306652) · [User choice billing](https://support.google.com/googleplay/android-developer/answer/13821247) · [Play service fees](https://support.google.com/googleplay/android-developer/answer/112622)
- [Apple In-App Purchase types (consumables)](https://developer.apple.com/help/app-store-connect/reference/in-app-purchases-and-subscriptions/in-app-purchase-types/) · [Apple In-App Purchase](https://developer.apple.com/in-app-purchase/)
- [Razorpay KYC onboarding guide (India, 2026)](https://razorpay.com/blog/payment-gateway-kyc-onboarding-india/) · [Documents required for a payment gateway](https://razorpay.com/blog/documents-required-for-payment-gateway) · [Sole proprietorship KYC documents](https://razorpay.com/docs/x/rbl-ybl-current-account/sole-proprietorship/kyc/)
- [Section 194-O TDS on e-commerce](https://taxgarden.in/blog/tds-on-ecommerce-payments-section-194o-393-guide-india-fy-2026-27) · [Section 194-O rules & rates](https://www.bajajfinserv.in/section-194o)
- [Merchant-of-record comparison for Indian businesses](https://www.playto.so/blogs/paddle-vs-lemon-squeezy-vs-playto-pay-india) · [MoR platforms 2026](https://dodopayments.com/blogs/best-merchant-of-record-platforms)
