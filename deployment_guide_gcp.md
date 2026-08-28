# Migration Guide: AWS EC2 → Google Compute Engine

Moving the TryOnFusion backend from EC2 (`ap-south-1`, Elastic IP `3.6.23.203` —
verified against live DNS on 2026-08-28; note `deployment_guide_ec2.md` still
names a stale `13.233.10.157`) to a GCE VM in `asia-south1` (Mumbai), with no change to the domain, the
database, or the object store.

---

## The short version

**This box is stateless.** Everything durable already lives off-instance:

| Thing | Where it lives | Migration work |
|---|---|---|
| MongoDB | Atlas (`mongodb+srv://…`) | None — just allowlist the new IP |
| Images / try-on output | AWS S3 + presigned URLs | None — S3 keeps working from GCE |
| Gemini, Resend, Telegram | External HTTP APIs | None |
| Server B scraper | Cloudflare quick tunnel on a separate host | None |
| App config | `.env` on the box (not in git) | **Copy this by hand — it is the only file you must carry over** |
| TLS certs | `caddy_data` Docker volume | Re-issued automatically, or copy the volume for a zero-gap cutover |

So the migration is: build an identical box on GCE, prove it works on a staging
hostname, flip DNS, decommission EC2. Because both boxes talk to the same Atlas
cluster and the same S3 bucket, **they can run simultaneously** — which means
the cutover has zero downtime and an instant rollback.

Budget ~2 hours of hands-on work, plus a 48-hour soak before you delete anything.

---

## Decisions made in this guide

| Decision | Choice | Why |
|---|---|---|
| Region | `asia-south1` (Mumbai), zone `asia-south1-c` | Matches your current `ap-south-1`; your users are in India. GCP's always-free `e2-micro` only exists in `us-west1`/`us-central1`/`us-east1` — taking it would add ~200ms to every request. Not worth it. |
| Machine type | `e2-small` (2 vCPU burst, 2 GB RAM) | `t2.micro`'s 1 GB is why `setup_swap.sh` exists. The container builds Go *and* runs headless Chromium; 2 GB removes the main source of OOM stalls. ~$13–15/mo in Mumbai. |
| Disk | 30 GB `pd-balanced` | The `golang:1.24` base + Chromium image is ~1.5 GB, plus build cache and 5×50 MB of rotated logs. GCE's 10 GB default fills up. |
| Object storage | **Stay on AWS S3** | Zero code change. See the optional GCS section at the end if you want to consolidate later. |
| SSH for CI | Metadata SSH key, OS Login **disabled** on this VM | With OS Login on, GCE ignores metadata keys and your `appleboy/ssh-action` deploy breaks. |

---

## Phase 0 — Before you touch GCP

### 0.1 Lower your DNS TTL (do this ~24h ahead)

At your registrar, set the TTL on the `@` and `www` A records for
`tryonfusion.com` to **300 seconds**. Without this, the cutover in Phase 8 can
take hours to propagate and rollback is just as slow.

### 0.2 Back up the one file that matters

From your laptop:

```bash
scp -i tryonfusion-ec2-key.pem ubuntu@3.6.23.203:~/web-product-scraper/.env \
    ./env.production.backup
chmod 600 ./env.production.backup
```

Store it in your password manager. **Do not commit it.** This file has your
`JWT_SECRET`, `AWS_SECRET_ACCESS_KEY`, `GEMINI_API_KEY`, `RESEND_API_KEY`,
`INTERNAL_API_SECRET` and the Atlas connection string — recreating it from
scratch means rotating six credentials.

### 0.3 Record the current state, so you can compare after

```bash
ssh -i tryonfusion-ec2-key.pem ubuntu@3.6.23.203 \
  'docker compose ps && curl -s localhost:8080/health' 
```

Save that `/health` output. It is your pass/fail benchmark in Phase 7.

---

## Phase 1 — GCP project and CLI

1. Go to <https://console.cloud.google.com> → **Select a project** → **New Project**.
   Name it `tryonfusion-prod`. Note the **Project ID** (it may get a numeric
   suffix, e.g. `tryonfusion-prod-481523`).
