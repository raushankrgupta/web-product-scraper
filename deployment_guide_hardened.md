# Deployment Guide — Hardened (post-security-audit)

Supersedes `deployment_guide_local_build.md`. That file is still accurate for the
"build locally, push to Docker Hub" escape hatch (Appendix A below), but it
predates the security changes and misses every configuration step they require.

**Read §1 before you deploy.** Three of the audit fixes change runtime behaviour
in ways that will break the app if the environment is not updated first.

---

## 1. Breaking changes in this release

| # | Change | Breaks if you don't act |
|---|--------|-------------------------|
| 1 | `GoogleLoginHandler` now fails closed when no audience is configured | Every Google sign-in returns **500** in production unless `GOOGLE_CLIENT_ID` is set |
| 2 | `/internal/*` routes now require `ENABLE_DEV_ROUTES=true` **and** a non-prod `ENVIRONMENT` | Dev star-minting and alert-test endpoints 404 (intended) — but staging boxes that relied on them need the flag |
| 3 | `IsProd()` now matches `production` as well as `prod` | A box set to `ENVIRONMENT=production` was previously treated as **non-prod** and was serving `/internal/stars` publicly. It is now correctly prod |
| 4 | `ValidateToken` rejects everything when `JWT_SECRET` is empty | All authenticated routes 401 if the secret is missing (previously they *accepted* forged tokens — this is the fix) |
| 5 | Container runs as non-root `appuser` (UID 1000) | Any host bind-mount into `/app` owned by root becomes unwritable |
| 6 | Outbound scraper fetches are blocked to private/loopback/metadata IPs | `SERVER_B_SCRAPE_URL` pointing at a private VPC address will now be refused |

> The six regressions the remediation introduced have since been corrected —
> see `§7`, which now records what was wrong and what the fix was. Verify §7.2
> and §7.3 against your data during the first deploy: both changed how money
> and stored objects are handled.

---

## 2. Environment variables

### 2.1 New / newly-mandatory

Add these to the `.env` on the EC2 box (`~/web-product-scraper/.env`).
`.env.example` now documents all three.

```bash
# Explicit gate for /internal/* dev routes. Must be absent or "false" in prod.
ENABLE_DEV_ROUTES=false

# At least one of the three must be set in production, or every Google login
# is refused with a 500. The token's aud AND azp are both checked against the
# set, so an Android ID token minted against your server client matches on aud.
# Set the mobile ids too if your clients mint tokens against them directly.
GOOGLE_CLIENT_ID="<web-or-backend-client-id>.apps.googleusercontent.com"
GOOGLE_ANDROID_CLIENT_ID="<android-client-id>.apps.googleusercontent.com"
GOOGLE_IOS_CLIENT_ID="<ios-client-id>.apps.googleusercontent.com"
```

### 2.2 Correct variable names

The remediation report lists several variable names that **do not exist in this
codebase**. Use the left column, not the right:

| Correct (what `config/config.go` reads) | Wrong name in the report |
|---|---|
| `PLAY_SERVICE_ACCOUNT_JSON` | ~~`GOOGLE_PLAY_SERVICE_ACCOUNT_JSON`~~ |
| `PLAY_SERVICE_ACCOUNT_FILE` | ~~`GOOGLE_APPLICATION_CREDENTIALS`~~ |
| `GOOGLE_ANDROID_CLIENT_ID` | ~~`GOOGLE_WEB_CLIENT_ID`~~ |
| `AWS_BUCKET_NAME` | (correct) |

Setting the wrong names silently disables Play billing — `playClient()` returns
`ErrPlayBillingDisabled` and every purchase submission fails.

### 2.3 Verify the secret is strong

`config.LoadConfig()` now logs a warning (it does **not** refuse to start) when
`JWT_SECRET` is under 32 characters in prod. Check for it after deploy:

```bash
docker compose logs app | grep "SECURITY WARNING"
```

To rotate:

```bash
openssl rand -base64 48
```

Rotating `JWT_SECRET` invalidates every issued token — all users are logged out.
Note that `STARS_IDENTITY_PEPPER` falls back to `JWT_SECRET` when unset, and
changing *that* resets every user's signup-identity state. Set
`STARS_IDENTITY_PEPPER` explicitly to a separate value **before** rotating the
JWT secret, or you will hand out welcome bonuses again.

### 2.4 Pre-deploy env check

