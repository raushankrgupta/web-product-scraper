# OpenAI image generation: costs, and where it fits

Written September 2026. **This is now implemented and shipped-but-off.** The
costing below is why it is configured the way it is; §5 is how to turn it on.

> **Status:** `OPENAI_ENABLED=false` by default. With it off the provider list
> is exactly `["gemini"]` and nothing here can be reached, so this is inert
> until someone sets a key and flips the flag.

The question this answers: Gemini occasionally fails (quota, capacity, a
timeout), and OpenAI now has a competitive image-editing model. Should we add
it, should users choose it, and what would it do to the star economy?

Short version:

1. **Gemini stays the primary on both tiers.** On price-per-image it wins
   outright at the Pro end and is close enough at the Standard end that
   switching would be churn for nothing.
2. **Add OpenAI as an internal fallback, not as a user-facing choice.** The
   product sells *Standard* and *Pro*; which vendor renders them is our
   problem, not the customer's.
3. **Only fall back on the failure reasons where a different vendor could
   plausibly succeed.** `utils.FailureReason` already produces exactly the
   codes needed to make that call, and falling back on the wrong ones doubles
   both the bill and the latency for no additional successes.
4. **`gpt-image-2` at `high` quality does not fit any existing tier.** At
   ₹21 a render it loses money at the 25★ Pro price. If you want it, it is a
   third tier at ~40★, not a fallback.

---

## 1. The models