2. Link a billing account: **Billing** → **Link a billing account**.
3. Install the CLI locally and authenticate:

```bash
# Debian/Ubuntu
sudo apt-get install -y apt-transport-https ca-certificates gnupg curl
curl https://packages.cloud.google.com/apt/doc/apt-key.gpg \
  | sudo gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg
echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
  | sudo tee /etc/apt/sources.list.d/google-cloud-sdk.list
sudo apt-get update && sudo apt-get install -y google-cloud-cli

gcloud auth login
gcloud config set project YOUR_PROJECT_ID
gcloud config set compute/region asia-south1
gcloud config set compute/zone asia-south1-c
gcloud services enable compute.googleapis.com
```

> **Note:** your `GEMINI_API_KEY` is tied to whichever Google Cloud project
> issued it. Creating this new project does **not** move or invalidate that key —
> leave it exactly as it is in `.env`.

---

## Phase 2 — Reserve a static external IP

GCE gives instances an ephemeral IP by default, which changes on stop/start —
the same trap Elastic IP solves on AWS. Reserve one *before* creating the VM:

```bash
gcloud compute addresses create tryonfusion-ip --region=asia-south1

# Print it — you'll use this everywhere below
gcloud compute addresses describe tryonfusion-ip \
  --region=asia-south1 --format='value(address)'
```

Call the result `NEW_IP` for the rest of this guide.

---

## Phase 3 — Firewall rules

GCP has no per-instance security groups. Ingress is denied by default and
opened by VPC rules that match **network tags**. The `default` network usually
ships with `default-allow-ssh`, `default-allow-http` and `default-allow-https`
already. Verify:

```bash
gcloud compute firewall-rules list \
  --filter="network=default" --format="table(name,allowed[],sourceRanges[],targetTags[])"
```

If `http-server` / `https-server` rules are missing, create them:

```bash
gcloud compute firewall-rules create default-allow-http \
  --network=default --allow=tcp:80 --source-ranges=0.0.0.0/0 --target-tags=http-server

gcloud compute firewall-rules create default-allow-https \
  --network=default --allow=tcp:443 --source-ranges=0.0.0.0/0 --target-tags=https-server
```

Do **not** open 8080. Caddy is the only thing that should face the internet, and
`docker-compose.yml` already keeps the app port unpublished.

---

## Phase 4 — Create the VM

```bash
gcloud compute instances create tryonfusion-server \
  --zone=asia-south1-c \
  --machine-type=e2-small \
  --image-family=ubuntu-2404-lts-amd64 \
  --image-project=ubuntu-os-cloud \
  --boot-disk-size=30GB \
  --boot-disk-type=pd-balanced \
  --address=tryonfusion-ip \
  --tags=http-server,https-server \
  --metadata=enable-oslogin=FALSE \
  --deletion-protection
```

Two flags worth understanding:

* `--metadata=enable-oslogin=FALSE` — keeps GCE reading SSH keys from instance
  metadata. If your org policy forces OS Login on, the GitHub Actions deploy in
  Phase 9 will not authenticate, and you'd need a service-account + IAP flow
  instead.
* `--deletion-protection` — you cannot `gcloud compute instances delete` this by
  accident. Remove it later with `gcloud compute instances update
  tryonfusion-server --no-deletion-protection` if you ever genuinely want it gone.

---

## Phase 5 — Provision the box

```bash
gcloud compute ssh tryonfusion-server --zone=asia-south1-c
```

Then, on the VM — this is your EC2 guide's Step 3 verbatim, since both are
Ubuntu 24.04:

```bash
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-v2 docker-buildx git
sudo systemctl enable --now docker
sudo usermod -aG docker $USER
exit   # log out and back in for the docker group to apply
```

Reconnect, then clone and add swap:

```bash
gcloud compute ssh tryonfusion-server --zone=asia-south1-c
```

```bash
git clone https://github.com/raushankrgupta/web-product-scraper.git
cd web-product-scraper
chmod +x setup_swap.sh && ./setup_swap.sh
free -h    # expect Swap: 2.0Gi
```