Run on the server before deploying:

```bash
cd ~/web-product-scraper
for v in JWT_SECRET MONGO_URI GOOGLE_CLIENT_ID AWS_BUCKET_NAME AWS_REGION \
         STARS_IDENTITY_PEPPER ENVIRONMENT; do
  grep -q "^$v=" .env && echo "ok   $v" || echo "MISS $v"
done
grep -E '^(ENABLE_DEV_ROUTES|ENVIRONMENT)=' .env
awk -F= '/^JWT_SECRET=/{gsub(/"/,"",$2); print "JWT_SECRET length:", length($2)}' .env
```

`JWT_SECRET length` must be ≥ 32. `ENVIRONMENT` must be `prod` or `production`.
`ENABLE_DEV_ROUTES` must be `false` or absent.

---

## 3. Reverse proxy — required, not optional

This is the single most important infrastructure change. The guest rate limiter
now trusts `CF-Connecting-IP` first, then `X-Real-IP`, then `X-Forwarded-For`.
**Caddy currently sets none of them in a way the app can trust**: `reverse_proxy`
*appends* to an incoming `X-Forwarded-For` rather than replacing it, and never
sets `X-Real-IP` or strips `CF-Connecting-IP`. As shipped, anyone can send
`CF-Connecting-IP: 1.2.3.4` and bypass guest rate limiting entirely.

Edit `Caddyfile` — replace the bare `reverse_proxy app:8080` with:

```caddyfile
    reverse_proxy app:8080 {
        # Overwrite, never append — a client-supplied value must not survive.
        header_up X-Forwarded-For {remote_host}
        header_up X-Real-IP       {remote_host}
        # This box is not behind Cloudflare. Strip the header the app trusts most.
        header_up -CF-Connecting-IP
    }
```

While you are in the file, add HSTS to the existing `header` block:

```caddyfile
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
```

**If you later put Cloudflare in front**, remove the `header_up -CF-Connecting-IP`
line and instead restrict the EC2 security group to Cloudflare's published IP
ranges — otherwise the header is spoofable by anyone who reaches the origin
directly.

### 3.1 Lock the origin

`docker-compose.yml` already keeps port 8080 off the host (only Caddy publishes
80/443). Confirm the security group matches:

```bash
# Should show only 80 and 443 open to 0.0.0.0/0, and 22 to your IP only.
aws ec2 describe-security-groups --group-ids <SG_ID> \
  --query 'SecurityGroups[].IpPermissions[].{port:FromPort,cidr:IpRanges[].CidrIp}'
```

---

## 4. AWS hardening

### 4.1 Enforce IMDSv2

The SSRF fix blocks the app from reaching `169.254.169.254`, but any other
process on the box (or a future dependency) can still read instance credentials
over IMDSv1. Enforce v2:

```bash
aws ec2 modify-instance-metadata-options \
  --instance-id <INSTANCE_ID> \
  --http-tokens required \
  --http-put-response-hop-limit 1 \
  --http-endpoint enabled
```

`--http-put-response-hop-limit 1` is what stops a container from reaching IMDS
through the docker bridge. Verify from inside the container — it must fail:

```bash
docker compose exec -T app wget -qO- --timeout=3 http://169.254.169.254/latest/meta-data/ \
  && echo "IMDSv1 STILL REACHABLE" || echo "blocked (expected)"
```

### 4.2 S3 bucket policy

Uploads now get UUID object keys, so key collision and path traversal are gone,
but the bucket policy is still yours to set:

```bash
aws s3api put-public-access-block --bucket "$AWS_BUCKET_NAME" \
  --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

aws s3api get-bucket-cors --bucket "$AWS_BUCKET_NAME"
```

CORS must not allow `PUT`/`POST` from `*`. Uploads go through the backend's IAM
credentials only — the client never writes to S3 directly.

---

## 5. Deploy

### 5.1 Normal path (GitHub Actions)

Pushing to `master` triggers `.github/workflows/deploy.yml`, which SSHes in,
pulls, tags a rollback image, and runs `docker compose up -d --build`. Nothing
about that changes — but the **first** deploy after this release must be done
manually so you can watch it, because the image now switches to a non-root user.

### 5.2 First hardened deploy (manual, on the server)

