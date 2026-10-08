# Infra log — VM provisioning & deploy (manual steps)

> **This file is intentionally a chronological changelog** — an exception to the
> repo's "docs describe current state only" rule (see SKILL.md). It records the
> manual, one-time infra steps taken to stand up the backend VM, so they can
> later be turned into Terraform/Ansible. Append new steps at the bottom; edit an
> earlier step in place only when a later, better decision supersedes it (note
> what changed). Keep it accurate.
>
> Companion to `deploy/README.md` (which describes the *target* stack) — this is
> the *record of what was actually done*.

## Facts (current)

- **VM:** Hetzner Cloud, Helsinki (hel1), AlmaLinux 10.1 (Heliotrope Lion),
  x86_64. Public IP in `.env` as `SERVER_IP` (kept out of the public repo).
- **Domain:** `gaias-choice.gardenofatlantis.com` → A record → the VM IP (`SERVER_IP`)
  (registrar: Namecheap). This is the API host Caddy terminates TLS for
  (`API_DOMAIN`). The static site stays on GitHub Pages
  (`https://dennislapchenko.github.io`) and does not move.
- **SSH:** `ssh -p 13337 root@gaias-choice.gardenofatlantis.com -i ~/.ssh/gaia`
  (root, key-based; **port 13337**, moved off 22 — see hardening section).

## Timeline

### 1. DNS
- Created an A record `gaias-choice.gardenofatlantis.com → <VM IP>` on
  Namecheap. (Owner action.)

### 2. VM rental
- Rented a Hetzner Cloud VM in Helsinki, AlmaLinux 10. (Owner action.)
- Verified reachable over SSH; confirmed OS `AlmaLinux 10.1`, arch `x86_64`,
  egress IP matched the VM's public IP (`SERVER_IP`). Docker/compose not yet installed.

### 3. VM base setup (over SSH, as root)
- Installed Docker from the official CE repo:
  ```sh
  dnf -y install dnf-plugins-core
  dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
  dnf -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  ```
  Result: Docker `29.6.1`, Compose plugin `v5.3.0`.
- Created host dirs for the bind mounts (per D9 — boring backups):
  ```sh
  mkdir -p /srv/gaias-choice/{data,caddy,backups}
  ```
- **Firewall:** this AlmaLinux 10 minimal image ships **without firewalld**
  (`firewall-cmd` absent, `nftables` service inactive). The host iptables
  `INPUT` policy is `ACCEPT`; the only nft/iptables rules present are the ones
  Docker installs for its own bridge. So **no host firewall step is needed** —
  ports 80/443 are reachable once Caddy binds them. If external access ever
  fails, check for a **Hetzner Cloud firewall** attached in the Hetzner console
  (none was configured; SSH/22 reachable implies no restrictive external rule).

### 4. Image delivery (registry via CI)
- Added `.github/workflows/build-backend.yml`: on push touching `backend/**`,
  builds `backend/Dockerfile` and pushes
  `ghcr.io/dennislapchenko/gaias-choice-be` tagged `sha-<commit>` + `latest`.
- Passed the live-editing tokens through in `deploy/compose.yaml` (api service
  `ADMIN_TOKEN`/`GITHUB_TOKEN`, sourced from `deploy.env`).
- **GHCR visibility:** turned out the package was **already pullable
  unauthenticated** from the VM after the first CI push (repo is public) — no
  manual visibility flip was needed. If a future private-repo move breaks the
  pull, either make the package public or `docker login ghcr.io` on the VM with
  a `read:packages` token.

### 5. First deploy (manual compose, doco-cd deferred)
- `git` is **not** installed on the VM (minimal image). For a manual deploy only
  `compose.yaml` + `Caddyfile` + `deploy.env` are needed, so they were `scp`'d
  to `/opt/gaias-choice/deploy/` rather than installing git / cloning. (doco-cd,
  when activated, clones inside its own container — host git still not required.)
- `deploy.env` on the VM (`chmod 600`, never committed) holds:
  ```
  API_DOMAIN=gaias-choice.gardenofatlantis.com
  CORS_ORIGINS=https://dennislapchenko.github.io
  BE_TAG=sha-<commit>          # pinned to the CI-built image, not `latest`
  ADMIN_TOKEN=<secret>         # edit-mode bearer
  GITHUB_TOKEN=<fine-grained PAT, Contents RW on this repo>
  ```
- Brought up:
  ```sh
  cd /opt/gaias-choice/deploy
  docker compose --env-file deploy.env -f compose.yaml up -d
  ```
- Caddy obtained a Let's Encrypt cert for `gaias-choice.gardenofatlantis.com`
  automatically (ports 80/443 reachable, DNS in place). Cert + ACME account
  persist in the `/srv/gaias-choice/caddy` bind mount across redeploys.
- **Verified externally** (from off the VM):
  - `GET /api/healthz` → 200 `{"status":"ok"}`
  - `GET /api/hello` → 200, DB round-trip OK (SQLite at `/srv/gaias-choice/data`)
  - `GET /api/content/ping` → **401** without auth, **200** with the bearer
    ⇒ live-editing is **armed** (not 503)
  - CORS preflight from `https://dennislapchenko.github.io` → 204 with the
    correct `Access-Control-Allow-Origin`
- The `api` container's `8787` is **not** host-published; only Caddy's 80/443
  are. (Bot scans of `/.env*` seen in api logs are proxied through Caddy and
  correctly 404 — nothing is directly exposed.)

### 6. Wire the live site to the backend
- Added `VITE_API_URL=https://gaias-choice.gardenofatlantis.com/api` to the
  Pages build env (`.github/workflows/deploy-pages.yml`) so the deployed SPA
  calls the VM API. Remove that line to unwire (api.ts then falls back to
  same-origin `/api`, i.e. no backend).

### 7. Auth model switch: ADMIN_TOKEN → login sessions (done)

- The backend replaced the static `ADMIN_TOKEN` bearer with real users/
  sessions/roles, then user accounts went reader-facing (header login,
  viewer self-registration, `/account` campfire — CLAUDE.md "Backend").