> Swap is still worth having on `e2-small`. Chromium spikes hard during a
> multi-image try-on, and swap turns a would-be OOM-kill into a slow request.

**If the repo is private**, generate a read-only deploy key on the VM instead of
using HTTPS:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/github_deploy -N ""
cat ~/.ssh/github_deploy.pub
# → GitHub repo → Settings → Deploy keys → Add key (read-only)
git clone git@github.com:raushankrgupta/web-product-scraper.git
```

### Restore `.env`

From your laptop:

```bash
gcloud compute scp ./env.production.backup \
  tryonfusion-server:~/web-product-scraper/.env --zone=asia-south1-c
```

**Nothing in `.env` needs editing.** `GOOGLE_REDIRECT_URL` points at
`tryonfusion.com`, not at an IP, so your Google OAuth client stays valid through
the move. Same for `EMAIL_FROM`, `ALLOWED_ORIGINS`, and the S3 credentials.

The one field to sanity-check is `SERVER_B_SCRAPE_URL`. It is a Cloudflare
*quick tunnel* hostname, which is regenerated every time `cloudflared` restarts —
the exact failure the comment in `api/health.go` documents. Confirm it still
resolves before you rely on it:

```bash
curl -sSf -o /dev/null -w '%{http_code}\n' https://<your-server-b-host>/ || \
  echo "Server B is dead — set SERVER_B_ENABLED=false and scrape locally"
```

---

## Phase 6 — Allowlist the new IP in MongoDB Atlas

**Do this before starting the app, or the container will boot and immediately
fail to connect.**

1. Atlas → your project → **Network Access** → **Add IP Address**.
2. Add `NEW_IP/32`, comment `GCE asia-south1`.
3. **Leave the existing EC2 entry (`3.6.23.203`) in place** until Phase 10.
   Both servers need access during the overlap.

---

## Phase 7 — Test on a staging hostname, before cutover

Do not flip production DNS to an untested box. But you also can't test Caddy's
TLS without a hostname pointing at the new IP — Let's Encrypt's HTTP-01
challenge needs real DNS. The clean way through is a throwaway subdomain.

### 7.1 Point a staging name at the new box

At your registrar, add one A record: `gcp` → `NEW_IP` (TTL 300).

**Then verify it before you start Caddy.** Let's Encrypt queries authoritative
DNS, not your local resolver, so "it works on my laptop" is not the test:

```bash
dig +short A gcp.tryonfusion.com @8.8.8.8    # must return NEW_IP
dig +short A gcp.tryonfusion.com @1.1.1.1    # and again from a second resolver
```

Do not proceed to 7.3 until both return `NEW_IP`. If you start Caddy against a
name that does not resolve, ACME fails with:

```
DNS problem: NXDOMAIN looking up A for gcp.tryonfusion.com
```

and Caddy enters an exponential backoff that reaches hours between attempts.
Nothing is damaged — Caddy falls back to Let's Encrypt's **staging** CA after
the first few failures precisely so production rate limits stay untouched — but
you must `docker compose restart caddy` after fixing DNS rather than waiting the
backoff out.

If a staging certificate did get issued in the meantime, browsers will reject it
as untrusted. Clear it and let Caddy reissue against production:

```bash
docker compose stop caddy
docker run --rm -v web-product-scraper_caddy_data:/data alpine \
  rm -rf /data/caddy/certificates/acme-staging-v02.api.letsencrypt.org-directory
docker compose start caddy
```

#### If you can't add a DNS record

Two ways to complete this phase without touching your registrar:

* **`nip.io`** — `NEW_IP.nip.io` resolves to `NEW_IP` with no setup at all. Use
  that as the hostname in the 7.2 block and you still get a publicly-trusted
  certificate. It is a shared domain with its own issuance limits, so if it
  fails, drop to the option below rather than retrying.
* **`tls internal`** — add that one line inside the temporary block and Caddy
  issues a self-signed certificate immediately; test with `curl -k`. This skips
  ACME entirely, so it exercises the app, the proxy and the headers but not real
  certificate issuance. Acceptable, because cutover exercises that anyway.

### 7.2 Add a temporary Caddy block — on the box only, uncommitted

```bash
cd ~/web-product-scraper
cat >> Caddyfile <<'EOF'