```bash
ssh -i "tryon.pem" ubuntu@<EC2_HOST>
cd ~/web-product-scraper

# 1. Snapshot for rollback.
docker tag phikarnot/web-product-scraper:latest \
           phikarnot/web-product-scraper:pre-hardening-$(date +%Y%m%d-%H%M%S)

# 2. Pull source and apply the Caddyfile change from §3 if not already committed.
git pull origin master

# 3. Build and start.
export APP_VERSION=$(git rev-parse --short HEAD)
docker compose up -d --build --remove-orphans

# 4. Reload Caddy separately so a bad Caddyfile doesn't take the site down.
docker compose exec -T caddy caddy validate --config /etc/caddy/Caddyfile \
  && docker compose restart caddy
```

### 5.3 Post-deploy verification

```bash
# Runs as appuser, not root.
docker compose exec -T app id
# -> uid=1000(appuser) gid=1000(appuser)

# Health.
curl -fsS https://tryonfusion.com/health

# Dev routes must be gone.
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://tryonfusion.com/internal/stars
# -> 404

# Client IP is not spoofable.
curl -s -H 'CF-Connecting-IP: 1.2.3.4' -H 'X-Forwarded-For: 5.6.7.8' \
     https://tryonfusion.com/health -o /dev/null -w '%{http_code}\n'
docker compose logs app --tail 20 | grep -i 'client_ip\|guest'
# The logged IP must be your real address, not 1.2.3.4 or 5.6.7.8.

# Upload validation rejects non-images. ($TOKEN = a valid JWT.)
printf 'not an image' > /tmp/x.jpg
curl -s -F 'images=@/tmp/x.jpg' -F 'name=t' -H "Authorization: Bearer $TOKEN" \
     https://tryonfusion.com/persons | head -c 200
# createPerson logs "rejected invalid image upload" and stores no image.

# SSRF policy is active on the scrape path.
curl -s -X POST https://tryonfusion.com/product/details \
     -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"url":"http://169.254.169.254/latest/meta-data/"}' | head -c 200
# -> rejected: "access to internal/metadata host ... is blocked"
```

Then exercise the real user paths, because these are the ones the audit fixes
touched most and are most likely to regress:

1. **Sign up with a new email → verify OTP.** This was broken by the audit fix
   and repaired in §7.1 — it is the single most important path to retest.
2. **Forgot password → OTP → reset.**
3. **Google sign-in** on a fresh install.
4. **Buy the smallest star pack**, confirm the balance moves exactly once.
5. **Run a try-on to completion**, confirm one debit in the ledger.
6. **Delete an account**, confirm the persons/wardrobe/tryons records follow.

### 5.4 Rollback

```bash
docker tag phikarnot/web-product-scraper:pre-hardening-<TS> \
           phikarnot/web-product-scraper:latest
docker compose up -d --no-build
```

---

## 6. Post-deploy monitoring (first 24h)

```bash
# Config warnings.
docker compose logs app | grep -i 'SECURITY WARNING'

# The two most likely regressions from this release.
docker compose logs app --since 24h | grep -c 'Too many failed attempts'
docker compose logs app --since 24h | grep 'late settling after sweep'

# SSRF false positives — a legitimate retailer being blocked.
docker compose logs app --since 24h | grep 'SSRF policy'

# Rejected uploads. A spike means the magic-byte check is too strict
# (e.g. HEIC from iOS, which is NOT in the allowlist).
docker compose logs app --since 24h | grep 'rejected invalid'
```

`ValidateImageFile` accepts **only** JPEG, PNG, and WebP. iOS shares HEIC by
default in some flows; if the app does not transcode client-side, those uploads
now fail with a 400 that users will read as "the app is broken."

---

## 7. Regressions the remediation introduced — all fixed

The audit fixes themselves carried six defects. All are corrected in the working
tree; this section records what was wrong so the behaviour change is traceable
and so the same mistakes are recognisable if the audit is re-run.

### 7.1 CRITICAL — new signups could not verify their OTP

`api/generic_auth_handler.go` filtered on `otp_attempts: {$lt: 5}`, but
`models/user.go` tags the field `bson:"otp_attempts,omitempty"`, so
`SignupHandler`'s struct insert omitted it entirely when it was zero. MongoDB's
`$lt` does not match a missing field, so `FindOneAndUpdate` returned
`ErrNoDocuments` on the *first* attempt — the handler burned the OTP and
answered *"Too many failed attempts. Please request a new OTP."* Requesting a new
one did not help: only `ForgotPasswordHandler` ever wrote `otp_attempts: 0`.