- Rolled out to the VM: in `deploy.env`, deleted `ADMIN_TOKEN`, added
  `BOOTSTRAP_ADMIN_EMAIL`/`BOOTSTRAP_ADMIN_PASSWORD`, bumped `BE_TAG`, and
  `up -d`. The existing `/srv/gaias-choice/data/gaia.db` self-migrated
  (002_users_sessions + 003_viewer_display_name) and the bootstrap admin was
  created on first boot.
- **Two gotchas hit during the rollout, both now rules:**
  - The VM's `/opt/gaias-choice/deploy/compose.yaml` is a manual `scp` copy —
    it does NOT track the repo. The stale copy silently dropped the new
    `BOOTSTRAP_ADMIN_*` env passthrough (no bootstrap, login 401). **When
    `deploy/compose.yaml` or `deploy/Caddyfile` changes, scp them to the VM
    before `up -d`** (doco-cd will make this automatic once activated).
  - The bootstrap password must satisfy the backend's ≥8-char policy or the
    api container **crash-loops at boot** (fatal bootstrap error + restart
    policy). The password lives in `deploy.env` on the VM and mirrors the
    repo-root `.env` locally.
- Verified externally: login → 200 token; `/api/auth/me` → 200 with
  `"editing": true` (admin + GitHub seam); `/api/users` → 401 unauthenticated,
  200 with a session; `/api/content/file` → 200 as admin; CORS preflight from
  the Pages origin → 204; logout → 204.

### 8. Email delivery: Postmark (transport live, account approval pending)

- Registered with **Postmark** as the transactional email provider (magic-link
  path A in `context/auth/auth-paths.md`).
- Added a **DKIM** record and a **CNAME** (`returnpath`) record for the
  sending domain on Namecheap, per Postmark's setup instructions — verified
  (sends from `login@gardenofatlantis.com` are accepted).
- Backend side is built: `internal/mail` + `/api/auth/magic{,/verify}` ship
  with the repo (see CLAUDE.md "Backend"). The live transport is **Postmark's
  HTTP API** (`POSTMARK_TOKEN` + `POSTMARK_STREAM=choice-email`, message
  stream created in the Postmark console); SMTP remains a dormant fallback.
  Env block passed through `deploy/compose.yaml`; values mirror the repo-root
  `.env` locally and `deploy.env` on the VM.
- Verified with a real API call: `POST https://api.postmarkapp.com/email`
  from `login@gardenofatlantis.com` on stream `choice-email` → `ErrorCode: 0`.
- **Open: Postmark account approval.** While pending, Postmark rejects (412)
  any recipient outside `gardenofatlantis.com` — reader magic links will NOT
  deliver until the account is approved in the Postmark console (owner
  action). The BE logs each failed send (`mail: magic-link send failed`).

### 9. doco-cd GitOps activated (manual deploy retired)

- **Daemon:** `ghcr.io/kimdre/doco-cd` (pinned — see below) runs on the VM from
  `/opt/doco-cd/{compose.yaml,poll.yaml,secrets.env}` (project `doco-cd`). It
  **polls** this repo `refs/heads/main` every 30s (`POLL_CONFIG_FILE`), so it
  needs **no inbound port** — the edge firewall stays 22/80/443, no webhook.
  Public repo ⇒ no `GIT_ACCESS_TOKEN`. Mounts `/var/run/docker.sock` + its own
  `doco-cd_data` clone-cache volume. No `WEBHOOK_SECRET`/`API_SECRET` (both
  endpoint sets stay disabled).
- **Proof first (throwaway `test` target):** a `traefik/whoami` stack under
  `deploy/test/` + `.doco-cd.test.yml`, selected by poll `target: test`, proved
  the full loop (push → recreate) in isolation — the live `.doco-cd.yml` is
  never read while a target is set. Both were deleted once the live stack
  migrated.
- **Migration:** the live stack moved from the hand-run compose project `deploy`
  (`/opt/gaias-choice/deploy`, `docker compose up`) to doco-cd's project
  `gaias-choice` (from repo-root `.doco-cd.yml`, `working_dir: deploy`). Cutover:
  `docker compose down` the old `deploy` project (frees 80/443), then dropped
  `target:` from `poll.yaml` and recreated the daemon → it deployed
  `gaias-choice-{api,caddy}-1`. The `/srv/gaias-choice/{data,caddy}` **host bind
  mounts carried the SQLite DB and Caddy cert across** — no re-ACME, no data
  loss (brief api outage during the swap only).
- **Secrets via `PASS_ENV`:** the daemon loads `/opt/doco-cd/secrets.env` (the
  former `deploy.env`, `chmod 600`, uncommitted) and `PASS_ENV=true` forwards it
  into the stack's compose interpolation. Non-secrets (`API_DOMAIN`,
  `CORS_ORIGINS`, `BE_TAG`) were pulled OUT of `secrets.env` and into
  `.doco-cd.yml`'s `environment:` in git — so a **backend release is now a
  `BE_TAG` commit** (`task be:deploy`), not a VM edit. `secrets.env` holds only
  the 13 real secrets.
- **Verified externally** through the doco-cd-managed stack: `/api/healthz` 200
  + valid TLS (cert reused), `/api/hello` 200 (DB round-trip), `/api/content/*`
  **401 not 503** (⇒ `GITHUB_TOKEN` delivered via `PASS_ENV`), CORS preflight
  from the Pages origin 204.