# TEMPORARY — remove at cutover (Phase 8.4)
gcp.tryonfusion.com {
    encode zstd gzip
    reverse_proxy app:8080
}
EOF
```

### 7.3 Build and start

```bash
export APP_VERSION=$(git rev-parse --short HEAD)
docker compose up -d --build
docker compose ps
docker compose logs -f
```

The first build pulls `golang:1.24` and installs Chromium — expect 5–10 minutes.

### 7.4 Verify against your Phase 0.3 benchmark

```bash
# Health — compare field by field with the EC2 output you saved
curl -s https://gcp.tryonfusion.com/health | jq .

# Static assets and the legal redirects Caddy handles
curl -sI https://gcp.tryonfusion.com/privacy | head -3     # expect 301 → /privacy.html
curl -sI https://gcp.tryonfusion.com/ | grep -i cache-control
```

Then exercise the paths that touch each external dependency, because those are
what a host move can break:

- [ ] **Atlas** — sign up a throwaway user; confirm the document lands in Mongo.
- [ ] **Resend** — the signup OTP email arrives. (GCP blocks outbound port 25,
      but you use Resend's HTTPS API, so this is unaffected. Worth confirming anyway.)
- [ ] **S3** — run a try-on; confirm the object appears in the bucket and the
      presigned URL renders. This proves the AWS IAM keys work from a non-AWS network.
- [ ] **Gemini** — the same try-on returns an image within `GEMINI_TIMEOUT_SECS`.
- [ ] **Scrapers** — scrape an Amazon and a Flipkart URL. These run headless
      Chromium; this is the step most likely to expose an under-sized VM.
- [ ] **Myntra / Server B** — scrape a Myntra URL. If it fails, Server B's tunnel
      hostname has rotated; that's pre-existing, not caused by the migration.
- [ ] **Telegram alerts** — hit `/internal/alert-test` (gated on `ENVIRONMENT`)
      and confirm the message arrives tagged with the new `APP_VERSION`.
- [ ] **Datacenter-IP blocking** — worth calling out: you are moving from an AWS
      Mumbai IP to a Google Mumbai IP. Anti-bot systems score these ranges
      differently, so a site that scraped fine on EC2 may challenge you here.
      This is the single most likely surprise in the whole migration — test every
      scraper before cutover, not after.

Watch memory while scraping:

```bash
docker stats --no-stream
free -h
```

If you see swap being consumed heavily under normal load, resize before cutover:

```bash
gcloud compute instances stop tryonfusion-server --zone=asia-south1-c
gcloud compute instances set-machine-type tryonfusion-server \
  --zone=asia-south1-c --machine-type=e2-medium
gcloud compute instances start tryonfusion-server --zone=asia-south1-c
```

---

## Phase 8 — Cutover

Leave the EC2 box **running**. Both servers are stateless and share Atlas and
S3, so during propagation some users hit the old box and some the new one, and
neither can tell. That is what makes this a zero-downtime flip.

### 8.1 (Optional) Carry the TLS certificate across

If you'd rather not wait on Let's Encrypt at the moment of cutover — and avoid
any risk of tripping the "5 duplicate certificates per week" rate limit if you
end up retrying — copy Caddy's data volume from the old box:

```bash
# On EC2
docker run --rm -v web-product-scraper_caddy_data:/data -v $PWD:/backup \
  alpine tar czf /backup/caddy_data.tar.gz -C /data .

# Laptop
scp -i tryonfusion-ec2-key.pem ubuntu@3.6.23.203:~/web-product-scraper/caddy_data.tar.gz .
gcloud compute scp caddy_data.tar.gz tryonfusion-server:~/ --zone=asia-south1-c