**Fixed.** The attempt allowance now lives in one place — `consumeOTPAttempt`
and `burnOTP` — with a filter that tolerates the absent field:

```go
"$or": []bson.M{
    {"otp_attempts": bson.M{"$lt": maxOTPAttempts}},
    {"otp_attempts": bson.M{"$exists": false}},
},
```

Two related repairs in the same change: a *successful* verification no longer
leaves the counter incremented (the password-reset branch resets it, since the
OTP stays live for the subsequent reset call), and `ResetPasswordHandler` —
which the audit left on the original non-atomic read-check-increment — now uses
the same helper. `api/otp_attempts_test.go` pins the marshalling behaviour that
caused this, so the `$exists` arm cannot be removed silently.

### 7.2 HIGH — late settlement debited nothing, and could double-charge

`utils/stars.go` reused the `inc` map, which holds only `lifetime_generations`
and `lifetime_spent_stars`. The balance field is `stars`, and it was never
touched — so the branch wrote a ledger row claiming `Delta: -r.Amount` against a
balance that had not moved. Worse, the branch was reachable by a plain retry: a
second `CommitReservation` for an already-committed hold finds no `held.id`,
falls through, and would have charged again.

**Fixed.** The debit is now real, and both settle paths record the hold id in a
bounded `settled_holds` ring on the balance document — the same idiom
`credited_tokens` already uses for purchases:

```go
"$inc": bson.M{
    "stars":                -r.Amount,
    "lifetime_generations": 1,
    "lifetime_spent_stars": r.Amount,
},
```

The late debit is conditional on `settled_holds` not already naming the hold, so
a retry settles exactly once.

`settled_holds` means "this hold reached a terminal state." A handler-driven
`ReleaseReservation` (the generation failed) marks it, so a stray commit
afterwards cannot charge for an image the user never got. The expiry **sweeper**
deliberately does not mark it — a swept hold is provisional, the generation may
still be running, and letting a late commit settle it is the entire point of
that path. Watch `grep 'late settling after sweep'` after
deploy — a steady stream means `hold_expiry_minutes` is too tight for your
current generation latency, which is the condition the audit was pointing at.

### 7.3 MEDIUM — account deletion purge was a no-op

Three separate problems in `utils/user_cleanup.go`:

- It wrote to a collection named `persons`. The real one is **`person`**
  (`api/tryon_handler.go`, `utils/indexes.go`), so the person cascade matched
  zero documents.
- It set `is_deleted` on wardrobe rows, but no wardrobe query filters on that
  flag — deleted users' wardrobe items kept being returned.
- AUDIT-016 was about **S3 object deletion** for GDPR Article 17 and Play Data
  Safety. No `DeleteObject` call existed anywhere. The compliance finding was
  marked FIXED without being addressed.

**Fixed.** `utils.DeleteObjectsFromS3` batches deletes at the 1000-key API limit
and collects per-key failures instead of aborting. `PurgeUserData` now collects
keys from `person.image_paths` and the three try-on image fields, deletes them,
soft-deletes persons and try-ons, and hard-deletes wardrobe rows.

The important detail is `purgeablePrefixes`. Only `person_images/`,
`generated_images/`, and `product_uploads/` are deletable. **Wardrobe images are
deliberately excluded**: they come from the scrape path, where one retailer photo
is shared by every user who saved that product, so deleting on one account's
closure would blank it out for everyone else. Absolute URLs are skipped for the
same reason. `utils/user_cleanup_test.go` covers the allow-list.

`DeleteAccountHandler` runs the purge on its own 60-second context — the
surrounding `ctx` is a 10s budget sized for a single update — and raises a
`privacy` alert on failure rather than discarding the error. The purge is
idempotent, so the alert is a prompt to re-run it.

### 7.4 MEDIUM — referral counters inflated on failure

`utils/rewards.go` incremented `stars_earned` at reservation time, before the
referrer grant was attempted. When the grant failed (the path that logs
`"referral referrer grant failed after redemption was recorded"`), nothing put it
back, so the referrer's displayed total drifted up. The compensating `$inc: -1`
writes on the self-referral and duplicate paths also had to undo two fields
non-atomically.

