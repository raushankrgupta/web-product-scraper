# Where each Google value comes from

Six env vars: three are copied out of a Google console, two you generate
yourself, and one stays empty. This is the click-path for each, what a correct
value looks like, and how to prove it works before you find out from a user who
paid and got nothing.

Facts this document assumes (they are already in the code, don't re-derive them):

| Thing | Value | Where it lives |
|---|---|---|
| Android package | `com.raushan26.tryonfusion` | `config/stars.json` → `billing.package_name` |
| iOS bundle id | `com.raushan26.tryonfusion` — declared but **no iOS build ships** | `fitly-app/app.json` |
| Backend origin | `https://www.tryonfusion.com` | RTDN push URL |

**Current state** (checked against `.env` on 2026-09-06 — the app is live on
Play, Android only, no iOS build):

| Variable | Status |
|---|---|
| `PLAY_SERVICE_ACCOUNT_JSON` | set — `fitly-billing@fitly-483317.iam.gserviceaccount.com`, parses cleanly under both godotenv and `docker compose env_file` |
| `PLAY_SERVICE_ACCOUNT_FILE` | empty, correctly — it is the alternative to the above, not a companion |
| `PLAY_RTDN_TOKEN` | set |
| `STARS_IDENTITY_PEPPER` | set |
| `GOOGLE_CLIENT_ID` | set (web client — required, see §5) |
| `GOOGLE_ANDROID_CLIENT_ID` | set |
| `GOOGLE_IOS_CLIENT_ID` | empty, correctly — no iOS build |

Sections 1–4 below are the provisioning history: read them when you need to
rotate a value or explain where it came from, not to set anything up again.

**Two different Google consoles are involved and people mix them up:**

- **Play Console** (`play.google.com/console`) — the store: products, RTDN
  setup, who is allowed to call the Publisher API, app signing certificate.
- **Google Cloud Console** (`console.cloud.google.com`) — the plumbing:
  service accounts, API enablement, Pub/Sub, OAuth client IDs.

A Play developer account is linked to exactly one Cloud project for the
Publisher API. **Do the Cloud work in the project linked to your Play account**
— Play Console → *Setup → API access* shows which one that is, and lets you
link a project if none is linked yet.

---

## 1. `PLAY_SERVICE_ACCOUNT_JSON`

**What it is:** the private key of a robot account that is allowed to ask
Google "is this purchase token real?". Every call in `utils/play_billing.go`
(`Get`, `Consume`, `Acknowledge`, `voidedpurchases.list`) authenticates with it.
Unset → `/billing/purchase` returns **503** and no purchase can ever be
credited, while the Play store still happily charges people.

**Where to get it**

1. Google Cloud Console → **APIs & Services → Library** → search
   *Google Play Android Developer API* → **Enable**. (Also enable
   *Google Play Developer Reporting API* if you plan to use it later; not
   required by this backend.)
2. **IAM & Admin → Service Accounts → Create service account**
   - Name: `play-billing-verifier`
   - Skip the optional "grant this account a role" step — the permission it
     needs is granted in Play Console, not in IAM.
   - Create.
3. Open the new account → **Keys → Add key → Create new key → JSON** →
   **Create**. A `.json` file downloads. **This is the only copy Google will
   ever give you.** Losing it means creating a new key; leaking it means
   someone can read your orders, so treat it like a password.
4. Copy the account's email — it looks like
   `play-billing-verifier@<project-id>.iam.gserviceaccount.com`.
5. Play Console → **Users and permissions → Invite new user** → paste that
   email → under *App permissions* add TryOnFusion → grant:
   - **View financial data, orders, and cancellation survey responses**
   - **Manage orders and subscriptions**

   → **Invite user**.

**What the value looks like** — the entire JSON file, inline, on one line:

```bash
PLAY_SERVICE_ACCOUNT_JSON='{"type":"service_account","project_id":"...","private_key_id":"...","private_key":"-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----\n","client_email":"play-billing-verifier@....iam.gserviceaccount.com",...}'
```

Use **single quotes**. The key contains `"` and literal `\n` sequences that
double quotes in a `.env` would mangle. To produce the one-liner:

```bash
jq -c . ~/Downloads/play-billing-verifier-abc123.json
```

**Gotchas**

- Play permissions take **up to 24 hours** to propagate. A 403 right after
  setup is expected — wait before you start debugging anything else.
- The Cloud project holding the service account must be the one linked under
  *Play Console → Setup → API access*. A key from an unrelated project
  authenticates fine and then 401s on every purchase.
- Delete the downloaded file from `~/Downloads` once it is in `.env`.

## 2. `PLAY_SERVICE_ACCOUNT_FILE`

**Leave this empty.** It is the alternative to the variable above, not an
addition: a filesystem path to the same JSON key, for setups that would rather
mount a secret file than put it in the environment. `playClient()` checks
`PLAY_SERVICE_ACCOUNT_JSON` first and only falls through to the file, so
setting both means the file is silently ignored.

You'd use it only if you moved the key into a Docker secret or a mounted
volume:

```bash
PLAY_SERVICE_ACCOUNT_FILE="/run/secrets/play-sa.json"
```

Inline JSON is the better fit for this deployment — there is nothing to mount
into the container and nothing to keep in sync across a rebuild.

---

## 3. `PLAY_RTDN_TOKEN`

**This one is not from Google — you generate it.** It is a shared secret you
invent and then put in two places: this env var, and the `?token=` on the
Pub/Sub push URL. Google pushes purchase and refund notifications to a public
URL, so without it anyone who guesses `/billing/play-rtdn` can forge a refund
and zero out someone's balance. The handler refuses **every** request when it
is unset.

**Generate it:**

```bash
openssl rand -hex 32
```

**Then wire up the Google side** (Cloud Console, same project):

1. **Pub/Sub → Topics → Create topic**, id `play-rtdn`.
2. On that topic → **Permissions / Add principal** → principal
   `google-play-developer-notifications@system.gserviceaccount.com`
   → role **Pub/Sub Publisher** → Save. *(Skipping this is the single most
   common RTDN mistake — Play refuses to save the topic without it.)*
3. **Create subscription** on the topic:
   - Delivery type: **Push**
   - Endpoint URL:
     `https://www.tryonfusion.com/billing/play-rtdn?token=PASTE_THE_HEX_HERE`
   - Leave "enable authentication" off; the `?token=` is the auth.
4. Play Console → **Monetize → Monetization setup → Real-time developer
   notifications** → paste the full topic name
   `projects/<project-id>/topics/play-rtdn` → **Save** →
   **Send test notification**.

You should see `play rtdn test notification received` in the backend logs. If
you instead see nothing, the token in the URL doesn't match the env var.

**Gotchas**

- The token is in a URL, so keep it to hex/base64url characters — no `+`, `/`,
  or `=` that would need escaping.
- Rotating it means editing the push subscription endpoint at the same time.
  Between the two edits, notifications are dropped (and Pub/Sub retries them,
  so a short gap is survivable).
- If you skip RTDN entirely, purchases still reconcile: the backend polls
  `voidedpurchases.list` hourly and re-checks pending purchases. That is up to
  an hour late for a UPI payment that settles after the app closed — which in
  India is a lot of purchases. Set it up.

---

## 4. `STARS_IDENTITY_PEPPER`

**Also not from Google — you generate it.** It salts the SHA-256 of an email
address before that hash is stored in `signup_identities`, which is how a
returning user is recognised after deleting their account (they get 1 welcome
try-on instead of 5). Storing an unsalted hash of an email would be a
rainbow-table lookup away from being the email itself; the pepper is what makes
the stored value useless to anyone who gets the database.

```bash
openssl rand -hex 32
```

**Treat it as permanent.** Changing it changes every hash, so every existing
user looks brand new and is handed a fresh 5-try-on welcome bonus. Back it up
wherever you keep your other secrets.

If unset it falls back to `JWT_SECRET` — which works, but then rotating
`JWT_SECRET` (an ordinary, expected thing to do) silently resets everyone's
signup identity. Set it explicitly.

---

## 5. `GOOGLE_ANDROID_CLIENT_ID`

**You already have this one.** It exists in `fitly-app/eas.json` (every build
profile), so the live Play build already carries it:

```bash
GOOGLE_ANDROID_CLIENT_ID="783690580464-nduedf6ko2qo4eundbssblof1st4es6l.apps.googleusercontent.com"
```

**First, the thing that confuses everyone about an Android-only app:**

> `GOOGLE_CLIENT_ID` is a **"Web application"** OAuth client, and it is
> **required** even though nothing about this app runs in a browser. "Web
> application" is an OAuth client *type*, not a deployment target. The app calls
> `GoogleSignin.configure({ webClientId })` (`src/hooks/useAuth.tsx:22`), which
> makes that client the token's **audience** — Google mints an ID token whose
> `aud` is the web client id and whose `azp` is the Android client id. Delete
> `GOOGLE_CLIENT_ID` from the backend and **every Google login on the live app
> breaks immediately with 401**.

So the two are not alternatives, and they are not interchangeable:

| Variable | OAuth client type | Appears in the token as | Required? |
|---|---|---|---|
| `GOOGLE_CLIENT_ID` | Web application | `aud` | **Yes** — login fails without it |
| `GOOGLE_ANDROID_CLIENT_ID` | Android | `azp` | Strongly recommended |

Both are clients in the same Cloud project (`fitly-483317`, project number
`783690580464`); the shared `783690580464-` prefix is that project number, and
everything after the dash is what makes them distinct.

`api/generic_auth_handler.go:939` accepts a login if **either** `aud` or `azp`
matches something in the allow-list. Setting the Android id is what stops the
check from resting on `aud` alone, and it is purely additive — widening the
allow-list cannot break a login that already works, which is why it is safe to
add to a live deployment.

**If you ever need to recreate it** (new signing key, or you lose track of which
client is which):

1. Play Console → **Test and release → Setup → App integrity → App signing** →
   copy the **SHA-1** under *App signing key certificate*. That is the
   certificate Google re-signs your AAB with, so it is the fingerprint the
   installed app actually presents — not your upload key.
2. Cloud Console → **APIs & Services → Credentials → Create credentials →
   OAuth client ID** → type **Android**, package `com.raushan26.tryonfusion`,
   paste the SHA-1.
3. Android clients have **no client secret**. The `GOOGLE_CLIENT_SECRET` in
   `.env` belongs to the web client and is used by the server-side OAuth
   callback, not by app sign-in.

A dev build signed with a different key (EAS debug keystore, `expo run:android`)
presents a different SHA-1 and therefore needs its **own** Android OAuth client
with the same package name. It still logs in fine without one, because `aud`
still matches the web client.

---

## 6. `GOOGLE_IOS_CLIENT_ID`

**Leave it empty.** There is no iOS build — `app.json` declares a
`bundleIdentifier`, but nothing ships to the App Store, and the app's own
`EXPO_PUBLIC_GOOGLE_IOS_CLIENT_ID` was a placeholder string that was never a
real client.

The backend only fails closed when **all three** client ids are empty, and two
are set, so an empty value here costs nothing.

When iOS does ship: Cloud Console → **Credentials → Create credentials → OAuth
client ID** → type **iOS**, bundle id `com.raushan26.tryonfusion`. Put the id
here and in the app as `EXPO_PUBLIC_GOOGLE_IOS_CLIENT_ID`.

---

## Putting it together

```bash
# .env on the server — all seven, as deployed
PLAY_SERVICE_ACCOUNT_JSON='{"type":"service_account","project_id":"fitly-483317",...}'
PLAY_SERVICE_ACCOUNT_FILE=""                             # §2 — stays empty
PLAY_RTDN_TOKEN="<32 bytes of hex>"                      # §3 — also in the push URL
STARS_IDENTITY_PEPPER="<32 bytes of hex>"                # §4 — permanent, never rotate
GOOGLE_CLIENT_ID="783690580464-jh8dlhl1....apps.googleusercontent.com"        # web client — REQUIRED
GOOGLE_ANDROID_CLIENT_ID="783690580464-nduedf6k....apps.googleusercontent.com" # §5
GOOGLE_IOS_CLIENT_ID=""                                  # §6 — stays empty, Android only
```

The three secrets are deliberately not written out here; this file is in git and
`.env` is not.

The same client ids live in `fitly-app/eas.json` (one copy per build profile) and
`fitly-app/.env` (local dev only — EAS profile env wins in a real build). All
four copies must agree; a stale web client id in `fitly-app/.env` breaks
`expo run:android` login with 401 while EAS builds keep working, which is a
genuinely annoying afternoon.

### Verify without paying anyone

```bash
curl -s https://www.tryonfusion.com/health | jq .billing
```

```json
{
  "play_configured": true,     // §1 parsed and a client was built
  "rtdn_configured": true,     // §3 is set
  "star_config_ver": "...",
  "star_config_date": "..."
}
```

`play_configured` only means the key parsed — it does not prove Play accepted
the permissions. For that, send the **test notification** from Play Console
(§3) and make one real purchase as a license tester (Play Console → **Setup →
License testing** → add your Gmail; the flow runs end to end without charging
you, but only from a build installed **through Play**, not a sideloaded APK).

### If something is wrong

| Symptom | Cause |
|---|---|
| `/billing/purchase` → 503 | §1 unset or unparseable |
| Purchase verification → 403 | Play Console permissions not granted, or <24h old |
| Purchase verification → 401 | Key is from a Cloud project not linked to Play |
| Refunds never claw back | §3 not set up; hourly polling still catches it late |
| RTDN test notification never arrives | `?token=` ≠ `PLAY_RTDN_TOKEN`, or the publisher role was never granted on the topic |
| Google login → 401 "Invalid Google token audience" | The token's `aud` (the **web** client id) isn't in `GOOGLE_CLIENT_ID`, or the app was built with a different web client id than the backend expects |
| Google login → 401 only on a local `expo run:android` build | `fitly-app/.env` and `eas.json` disagree — EAS profile env wins in a real build, `.env` wins locally |
| Google login → 500 in prod | All three client ids empty |
| Everyone suddenly has 5 free try-ons again | §4 was changed |

---

See also: `docs/STARS_SETUP.md` (the full go-live sequence, pricing, app-side
work) and `.env.example` (the authoritative list of every variable).