# On GCE — stop Caddy first
docker compose stop caddy
docker run --rm -v web-product-scraper_caddy_data:/data -v ~/:/backup \
  alpine sh -c "tar xzf /backup/caddy_data.tar.gz -C /data"
docker compose start caddy
```

Skipping this is fine — Caddy will just issue a fresh certificate within about
30 seconds of DNS resolving. The copy simply removes that window.

### 8.2 Remove the temporary staging block

```bash
cd ~/web-product-scraper
git checkout Caddyfile     # discards the gcp.tryonfusion.com block from 7.2
docker compose restart caddy
```

### 8.3 Flip the A records

At your registrar, change both records to `NEW_IP`:

* `@`   → `NEW_IP`
* `www` → `NEW_IP`

### 8.4 Watch it land

```bash
# From your laptop, until it returns NEW_IP
dig +short tryonfusion.com @8.8.8.8
dig +short www.tryonfusion.com @8.8.8.8

# On the GCE box — traffic should start arriving
docker compose logs -f caddy
curl -sI https://tryonfusion.com | head -5
```

Confirm the padlock and the certificate's issue date in a browser. Then check
that the *old* box has gone quiet:

```bash
ssh -i tryonfusion-ec2-key.pem ubuntu@3.6.23.203 \
  'docker compose logs --tail 20 caddy'
```

### Rollback

Change the two A records back to `3.6.23.203`. That's the entire procedure —
the EC2 box is still running, still allowlisted in Atlas, still holding its
certificate. Keep it that way for at least 48 hours.

---

## Phase 9 — Repoint CI/CD

`.github/workflows/deploy.yml` deploys over SSH. Two things to change: the key
the VM accepts, and the secrets the workflow uses.

### 9.1 Create a deploy key and install it in instance metadata

On your laptop:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/gce_deploy -N "" -C "github-actions"
```

GCE parses metadata SSH keys as `username:key-content`. Pick the Linux user the
workflow will log in as — `deploy`:

```bash
echo "deploy:$(cat ~/.ssh/gce_deploy.pub)" > /tmp/gce_keys.txt
gcloud compute instances add-metadata tryonfusion-server \
  --zone=asia-south1-c --metadata-from-file ssh-keys=/tmp/gce_keys.txt
```

> **Careful:** `add-metadata ssh-keys=` *replaces* the whole key list. If other
> keys are already there, read them first with
> `gcloud compute instances describe tryonfusion-server --zone=asia-south1-c \
> --format="value(metadata.items.filter(key:ssh-keys).extract(value))"`
> and append to that file rather than overwriting it.

GCE auto-creates the `deploy` user on first login. Give it Docker access and put
the checkout where the workflow expects it:

```bash
gcloud compute ssh tryonfusion-server --zone=asia-south1-c
sudo usermod -aG docker deploy
sudo -iu deploy git clone https://github.com/raushankrgupta/web-product-scraper.git
sudo cp ~/web-product-scraper/.env /home/deploy/web-product-scraper/.env
sudo chown deploy:deploy /home/deploy/web-product-scraper/.env
sudo chmod 600 /home/deploy/web-product-scraper/.env
```

Verify from your laptop before touching GitHub:

```bash
ssh -i ~/.ssh/gce_deploy deploy@NEW_IP 'cd web-product-scraper && docker compose ps'
```

> Simpler alternative: skip the `deploy` user entirely and reuse your own
> `gcloud compute ssh` username. It works, but then CI holds a key to your
> personal account — a dedicated user is worth the extra five minutes.

### 9.2 Update the GitHub secrets

Repo → **Settings** → **Secrets and variables** → **Actions**:

| Secret | New value |
|---|---|
| `EC2_HOST` | `NEW_IP` |
| `EC2_USER` | `deploy` |
| `EC2_SSH_KEY` | contents of `~/.ssh/gce_deploy` (the private key, including the BEGIN/END lines) |

Keeping the `EC2_*` names means **zero workflow changes** — the deploy script
itself (`git pull`, image tagging for rollback, `docker compose up -d --build`,
the `/health` check) is host-agnostic and works as-is.