**Fixed.** The atomic reservation now claims only the `redemptions` slot — which
is the thing actually being raced — and `stars_earned` moves after the grant
succeeds. A single `releaseSlot` closure handles both bail-out paths and logs
rather than discarding its error. A failure now leaves the counter *low*, which
is the safe direction.

### 7.5 MEDIUM — `PUT /persons/{id}` skipped upload validation

`createPerson` got magic-byte validation and UUID keys; `updatePerson`, twenty
lines below in the same file, kept building S3 keys from the raw client filename
and trusting the client `Content-Type`. **Fixed** — it now uses
`utils.ValidateImageFile` in the same scoped closure, which also closes the file
descriptor per iteration.

### 7.6 LOW — SSRF bypass via the browser strategies

`FetchDocumentHTTP` validated the URL, but `FetchDocumentChromeDP` and
`FetchDocumentSelenium` did not — and a headless browser resolves DNS and opens
sockets itself, entirely outside the Go transport where `SafeDialerControl`
lives. Strategy 1 refusing a blocked address (as it should) simply fell through
to Strategy 2, which fetched it.

**Fixed.** `ValidateSafeURL` now runs once at the top of `FetchDocument`, before
any strategy. Two smaller repairs in `utils/url_helper.go` alongside it:

- `net.LookupIP` took no context and ran on the request path. It is now
  `net.DefaultResolver.LookupIPAddr` under a 5-second timeout.
- The host check rejected any hostname *containing* the substring `metadata`,
  which would block legitimate retailer domains. It now matches the metadata
  hosts exactly; the numeric addresses are already covered by `IsRestrictedIP`
  once the host resolves.

One residual gap, unchanged and worth knowing: `NormalizeProductURL` resolves
the host, and the browser resolves it again when a fallback strategy runs. A DNS
rebinding attacker can still win that second lookup. Closing it properly means
pinning the resolved IP through the fetch, which the browser drivers do not
expose. The Go HTTP strategy — the one that succeeds for almost all traffic — is
not affected, because `SafeDialerControl` re-checks at connect time.

### 7.7 Verification

```
go build ./...     ok
go vet ./...       ok
gofmt -l .         clean
go test ./...      ok (all packages)
govulncheck ./...  0 vulnerabilities affecting the code
```

Two new test files were added for the root causes the existing suite could not
have caught: `api/otp_attempts_test.go` and `utils/user_cleanup_test.go`.


## Appendix A — Local build & push (low-disk fallback)

Use when the EC2 box cannot build (`no space left on device`). This is the old
`deployment_guide_local_build.md` flow, corrected: `docker-compose.yml` already
has both `image:` and `build:` keys, so there is nothing to edit on the server —
just skip the build.

```bash
# Local machine
docker login
docker build --build-arg APP_VERSION=$(git rev-parse --short HEAD) \
             -t phikarnot/web-product-scraper:latest .
docker push phikarnot/web-product-scraper:latest
```

```bash
# Server
ssh -i "tryon.pem" ubuntu@<EC2_HOST>
cd ~/web-product-scraper
git pull origin master          # picks up Caddyfile / compose changes
docker compose pull app
docker compose up -d --no-build --remove-orphans
docker image prune -f
```

Reclaiming disk first, if needed:

```bash
docker image prune -a -f        # safe: untagged + unused images
docker builder prune -a -f      # build cache
# Avoid `docker system prune --volumes` — it destroys caddy_data,
# which holds your TLS certificates.
```

`setup_swap.sh` exists for the same reason; run it if the box has no swap.

---

## Appendix B — What the audit could not fix in code

Carried over from the remediation report, with the pointers corrected:

| Item | Where it lives | Section |
|---|---|---|
| IMDSv2 enforcement | AWS EC2 instance metadata options | §4.1 |
| Client IP spoofing | `Caddyfile` reverse proxy headers | §3 |
| S3 public access & CORS | AWS S3 bucket policy | §4.2 |
| Play RTDN push auth | Google Cloud Pub/Sub subscription + `PLAY_RTDN_TOKEN` | — |
| S3 object deletion on account delete | Implemented in §7.3; bucket lifecycle rules for orphans are still yours | §7.3 |

For Play RTDN: the push subscription endpoint must carry `PLAY_RTDN_TOKEN`, and
a dead-letter queue should be attached so a failed revoke is retried rather than
dropped. Revocation is what claws back stars on a refund — dropping those
messages is a direct revenue leak.