OpenAI bills images by token, not per image. The rates ([OpenAI API
pricing](https://developers.openai.com/api/docs/pricing)):

| Model | Text in | Image in | Image out | Notes |
|---|---|---|---|---|
| `gpt-image-2` | $5.00/M | $8.00/M | $30.00/M | Current flagship |
| `gpt-image-1.5` | $5.00/M | $8.00/M | $32.00/M | Previous flagship |
| `gpt-image-1-mini` | $2.00/M | $2.50/M | $8.00/M | Cheapest |
| `gpt-image-1` | $5.00/M | $10.00/M | $40.00/M | **Retiring 23 Oct 2026 — do not build on it** |

Batch mode halves both rates but allows up to 24h of latency, so it is
irrelevant to a request a user is watching. It *is* relevant if we ever
backfill or regenerate galleries offline.

Per-image, at 1024×1024, by the `quality` parameter:

| | low | medium | high |
|---|---|---|---|
| `gpt-image-2` | $0.006 | $0.053 | $0.211 |
| `gpt-image-1.5` | $0.009 | $0.076 | $0.200 |
| `gpt-image-1-mini` | $0.005 | $0.015 | $0.052 |

For comparison, what we pay now ([Gemini API
pricing](https://ai.google.dev/gemini-api/docs/pricing)):

| | per image |
|---|---|
| `gemini-2.5-flash-image` (our Standard) | $0.039 |
| `gemini-3-pro-image-preview` (our Pro, 1K/2K) | $0.134 |

Those two figures are the `est_cost_usd` values already in
`config/stars.json`, and they are correct.

### The input images are not free

This is the part a headline price table hides, and it matters more to us than
to most callers: **a try-on is an edit, not a generation.** We send a customer
photo plus one to three garment references on every single call, and
`gpt-image-2` processes every input at high fidelity with no way to turn that
down (`input_fidelity` is rejected on that model).

At an assumed ~1,250 tokens per reference image — **this is the one number
below that is a guess and should be measured**, see §5 — three references cost
about $0.030 on `gpt-image-2` and about $0.009 on `gpt-image-1-mini`. On the
cheap tiers that is *larger than the output cost*.

Gemini charges for inputs too, but at $0.30/M (flash) and $2/M (pro) it rounds
to nothing, which is why `est_cost_usd` being output-only has never mattered.

---

## 2. What each model costs in stars

Same arithmetic as `tools/stars_check`: ₹88/$, Play takes 15%, and margins are
computed at the **cheapest star rate** (₹0.80/star, what a customer on the
1000-pack pays), so net revenue is ₹0.68 per star. "Floor" is the 1.25×
hard minimum the CI check enforces; "target" is the 2× advisory number.

| Model | Total $/image | ₹ cost | Floor | Target |
|---|---|---|---|---|
| `gemini-2.5-flash-image` | $0.039 | ₹3.43 | 7★ | 11★ |
| `gemini-3-pro-image-preview` | $0.134 | ₹11.79 | 22★ | 35★ |
| `gpt-image-1-mini` low | $0.014 | ₹1.26 | 3★ | 4★ |
| `gpt-image-1-mini` medium | $0.024 | ₹2.15 | 4★ | 7★ |
| `gpt-image-1-mini` high | $0.061 | ₹5.40 | 10★ | 16★ |
| `gpt-image-2` low | $0.036 | ₹3.17 | 6★ | 10★ |
| `gpt-image-2` medium | $0.083 | ₹7.30 | 14★ | 22★ |
| `gpt-image-2` high | $0.241 | ₹21.21 | 39★ | 63★ |
| `gpt-image-1.5` medium | $0.106 | ₹9.33 | 18★ | 28★ |
| `gpt-image-1.5` high | $0.230 | ₹20.24 | 38★ | 60★ |

Read against what we charge today (individual: 10★ Standard, 25★ Pro):

- **`gpt-image-2` medium fits inside the Pro tier** with room to spare —
  ₹7.30 against ₹17.00 of net revenue is 2.3×, better than the 1.44× that
  Gemini 3 Pro currently returns. A Pro try-on that falls back to it makes
  *more* money than one that succeeds.
- **`gpt-image-1-mini` medium fits inside the Standard tier** at 3.2×, again
  better than the 1.98× Gemini Flash returns.
- **`gpt-image-2` high fits nothing.** ₹21.21 against ₹17.00 of net revenue is
  a loss on every render. It needs 39★ to clear the floor and 63★ to be
  comfortable. That is a new tier, not a substitution.
- `gpt-image-1.5` is dominated: it costs more than `gpt-image-2` at every
  comparable quality. Ignore it.

Also worth knowing: **Gemini 3.1 Flash Image and 3.1 Flash Lite Image now
exist** ($60/M and $30/M output). If the Lite variant is good enough for
Standard it is a straight cost cut on the tier we run most often, and it is a
one-line change in `stars.json`. Worth an hour of evaluation before any of the
OpenAI work below.

---

## 3. Fallback, or let the user choose?

**Fallback. Do not put the vendor in front of the user.**

The argument for user choice is that different models flatter different
photos, and some people would enjoy picking. The arguments against are
stronger:

- **It multiplies the price list.** Today: 3 try-on types × 2 qualities = 6
  prices, and the app already spends a whole footer explaining two of them.
  Adding a vendor axis makes it 12, and every one has to be priced, margin
  checked, displayed, and kept in sync between `stars.json` and
  `src/config/stars.ts`.
- **Nobody can act on it.** "Standard · ₹10 · about 20 seconds" and "Pro ·
  ₹25 · sharper detail" are choices a customer can make. "Gemini or OpenAI?"
  is a question about our supply chain.
- **It freezes the supply chain.** The whole point of
  `config.Stars.GeminiModelFor(quality)` is that the model is a pricing
  decision we can change without shipping an app release. The moment a vendor
  name is on a radio button, changing vendors is a UX migration and probably a
  support issue.
- **It makes failure worse, not better.** A user who explicitly picked OpenAI
  and got a refusal has been let down by a choice we invited them to make.

The tier stays the product. `models.flash` and `models.pro` in `stars.json`
each grow an ordered provider list, and the first one that returns an image
wins. The customer is charged the tier price either way — which is why §2's
requirement that *every* provider on a tier clears that tier's margin floor
is not optional.

---

## 4. When to fall back — the part that decides whether this pays

A fallback is a second paid call and a second latency budget. Firing it on
every failure would roughly double the cost of a failing try-on and push the
worst case past the client's own timeout, so it has to be selective.

`utils.FailureReason` already returns the codes to select on:

| Reason | Fall back? | Why |
|---|---|---|
| `quota_exhausted` | **Yes** | Our Gemini billing is dead; OpenAI's is not. This is the case that justifies the whole feature. |
| `circuit_open` | **Yes** | We have decided Gemini is down. Fall straight through — no Gemini call at all. |
| `upstream_error` | **Yes** | Transport-level. A different vendor is a genuinely different roll. |
| `timeout` | **Only if budget remains** | See below. |
| `text_instead_of_image` | Yes | A model quirk, not a policy call. |
| `image_safety`, `prohibited_content`, `recitation`, `spii` | **No** | This is the important one. These are content refusals about the customer's photo, and **OpenAI's moderation is stricter than Gemini's on identifiable real people**, not looser. The overwhelmingly likely outcome is two refusals, two bills, and forty seconds instead of twenty. Keep the honest 422 we return today. |
| `insufficient_input_images`, `misconfigured` | **No** | Our bug. A second vendor cannot fetch an image we failed to fetch. |
| `no_image`, `blocked_other` | Judgement | Start with No; revisit once `tryon_failures` shows how often these are transient. |

That table is the reason the failure-classification work is worth having
independently of OpenAI: **`tryon_failures` will tell you what the real
distribution is before you write a line of integration code.** If 90% of
failures turn out to be `image_safety`, a fallback provider buys almost
nothing and the effort belongs in input curation instead. Run the numbers
first.

### Latency

`geminiTimeout` gives flash 45s and pro 90s, and the client waits 150s
single / 210s multi. A fallback must fit *inside* the remaining client budget,
or it converts an honest 422 into a timeout the user reads as "broken".

Reuse the pattern already in `runGemini`: `hasBudgetForRetry` checks that the
context deadline leaves room for another attempt of roughly the same length.
The fallback needs the same guard, sized from the OpenAI model's own expected
duration rather than Gemini's.

---

## 5. How to turn it on

```bash
OPENAI_API_KEY="sk-..."
OPENAI_ENABLED="true"
IMAGE_PROVIDER_PREFERENCE="gemini"   # or "openai" to lead with it
OPENAI_TIMEOUT_SECS="90"
```

`OPENAI_ENABLED` is forced off without a key, and
`IMAGE_PROVIDER_PREFERENCE=openai` is downgraded to `gemini` with a warning
when OpenAI is disabled — a preference for a provider we cannot authenticate
to would otherwise be silent, and every generation would quietly run on the
"fallback" while someone wondered why the bill never moved.

**Measure the input tokens on the first real call.** Every successful OpenAI
generation logs `input_tokens`, which is deliberate: the ~1,250 tokens/image
figure in §1 is the only estimate in §2 that is not authoritative, and it is
what decides whether `gpt-image-2` works as a Pro-tier fallback. If the real
number is materially higher, reprice `openai_est_cost_usd` in `stars.json` and
re-run `go run ./tools/stars_check`.

**Where things live:**

| | |
|---|---|
| `config/stars.json` → `models.*.openai_model` | the fallback model and quality per tier, next to that tier's price |
| `utils/openai_client.go` | the `/v1/images/edits` call |
| `utils/tryon_resolve.go` | downloads every image **once**, shared by both vendors |
| `utils/tryon_dispatch.go` | provider order, the fallback policy from §4, the budget guard |
| `tools/stars_check` | validates **every** provider against the tier price, not just the primary |
| `tools/tryon_failures` | reads the post-mortems, including per-provider attempts |

The two vendors share the prompts (`individualTryOnPrompt` /
`multiPersonTryOnPrompt`) and the failure vocabulary (`utils.FailureReason`),
so an `image_safety` refusal means the same thing and is counted the same way
whichever one produced it. A failed generation records one row with an
`attempts` array, so "Gemini refused on safety, then OpenAI timed out" is a
thing you can query rather than infer.

**API details that differ from Gemini:**

- Endpoint is `POST /v1/images/edits` (multipart), not a `generateContent`
  call. Multiple reference images go in as repeated `image[]` parts — GPT
  image models accept up to 16.
- `moderation: "low"` is the least restrictive setting available. Set it;
  the default `auto` will refuse more try-ons.
- `input_fidelity` must be **omitted** for `gpt-image-2` — it always processes
  inputs at high fidelity and rejects the parameter.
- Sizes are fixed (1024×1024, 1024×1536, 1536×1024), unlike Gemini which
  returns its own aspect. Pick portrait (1024×1536) for try-ons.
- Responses come back base64 (`b64_json`), so the existing "bytes → S3" path
  works after one decode.
- Output images carry C2PA provenance metadata. Harmless, but it means a
  downloaded image is identifiable as AI-generated — check that against
  whatever the privacy policy currently says.
- A separate `OPENAI_API_KEY`, its own `utils.Breaker` instance
  (`OpenAIBreaker`), and its own alert component, so an OpenAI outage cannot
  trip the circuit on the provider that exists to cover for a Gemini outage.
  Content refusals deliberately do **not** move that breaker: one user's photo
  being refused must not take the vendor down for everybody.
- There is no equivalent of the Gemini safety-block retry. That retry re-sends
  a stripped-down prompt on the theory that the wording tripped a classifier;
  OpenAI's refusals are moderation decisions about the images, so a second call
  with fewer words buys another bill and another refusal.

**Billing.** Nothing in `StarGateMiddleware` changes. The hold is taken for
the tier before any vendor is chosen and settled on the HTTP status, so a
fallback that succeeds commits exactly one charge and a fallback that fails
refunds exactly one. The one thing worth adding is the vendor that actually
served the request in the ledger row, so per-vendor spend is reconcilable
against the real invoices.

---

## 6. What was configured

| | Primary | Fallback | Tier price |
|---|---|---|---|
| Standard | `gemini-2.5-flash-image` | `gpt-image-1-mini` (medium) | unchanged |
| Pro | `gemini-3-pro-image-preview` | `gpt-image-2` (medium) | unchanged |

`stars_check` output with these in place — note every fallback clears the floor
by more than the primary it covers for:

```
TYPE        QUALITY  PROVIDER  MODEL                       STARS  COST    MULT
individual  flash    gemini    gemini-2.5-flash-image      10     ₹3.43   1.98x  thin
individual  flash    openai    gpt-image-1-mini (medium)   10     ₹2.11   3.22x  ok
individual  pro      gemini    gemini-3-pro-image-preview  25     ₹11.79  1.44x  thin
individual  pro      openai    gpt-image-2 (medium)        25     ₹7.30   2.32x  ok
```

Both fallbacks are *cheaper* than the primary they cover for, so this raises
blended margin on the two tiers that `stars_check` currently calls thin.

Not recommended, in order of how confident I am:

- Do not expose the vendor to users (§3).
- Do not fall back on content refusals (§4) — this is where a naive
  implementation burns money.
- Do not adopt `gpt-image-2` at `high` inside an existing tier (§2). As a new
  ~40★ "Ultra" tier it is defensible; as a substitution it is a loss.
- Do not build on `gpt-image-1`; it is retired on 23 October 2026.

**Before switching it on**, read `tryon_failures` for a couple of weeks:

```
go run ./tools/tryon_failures -days 14
```

If the `generate` stage is dominated by `image_safety` / `prohibited_content`,
a second vendor buys almost nothing — those are the failures §4 deliberately
does not fall back on — and the effort belongs in input curation instead. The
switch costs nothing to leave off while you find out.

Also still worth an hour: **Gemini 3.1 Flash Image / Flash Lite Image** now
exist and may be a straight cost cut on Standard, which is a one-line change in
`stars.json` and needs none of this.