If you'd rather the names not lie, rename them to `DEPLOY_HOST` / `DEPLOY_USER` /
`DEPLOY_SSH_KEY` and update the four references in `.github/workflows/deploy.yml`,
plus the workflow's `name:` line. Cosmetic, but this file will outlive your memory
of the migration.

### 9.3 Prove it

Push a trivial commit to `master` and watch the Actions tab. The workflow's own
`/health` check at the end is the gate — if it passes, CI is fully migrated.

---

## Phase 10 — Decommission AWS

Only after **48+ hours** of clean running on GCE.

### Do remove

1. **Atlas allowlist** — delete the `3.6.23.203` entry.
2. **Terminate the EC2 instance** — EC2 console → Instance state → Terminate.
3. **Release the Elastic IP** — Network & Security → Elastic IPs → Release.
   *AWS bills for allocated-but-unassociated Elastic IPs.* Skipping this is the
   most common way a "closed" AWS account keeps charging.
4. **Delete leftover EBS volumes and snapshots** — Elastic Block Store → Volumes,
   filter for `available` status.
5. **Delete the EC2 key pair** and the local `.pem`.

### Do NOT remove

- **The S3 bucket** — it holds every try-on image your users have generated, and
  the app still reads and writes it.
- **The IAM user behind `AWS_ACCESS_KEY_ID`** — still in active use from GCE.
- **The AWS account itself.**

Tighten instead: if that IAM user's policy allows EC2 actions it no longer needs,
scope it down to `s3:PutObject` / `s3:GetObject` on your bucket only. Migration is
a good moment to do it, since you now know exactly what the app uses.

---

## Optional: consolidating S3 → Google Cloud Storage

Not required, and not recommended as part of this migration — do it as a separate
change once the host move is stable.

The appeal is that cross-cloud egress goes away. In practice your egress is
small: clients fetch images directly from S3 via presigned URLs
(`utils/s3.go:84`), so the app→S3 traffic is uploads only.

If you do move, the low-effort path is GCS's S3-compatible XML API, which lets
you keep the AWS SDK and change only configuration:

1. Create a bucket in `asia-south1`.
2. Copy existing objects with **Storage Transfer Service** (console → Data
   Transfer), which handles S3 sources natively.
3. Generate **HMAC keys**: Cloud Storage → Settings → Interoperability.
4. In `utils/s3.go:30`, add a custom endpoint resolver pointing at
   `https://storage.googleapis.com`, and put the HMAC key/secret in the existing
   `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` variables.

The presign call at `utils/s3.go:84` works against GCS too, so the try-on gallery
keeps functioning unchanged. Run both buckets in parallel for a week before you
delete anything from S3.

---

## Reference: EC2 → GCE concept mapping

| AWS | GCP | Note |
|---|---|---|
| Elastic IP | Reserved static external IP address | Both bill when allocated and unused |
| Security Group | VPC firewall rule + network tag | Rules are project-wide and matched by tag, not attached per instance |
| Key pair / `.pem` | Instance metadata `ssh-keys`, or OS Login | Enabling OS Login makes GCE **ignore** metadata keys |
| `t2.micro` (1 GB) | `e2-small` (2 GB) | `e2-micro` is the closest match but is only free in US regions |
| EBS gp3 | `pd-balanced` | Default boot disk is 10 GB — raise it |
| `ap-south-1` | `asia-south1` | Both Mumbai |
| AMI | Image family (`ubuntu-2404-lts-amd64`) | Families always resolve to the latest patched image |
| Instance stop/start changes IP | Same | Reserve the address to avoid it |
| — | Deletion protection, live migration | GCE patches the host underneath you without a reboot |

Two GCP-specific behaviours worth knowing:

* **Outbound port 25 is permanently blocked** on GCE, with no exception process.
  You are unaffected — Resend is an HTTPS API — but never add a direct-SMTP
  fallback to this server.
* **Egress is open by default, ingress is denied.** The inverse of nothing in
  AWS, but the rules live at the network level, so a firewall change can affect
  every instance carrying that tag.