- **Old manual-deploy dir removed:** `/opt/gaias-choice/deploy/` (stale
  `compose.yaml`/`Caddyfile`/`deploy.env`) was `rm -rf`'d — it was inert but a
  footgun (a `docker compose up` there would fight doco-cd's caddy for 80/443).
  `/opt/gaias-choice/` is gone; persistent data still lives in `/srv/gaias-choice`.
- **doco-cd image pinned** to `0.103.0@sha256:90b56eaf…` in
  `/opt/doco-cd/compose.yaml` (history: `:latest` → `0.94.0` → `0.102.0`). Bump
  the tag+digest together to upgrade the daemon (via `task doco:sync`). Note the
  ghcr tags have **no `v` prefix** (`:0.103.0`, not `:v0.94.0` — the earlier `v`
  tag was cosmetic and only resolved because the `@sha256` digest is
  authoritative).
- **Deploy pings collapsed to a one-liner** (0.103.0's
  `APPRISE_NOTIFY_BODY_TEMPLATE`): body = `{{.Stack}} @ {{.Revision}}` instead of
  the 4-line job_id/repository/revision/stack block; title stays daemon-native.
  Public repo ⇒ no `GIT_ACCESS_TOKEN_USER` needed (that 0.103.0 knob is for
  private GitLab deploy tokens).

#### doco-cd server config (`/opt/doco-cd/`)

Three files under `/opt/doco-cd/`. Two are **mirrored in the repo**
(`deploy/controller/`) and pushed with `task doco:sync` (step 10); `secrets.env`
is VM-only. The verbatim copies below are a **snapshot of step 9** kept for the
record and deliberately not refreshed on later bumps — `deploy/controller/` is
the source of truth for what runs now (image pin included).

`compose.yaml` — the daemon + the Apprise notification sidecar (docker-socket,
polling, `PASS_ENV`, pinned images):

```yaml
services:
  doco-cd:
    image: ghcr.io/kimdre/doco-cd:0.103.0@sha256:90b56eafce7791142a91bdeadf71a2cb0c0d554ef70c4e47ac43bbd7554afbf6
    restart: unless-stopped
    env_file: [secrets.env]          # forwarded into deploys via PASS_ENV
    environment:
      LOG_LEVEL: warn   # `info` logs two lines every 30s poll (polling + job
                        # completed) — pure noise. doco-cd has no per-event log
                        # toggle, only LOG_LEVEL, so `warn` is the lever: routine
                        # polls go silent, deploy *failures* (warn/error) still
                        # show; you lose the "deployed" info line (deploys are
                        # confirmed externally anyway). Bump back to info to debug.
      TZ: Europe/Helsinki
      POLL_CONFIG_FILE: /etc/doco-cd/poll.yaml
      PASS_ENV: "true"
      APPRISE_API_URL: http://apprise:8000/notify  # notifications → apprise
      APPRISE_NOTIFY_LEVEL: success                # min level; no-op polls
                                                   # aren't deploys, so this is
                                                   # ✅/❌ deploys only, no spam
      APPRISE_NOTIFY_BODY_TEMPLATE: "{{.Stack}} @ {{.Revision}}{{if .IsReconciliation}} · {{.ReconciliationEvent}}{{end}}"  # one-line body (0.103.0+)
    depends_on: [apprise]            # service_started (not _healthy): a broken
                                     # apprise must never block polling
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./poll.yaml:/etc/doco-cd/poll.yaml:ro
      - data:/data
    healthcheck:
      test: ["CMD", "/doco-cd", "healthcheck"]
      start_period: 15s
      interval: 30s
      timeout: 5s
      retries: 3
  # doco-cd's notifier speaks only the Apprise wire format (not a raw templatable
  # webhook), so Telegram/etc. deploy pings need this sidecar. Stateless,
  # internal-only (no published port; reached at apprise:8000), pinned by digest.
  apprise:
    image: caronc/apprise@sha256:91321755496e8472bdb674e4b14eb64d1f3b15510ef94971309c9f76ef3171e7
    restart: unless-stopped
    environment: { TZ: Europe/Helsinki, APPRISE_WORKER_COUNT: 1 }
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:8000/status"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 20s
volumes:
  data:
```

**Notifications (Telegram, via the Apprise sidecar).** doco-cd POSTs an
Apprise-format `{urls,title,body,type}` to `apprise:8000`, which does the actual
Telegram `sendMessage`. Fires on **deploy completed / failed** + reconciliation
warnings — never per-poll (a skipped poll isn't a deployment). The target lives
in `secrets.env` as `APPRISE_NOTIFY_URLS=tgram://<TELEGRAM_BOT_TOKEN>/<chat_id>/`
— it **reuses the BE's login-bot token** (safe: Apprise only calls `sendMessage`,
independent of the BE's `getUpdates` login poll). Always an **explicit** chat_id,
never Apprise's chat-id-less form (that auto-broadcasts to *every* user who ever
DM'd the login bot). Current target is a personal chat; swap the chat_id for a
channel id (`-100…`) + add the bot as a channel admin to move it later.

`poll.yaml` — what to watch (no `target:` ⇒ the default `.doco-cd.yml`):

```yaml
- url: https://github.com/dennislapchenko/gaias-choice.git
  reference: refs/heads/main
  interval: 30s
```

`secrets.env` — VM-only secrets (`chmod 600`, uncommitted), keys only:
`GITHUB_TOKEN`, `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD`,
`POSTMARK_TOKEN`, `POSTMARK_STREAM`, `MAIL_FROM`, `PUBLIC_SITE_URL`,
`TELEGRAM_BOT_TOKEN`, `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL`, `GIN_MODE`,
`DEBUG`, `SERVER_IP`, plus `APPRISE_NOTIFY_URLS` (the tgram target — reuses
`TELEGRAM_BOT_TOKEN` inline, so the token string appears twice; `env_file` has
no interpolation, hence the small duplication). (Non-secrets
`API_DOMAIN`/`CORS_ORIGINS`/`BE_TAG` live in `.doco-cd.yml`, not here.) Mirror of
the repo-root `.env` minus those three (plus the doco-cd-only `APPRISE_NOTIFY_URLS`).

#### Reconcile triggers (what actually causes a redeploy) — verified

doco-cd polls every 30s; an unchanged ref is a ~400ms no-op. When the ref
advances it enters a pre-deploy check (`shouldSkipDeployment`) and **only
redeploys when something the stack actually references changed**:

- **`deploy/app/compose.yaml`** (the compose file, `working_dir: deploy/app`) →
  redeploy.
- **`.doco-cd.yml`** (deploy config; it hashes the whole thing — `BE_TAG`,
  `environment:`, `working_dir`, …) → redeploy. This is what the backend
  auto-roll trips.
- **A bind-mounted / referenced file changes**, e.g. `deploy/app/Caddyfile` →
  doco-cd **force-recreates just that service** (`forced_services:[caddy],
  mode:force`). So Caddyfile edits DO auto-apply — no `--watch`, no manual
  reload. (Verified 2026-07: pushing a Caddyfile header force-recreated caddy.)
- **A new image digest** behind the referenced tag → redeploy. Ours are
  immutable `sha-…`, so this only moves when `BE_TAG` moves.
- **Running services drifted** from the rendered config → recreate the drifted.

Commits that touch **none** of the above — docs, `deploy/README.md`,
`deploy/infra-log.md`, **`deploy/controller/**`** (Layer 0, applied via
`task doco:sync`, not by doco-cd), anything outside the stack, every
`content/**` / `frontend/**` push — are **skipped** (checked out into doco-cd's
clone but no `docker compose up`). Empirically: over one stretch, 50 polls → 1
deploy.

### 10. Deploy layout: `app/` + `controller/` split; controller now repo-managed

- **Split `deploy/` into two layers by lifecycle.** `deploy/app/` holds the
  GitOps app stack (`compose.yaml` + `Caddyfile`, reconciled by doco-cd);
  `deploy/controller/` holds the doco-cd daemon's own config (previously VM-only
  bootstrap). `.doco-cd.yml` `working_dir` moved `deploy` → `deploy/app` in the
  same commit as the file move — doco-cd checked out the new tree and reconciled
  from `deploy/app` in one poll (verified: `/api/healthz` 200, first real ✅
  Telegram deploy ping fired end-to-end).
- **Controller config is now in the repo** (`deploy/controller/{compose.yaml,
  poll.yaml}` + `secrets.env.example`), the repo being the source of truth.
  `task doco:sync` (→ `sync.sh`) scps the two non-secret files to `/opt/doco-cd/`
  and reloads the daemon; `secrets.env` stays VM-only (git-ignored). doco-cd
  never deploys `deploy/controller/**` itself (it's Layer 0, not in the watched
  stack).
- **`bootstrap-vm.sh`** added — idempotent from-scratch provisioner (Docker +
  `/srv` dirs + fetch controller config from the public repo + seed the secrets
  template + `up`), meant to double as OpenTofu server userdata.
- **Deferred to OpenTofu:** the Hetzner edge firewall + SSH hardening stay
  imperative for now (see "Security posture"); they'll be codified as
  `hcloud_firewall`/config in an `infra/` OpenTofu module, not shell.

### 11. Runtime self-healing: api healthcheck + doco-cd reconciliation

- **`/api/healthz` is now a readiness probe** — it `Ping`s the DB (was a bare
  `{status:ok}` liveness). 200 only when the DB round-trips, else 500. Non-mutating
  (unlike `/api/hello`, which bumps a counter), so it's safe to hit every interval.
  Also tightens the FE `backendUp` gate (`session.tsx` probes it).
- **Container healthcheck** on the api service (`deploy/app/compose.yaml`):
  `test: ["CMD", "/server", "healthcheck"]`. The image is distroless (no
  shell/curl), so the Go binary probes itself — `main.go` intercepts a
  `healthcheck` arg, GETs `/api/healthz`, exits 0/1 (same pattern as doco-cd's
  own `/doco-cd healthcheck`).
- **`reconciliation: { events: [unhealthy, die] }`** in `.doco-cd.yml`. doco-cd
  watches Docker events on the stack: **unhealthy** → in-place container restart;
  **die** → full `compose up` reconcile. Both fire a `[R]` Telegram alert. Events
  during doco-cd's own deploys are suppressed (`isStackDeploymentInProgress`), so a
  `BE_TAG` bump never self-triggers. Crash-loop caveat: `die` isn't rate-limited
  (only `unhealthy` is, via `restart_limit`/`restart_window`), so a genuinely broken
  api can redeploy-loop until fixed — acceptable (noisy, not damaging). Exit code is
  **not** in the notification payload; `task be:logs` for the why.
- **Shipped in two phases (ordering matters):** the healthcheck config must not go
  live before the image that implements `/server healthcheck`. Phase 1 = backend
  push (CI builds the new image + auto-bumps `BE_TAG`); phase 2 = the compose
  healthcheck + reconciliation, applied against the now-current image.

### 12. doco-cd 0.114.0; deploy pings name the services a deploy moved

- **Daemon bumped `0.110.1` → `0.114.0`** (tag + digest together) in
  `deploy/controller/compose.yaml`, applied with `task doco:sync`. The daemon
  confirms itself: `starting application version=v0.114.0`.
- **`APPRISE_NOTIFY_BODY_TEMPLATE` gained a second range, over
  `.ChangedServices`** — one `` `recreated: <service>` `` line per service the
  deploy actually moved, under the commit lines and fully backtick-wrapped for
  the same `?format=markdown` reason. It only earns its place now: the field
  used to stay empty on a plain image-tag bump, which is exactly how this stack
  ships the backend (`release.sh` moves `BE_TAG`). 0.112.0 added a local
  configured-vs-deployed image-reference comparison, so a `BE_TAG` bump finally
  names `api`. Field semantics live in the compose file's comment; the template
  was executed against all five notification shapes (single service, multi
  service, no changed services, failure, reconcile) before syncing, since a bad
  field reference fails the daemon at startup.
- **The three 0.113.0 removals and the 0.111.0 parser swap do not touch this
  estate.** Checked before syncing:
  - `one_shot` job-execution-mode alias gone (`one_off` only) — this estate
    schedules no jobs; zero matches in the repo.
  - legacy auto-discovery labels retired (`…auto_discover`,
    `…auto_discover.delete`, `…auto_discovery.delete`) — zero matches, and
    `auto_discovery` is not enabled in `.doco-cd.yml`.
  - PKCS#1 `ENCRYPTED PRIVATE KEY` SSH keys unsupported — `poll.yaml` polls a
    **public** `https://` repo with no git credentials. No SSH key exists
    anywhere in the deploy path.
  - 0.111.0 replaced the `env_files` dotenv parser with compose-go's, so
    `${lowercase}` / `${MixedCase}` inside such a file now interpolate (empty
    string when unset) instead of staying literal, and bare keys inherit from
    the environment. Two independent reasons this cannot bite: the deploy
    config uses no `env_files` key at all, and `/opt/doco-cd/secrets.env` —
    which compose's own `env_file:` reads, not doco-cd's parser — holds **zero
    lines containing `$`** across its 14 variables.
- **`reconciliation: events: [unhealthy, die]` behaves as before.** The wiki's
  Reconciliation Settings section is byte-identical between the upstream
  `v0.110.1` and `v0.114.0` tags: same events, same restart-vs-redeploy
  actions, same `restart_limit`/`restart_window` defaults.
- **Measured: the managed stack was NOT force-recreated.** `gaias-choice-api-1`
  and `gaias-choice-caddy-1` report `StartedAt` `2026-08-13T19:18:58.444324990Z`
  and `…446554071Z` — identical to the nanosecond before the sync, after it, and
  after two further daemon recreates. Same container ids (`295fce3ddca2`,
  `1e97c88c0d11`), `RestartCount=0`, api still `healthy`, 19d18h continuous
  uptime. `doco-cd-apprise-1` untouched too. Only `doco-cd-doco-cd-1` was
  replaced. Four consecutive polls logged `job completed successfully`
  (1.57s / 761ms / 1.911s / 840ms) with zero deployments and zero warnings.
- **A controller restart pings ❌ once or twice** (`context canceled` — the
  in-flight poll dies with the container). Expected, not a fault; the next poll
  is clean.

### 13. doco-cd 0.117.1; the metrics endpoint answers "which version, did it poll"

- **Daemon bumped `0.114.0` → `0.117.1`** (tag + digest together) in
  `deploy/controller/compose.yaml`, applied with `task doco:sync`. The pin is
  the **index** digest, not a per-arch one: `ghcr.io` returns
  `sha256:9db114c9…` for the tag with an
  `application/vnd.oci.image.index.v1+json` content type, and the amd64 child
  inside it is what this VM runs.
- **Image pre-pulled before the sync, and that is now the house rule.**
  `sync.sh` scps `compose.yaml` and only THEN runs `up -d`, so a failed pull
  leaves `/opt/doco-cd/compose.yaml` naming an image the box is not running —
  no outage, no error, and the divergence surfaces on the next reboot. ghcr.io
  is flaky from these Hetzner boxes (truetothebook needed 4 attempts on one
  VM last round). This time **one attempt** was enough here.
- **Nothing in 0.115.0 → 0.117.1 touches this estate.** Checked before syncing:
  - **No removals, no renames.** `internal/config/app` and
    `internal/config/deploy` only gained keys (`SSH_KNOWN_HOSTS_FILE`,
    `OPENAPI_ENABLED`, `PPROF_ENABLED`/`PPROF_PORT`), all defaulting off. The
    `cd.doco.*` label set only gained four scheduler bookkeeping labels
    (`job.run_id`, `job.scheduled_at`, `job.started_at`,
    `job.source_service_id`) — nothing this estate writes, and it schedules no
    jobs anyway.
  - **`env_files` parsing and the notification template are byte-identical.**
    `internal/config/deploy/dotenv.go` and `reconciliation.go` do not change
    across the range, and `Metadata` + `TemplateData` — the structs behind
    `{{.Commits}}`, `{{.ChangedServices}}`, `{{.Duration}}` — diff to nothing.
    The template in `compose.yaml` needed no edit.
  - **Auto-discovery changed but is not enabled here** (`.doco-cd.yml` has no
    `auto_discovery`). 0.116.0 made the config comparison semantic rather than
    textual.
  - **SSH host-key handling went fail-closed** and 0.116.0 added
    `SSH_KNOWN_HOSTS_FILE`. Irrelevant: `poll.yaml` polls two **public
    https** repos with no git credentials.
  - **SOPS decryption fix in 0.117.1** (files from every bind-mounted
    directory were being dropped) — this estate runs no encryption; secrets
    come from `/opt/doco-cd/secrets.env` via `PASS_ENV`.
- **The one thing worth proving, and the reason to read the compose libs.**
  A deploy is skipped when the recorded `cd.doco.deployment.compose.sha`
  still matches, and that hash is `json.Marshal` of the loaded compose-go
  project — so a compose-go bump that adds a serialized field silently
  redeploys every stack on the first poll. compose-go went `2.14.0` →
  `2.15.0` here; its `types/` diff is one `GetPullPolicy` fix and one new
  `Project` method, **zero struct fields**, and the loader diff is a mutex
  plus include dedup. Config-hash side is safe by inspection too:
  `deploy.Config` and its `Hash()` are unchanged. Measured after the sync,
  and it held.
- **Measured: nothing but the daemon moved.** `gaias-choice-api-1`
  (`295fce3ddca2`) and `gaias-choice-caddy-1` (`79cfde4fa867`) kept their
  container ids and their `StartedAt` to the nanosecond —
  `2026-08-13T19:18:58.444324990Z` for api, 31 days continuous. The second
  stack, `mokri-potok-potok-api-1` (`3687b2a8eea0`), and `doco-cd-apprise-1`
  (`87262c930175`) are untouched as well. Only `doco-cd-doco-cd-1` was
  replaced, `f86d16ac688a` → `d6f7293690b6`. `/api/healthz` 200 throughout.
- **`doco_cd_info` answers the version question without raising `LOG_LEVEL`.**
  This box logs nothing at `warn`, so the old recipe (write `/tmp/ll.yaml`,
  recreate the daemon with `LOG_LEVEL=info`, read, recreate again) cost two
  extra container recreates just to confirm a version. The Prometheus endpoint
  on 9120 carries it instead, and 9120 is not published — reach it from the
  sidecar on the compose network:

  ```sh
  docker exec doco-cd-apprise-1 curl -s http://doco-cd:9120/metrics
  ```

  `doco_cd_info{log_level="warn",start_time="…",version="v0.117.1"} 1` is the
  version. `doco_cd_polls_total` per repository is the poll proof
  (13 for `gaias-choice`, 7 for `mokri-potok-portal` in the first minutes).
  And `doco_cd_deployment_stage_duration_seconds_count{stage="pre-deploy",
  outcome="skipped"}` is the *no-redeploy* proof, per stack, as a number —
  every poll skipped, no stage past `init` ran. That last metric is **new in
  0.116.0**; `doco_cd_info` and `doco_cd_polls_total` were always there, we
  just never used them. The `/tmp/ll.yaml` trick below is still the way to read
  actual log lines; it is no longer needed for these three questions.
  ⚠ **It also went unexercised this round** — the metrics answered everything,
  so nobody wrote `/tmp/ll.yaml` on this box since the 0.114.0 bump. Treat the
  recipe as last-verified 2026-09-02, not 2026-09-13, and expect to debug it
  rather than trust it the next time a log line is genuinely needed.
- `/v1/health` is reachable the same way (`curl -s http://doco-cd/v1/health` →
  `{"content":"healthy",…}`), which is the same answer the container
  healthcheck gives.

## Redeploy / operate (quick reference)

- **New backend image (fully automatic):** push to `main` touching `backend/**`
  → `build-backend.yml` builds + pushes the image **and rolls the deploy** (final
  step bumps `BE_TAG` in `.doco-cd.yml` with a `[skip ci]` commit and pushes) →
  doco-cd reconciles within ~30s. One human push, no `task be:deploy`. Needs
  `contents: write` on the workflow (granted). See step 9 below.
- **Manual roll / rollback:** `task be:deploy BE_TAG=sha-…` (→ `deploy/release.sh`)
  writes that tag to `.doco-cd.yml` and pushes — for pinning an older image or
  shipping without a fresh `backend/**` build.
- **Controller (Layer 0) change:** edit `deploy/controller/{compose,poll}.yaml`
  in the repo, then `task doco:sync` (scp non-secret files → `/opt/doco-cd/` +
  `docker compose up -d`). doco-cd does NOT self-deploy its own config.
  **On a version bump, `docker pull` the new pin on the VM first** — `sync.sh`
  scps before it `up -d`s, so a failed pull strands the file (step 13). Verify
  after: `doco_cd_info` for the version, `doco_cd_polls_total` for the polls,
  `deployment_stage_duration_seconds{stage="pre-deploy",outcome="skipped"}` for
  "no stack was redeployed" — all from the metrics endpoint, no `LOG_LEVEL` edit.
- **Secret rotation / notify-target change:** edit the repo-root `.env`
  (gitignored source of truth) — e.g. `TELEGRAM_CHAT_ID` to move the deploy-ping
  target — then `task doco:secrets` (→ `push-secrets.sh`): streams `.env` + a
  derived `APPRISE_NOTIFY_URLS` 0600 over ssh into `/opt/doco-cd/secrets.env` and
  reloads the daemon. Never a local temp, never git; kept out of `doco:sync` so a
  routine config sync never rewrites secrets.
- **From-scratch VM:** `deploy/controller/bootstrap-vm.sh` (Docker + `/srv` dirs
  + fetch controller config + seed secrets template + up). Doubles as OpenTofu
  userdata.
- **Manual fallback** (doco-cd itself down): SSH in and, from `/opt/doco-cd`,
  `docker compose up -d` restarts the daemon; it re-clones and reconciles.
  Secrets live in `/opt/doco-cd/secrets.env` (`chmod 600`, never committed); the
  non-secrets (`API_DOMAIN`/`CORS_ORIGINS`/`BE_TAG`) live in `.doco-cd.yml`.
- **Logs:** `task be:logs` (app api; `SVC=caddy` for the edge) or
  `docker logs -f doco-cd-doco-cd-1` (the daemon). The daemon logs **nothing**
  at the committed `LOG_LEVEL=warn` — that is the point of `warn`, but it also
  means "did it poll?" is unanswerable from logs. Raise it for one capture
  without editing the repo file or leaving drift: write a
  `services: {doco-cd: {environment: {LOG_LEVEL: info}}}` override to
  `/tmp/ll.yaml`, `docker compose -f compose.yaml -f /tmp/ll.yaml up -d`, read
  the version + `job completed successfully` lines, then plain
  `docker compose up -d` to land back on the repo file.
- **DB backup (host cron, never `cp` a live WAL db):** the db is a host file:
  `sqlite3 /srv/gaias-choice/data/gaia.db ".backup '/srv/gaias-choice/backups/gaia-$(date +%F).db'"`.

## Security posture & hardening plan

**Current inbound surface (what's actually reachable):**
- **13337/tcp** — SSH, key-based, root login (moved off 22, see below).
- **80/443/tcp** — Caddy only. Caddy serves **two named sites**:
  `{$API_DOMAIN}` → `api:8787`, and
  `{$POTOK_DOMAIN}` → `potok-api:8788` for the whole host (added 2026-09-06,
  see the portal section below). Requests to the raw IP or any other `Host`
  still match no site (and get no cert on 443), so they don't proxy through.
  Neither `api:8787` nor `potok-api:8788` is host-published — both live only on
  the compose network. So the effective surface is 13337 + 80/443, nothing else.
- **Scanner paths edge-dropped:** the `Caddyfile` matches the usual bot-scan
  paths (`/.env*`, `/.git*`, `/wp-*`, `*.php`, …) with an `@scanners` matcher
  and `abort`s the connection — they never reach the api, so its logs stay
  clean. This is **not** rate limiting/IP banning: Caddy has no native counter
  keyed on response status (that's the fail2ban pattern), and real rate
  limiting would need the `caddy-ratelimit` plugin → a custom image off the
  stock `caddy:2-alpine`, not worth it while these scans just 404 harmlessly.
  Extend the matcher as new patterns appear in the logs.
- **Transport hardening in the `Caddyfile`** (the backend already owns per-field
  size caps + a 429 rate limiter, so these are only the edge-level gaps): HSTS
  (`max-age=31536000`, no preload/includeSubDomains), `X-Content-Type-Options:
  nosniff`, `Server` banner dropped, and a **10MB `request_body max_size`** — the
  backend's 256KB/1MB caps only fire after gin buffers the whole body, so the cap
  stops a giant POST to the unauthenticated `/track`/`/auth/*` endpoints from
  OOMing before the handler runs. Deliberately NOT added: TLS tuning (Caddy
  defaults are already A+), CSP/X-Frame-Options (this host serves JSON to a
  cross-origin `fetch`, never framed HTML — those belong to the SPA edge), and
  real rate limiting/WAF (that's the future Cloudflare's job).

**The gap:** nothing at the network edge. There's no Hetzner Cloud Firewall and
no host firewall. Today that's fine (only Caddy publishes ports), but there's
no guard against a *future* mistake — the moment any container gets a
`ports:` mapping (e.g. `8787:8787`), it's world-exposed with no warning.

**Do NOT reach for host `firewalld`/`ufw` for this.** Docker publishes ports by
writing its own DNAT/filter iptables rules that are evaluated *before*
firewalld's `INPUT` chain, so a published container port bypasses the host
firewall entirely. Host firewalld would give false confidence for exactly the
failure mode above. (This refines step 3's "no host firewall needed" note: true
for the current state, but host firewalld is the wrong tool regardless.)

**The right control — Hetzner Cloud Firewall (applied).** It filters at the
network edge, *before* packets reach the VM's NIC and before Docker's iptables
run, so it also catches accidental container port-publishes. Free, declarative,
and Terraform-able later (`hcloud_firewall`). Applied to the VM as firewall
**`gaias-choice-edge`**:
- **Inbound allow:** `13337/tcp` (SSH), `80/tcp` (Caddy ACME + redirect),
  `443/tcp` (Caddy HTTPS) — each `0.0.0.0/0` + `::/0`. Tighten SSH to the
  owner's IP later if it becomes static.
- **Inbound:** everything else dropped (Hetzner default-deny for unlisted
  inbound). Verified: `8787` from off-VM is now filtered.
- **Outbound:** allow all — no outbound rules added, which Hetzner treats as
  unrestricted egress (the API needs GHCR, Postmark, Telegram, Anthropic,
  Let's Encrypt).
- Created with `hcloud firewall create/add-rule/apply-to-resource` (token from
  the `GAIA_HETZNER_TOKEN` keychain env var). To reproduce/automate: three
  `--direction in --protocol tcp --port <22|80|443> --source-ips 0.0.0.0/0
  --source-ips ::/0` rules, then `apply-to-resource --type server`.

**SSH moved to port 13337 (done).** Cuts the constant 22 brute-force/scan
noise. Done as a reversible drop-in `/etc/ssh/sshd_config.d/10-port.conf`
(`Port 13337`) — the stock `sshd_config` has no `Port` line, so the drop-in is
the only source; revert = delete the file + `systemctl restart sshd` (and
re-add the edge rule). SELinux is **Permissive**, so no `semanage port`
relabel was needed (it would be on Enforcing). Cutover order (to avoid
lockout): add edge rule 13337 → drop-in `Port 22` + `Port 13337` → verify a
fresh 13337 login → drop-in `Port 13337` only → remove edge rule 22. The
deploy tooling was updated to use it: `deploy/release.sh` + `task be:deploy`
take **`VM_PORT` (default 13337)**, wiring `ssh -p` / `scp -P`.

**Still deferred (defense in depth):** in `sshd_config` set
`PermitRootLogin prohibit-password` + `PasswordAuthentication no` (key auth is
already the only working path; makes it explicit). Not done — touches live
sshd auth; apply with console access as the lockout fallback.

## Second stack on the VM — mokri-potok portal backend

The VM also hosts the backend of `github.com/dennislapchenko/mokri-potok-portal`.
Wiring, all in git: `controller/poll.yaml` has a second poll entry for that
repo (`main`, 60s); its own `.doco-cd.yml` deploys compose project
`mokri-potok`, one service `potok-api` (GHCR image, SQLite bind-mounted at
`/srv/mokri-potok/data`, owned by uid 65532, no secrets). `app/Caddyfile`
`handle /potok/*` strips the prefix and proxies to `potok-api:8788`; the portal
compose joins `gaias-choice_default` as an external network so the name
resolves. No new hostname, DNS or edge port. Controller and Caddy stay this
repo's — a portal change needing a route or poll entry is a change here. Note:
`task doco:sync` does not reload a changed `poll.yaml` (the daemon reads it at
start); a `docker compose restart doco-cd` in `/opt/doco-cd` is needed.
`PASS_ENV=true` forwards this VM's `secrets.env` into the portal stack's
interpolation scope too; the portal compose references none of those vars.

### 2026-09-06 — the portal gets its own hostname (`vas.mokri-potok.si`)

- **DNS:** the village created an A record `vas.mokri-potok.si` → this VM's IP.
  (Their action, on their own domain; this repo does not manage that zone.)
- **Why:** `potok-api` now serves its **own frontend** as well as the JSON api —
  the static files are embedded in the Go binary. So the new host must proxy
  **everything** to `potok-api:8788`, not just an api prefix.
- **Caddy:** `app/Caddyfile` gained a **second site block** keyed on
  `{$POTOK_DOMAIN}`, with automatic Let's Encrypt TLS, the same hardening as the
  api site (HSTS `max-age=31536000`, `nosniff`, `-Server`, 10MB
  `request_body max_size`, `@scanners` → `abort`) and a bare
  `reverse_proxy potok-api:8788` — no `uri strip_prefix`.
  - The scan list drops `/config*` and `/vendor/*` versus the api site: this host
    serves an SPA and both are plausible real asset paths there.
  - **No Content-Security-Policy, deliberately.** The portal page embeds the
    Slovenian weather agency's widget in an iframe from
    `https://vreme.arso.gov.si`; a `default-src` CSP would blank it. Any future
    CSP must carry `frame-src https://vreme.arso.gov.si`.
- **`POTOK_DOMAIN`** is wired exactly like `API_DOMAIN`: a non-secret value in
  `.doco-cd.yml`'s `environment:` (in git), passed into the caddy service in
  `app/compose.yaml`. **No `:-` default** — an empty value leaves an unnamed site
  block, the Caddyfile fails to parse, and Caddy takes the api site down with it.
- **The old `/potok/*` block on `{$API_DOMAIN}` stays**, marked transitional in
  the Caddyfile. The portal's GitHub Pages frontend still calls it. Delete it
  once that Pages copy is retired.
- **No other change needed:** ports 80/443 are already open on the
  `gaias-choice-edge` Hetzner firewall and already bound by this Caddy;
  `potok-api` stays unpublished on the compose network; the new cert lands in the
  existing `/srv/gaias-choice/caddy` bind mount. A push to `main` is the deploy —
  the Caddyfile change force-recreates the caddy service alone.

### 2026-09-08 — the transitional `/potok/*` route goes
- The portal's GitHub Pages frontend is gone (everyone moved to
  `vas.mokri-potok.si`; the Pages site was disabled in that repo), so the
  `handle /potok/*` block on `{$API_DOMAIN}` was deleted. Anything still calling
  `/potok/api/...` now falls through to `api:8787` and 404s. `poll.yaml`'s
  comment updated to match. Caddyfile comment about "no CSP" rewritten the day
  before: the portal sets its own CSP in its Go server.

## Third and fourth sites — the Porta Pagi landing page and demo

### 2026-09-24 — `porta-pagi.gardenofatlantis.com` and `pp-demo.gardenofatlantis.com`

The village portal became a product, Porta Pagi (`dennislapchenko/porta-pagi`;
the landing page is in `dennislapchenko/porta-pagi-cloud`, private). The
owner bundles its landing page and demo village on this VM for now. Two
services in `app/compose.yaml`, two Caddy sites, four non-secret variables
in `.doco-cd.yml` — nothing hand-run on the box:

- `pagi-site`: `ghcr.io/dennislapchenko/porta-pagi-site` (caddy:2-alpine
  serving the static page on :8080), rolled by `PAGI_SITE_TAG`.
- `pagi-demo`: `ghcr.io/dennislapchenko/porta-pagi-demo` (the product's demo
  build: a door with no code, an hourly wipe-and-reseed), rolled by
  `PAGI_DEMO_TAG`. **No bind mount:** `/data` is a 256 MB tmpfs, mode 1777
  so the image's nonroot user can write, and the container seeds itself on
  an empty ground. `mem_limit: 384m`, `cpus: "0.5"`, `pids_limit: 128`. A visitor holds a
  steward's powers for an hour, and the product's own docs say a demo never
  runs beside a village; this box hosts one. The tmpfs and the three caps
  are what bound the blast: a visitor can kill the demo and take half a core
  with it, not fill the disk the village's SQLite is on or starve its CPU.
- Both A records → this VM, created by the owner. Both GHCR packages start
  **private** (a package inherits its repo's visibility on first push):
  **flip both to public before pushing this** — `docker compose up` fails
  on the first unpullable image and the whole stack's reconcile stops with
  it, api and village included. Checked with the registry's anonymous
  token flow (`ghcr.io/token?scope=repository:<pkg>:pull`, then the
  manifest — 200 public, 403 private), not with a bare manifest GET, which
  is 401 for public and private alike.
- Pushed 2026-09-24 (`58ed96f`) once both packages were public and both
  records resolved to this VM. Both hosts answered 200 within 40 s of the
  push, with their Let's Encrypt certs; `caddy` was recreated for the new
  Caddyfile, `api` and `potok-api` were not touched. Verified on the box:
  `pagi-demo` logged "demo village seeded on boot", its healthcheck passes,
  `docker inspect` shows the memory, CPU and pid caps and the 256 MB tmpfs.
- Same day, on review: **the two Porta Pagi services moved to their own
  compose network, `pagi`, with Caddy on both.** On the stack's default
  network the demo container could have named `potok-api:8788` and
  `api:8787`; a demo visitor holds a house's login for an hour, so it gets
  Caddy as its only neighbour. Verified after the roll: `docker inspect`
  shows `pagi-demo` on `gaias-choice_pagi` alone, `caddy` on both.
- **Expected noise:** `reconciliation: events: [unhealthy, die]` is per
  stack, not per service. A visitor who pushes the demo past its memory
  cap kills it, which is a `die`, which is a stack-wide `compose up` and an
  [R] Telegram ping — and can recur each hour. That ping is the demo, not
  the village or the api. Dropping `die` for cause is backlog item 8 of the
  doco-cd notes, unchanged by this.

### 2026-09-28 — the demo takes switches, dues, roster, orders, feedback

Tags rolled: the demo to porta-pagi `7b2f94e` (the switches, a steward's
settings page, a dues ledger and a duty roster behind switches, order runs,
the feedback chip, contacts by letter, a bilingual pt-PT/English seed), the
landing to `a6a3ffa`. `pagi-demo` gains three pass-through variables for the
feedback chip — `RESEND_API_KEY`, `FEEDBACK_TO`, `EMAIL_FROM`, all `${…:-}`:
empty until the owner puts them in `/opt/doco-cd/secrets.env`, and empty
means feedback is written to the container's log. Hence a `json-file` log
cap on that service (10 MB × 3): the log holds reply addresses until email
is wired. The demo build refuses a switch flip server-side, so no visitor
takes a room from the others for the hour.

### 2026-10-08 — an access log for the landing and the demo

`396965d`: Caddy writes an access log for `{$PAGI_DOMAIN}` and
`{$PAGI_DEMO_DOMAIN}` only, to `/srv/gaias-choice/caddy-logs`
(`pagi-site.log`, `pagi-demo.log`; bind-mounted as `/var/log/caddy`,
created by docker on the first `up`, nothing by hand). The address is cut
to /24 or /48 before it is written, cookies are redacted by Caddy itself,
assets are `log_skip`ped; 10 MiB × 10 rolled, 90 days at most. The village
and the api keep no access log. Read by porta-pagi-cloud's `task lens`. The
landing, rolled to `814883a` in the same push, says in its promises that it
keeps this log. Verified on the box: `caddy` and `pagi-site` recreated,
`potok-api` and `pagi-demo` untouched, both files written as root 0600 with
the address masked. The first lines are the verification's own: one
request with a `chatgpt.com` referrer at 10:49 UTC is the test, not a
visitor.

## Deferred (not done yet, by design)
- **Terraform the edge firewall** — `gaias-choice-edge` is live but was created
  imperatively via `hcloud`; codify it later as `hcloud_firewall` + attachment.
- **SOPS+age secrets** — secrets currently reach doco-cd via `PASS_ENV` from the
  VM-only `/opt/doco-cd/secrets.env` (step 9). Phase 3 would encrypt them with
  SOPS+age and commit the encrypted env to the repo (doco-cd auto-decrypts with
  an age key mounted as a docker secret), removing the VM-only secret file.
- **DB backup cron** — dirs exist (`/srv/gaias-choice/backups`); the host cron
  itself is not yet installed.
