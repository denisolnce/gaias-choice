# Deploy stack (VM + doco-cd)

This directory is the **production home of the Go backend sidecar**. It is
**live** and **GitOps-managed** by [doco-cd](https://github.com/kimdre/doco-cd)
on the Hetzner / AlmaLinux VM. Two layers, kept in separate dirs because they
have different lifecycles:

- **`app/` — Layer 1 (GitOps).** The `api` + `caddy` stack that doco-cd
  **reconciles from git**. doco-cd polls this repo and runs `docker compose up`
  from repo-root `.doco-cd.yml` (`name: gaias-choice`, `working_dir: deploy/app`)
  on change. **A push to `main` is the deploy.** A `backend/**` push is fully
  automatic: `.github/workflows/build-backend.yml` builds `backend/Dockerfile` →
  GHCR, then bumps `BE_TAG` in `.doco-cd.yml` and pushes, so doco-cd ships the
  new image within ~30s. `task be:deploy BE_TAG=sha-…` is the manual
  rollback/pin.
- **`controller/` — Layer 0 (the daemon itself).** doco-cd can't GitOps its own
  definition, so the daemon config is **hand-synced**: edit here, run
  `task doco:sync` (scp the non-secret files to `/opt/doco-cd/` + reload). The
  repo is the source of truth; the VM is synced from it.

The static site stays on GitHub Pages and never depends on this stack.

> **The provisioning history lives in `infra-log.md`** (chronological changelog
> of what was done on the VM). This README describes the *current* stack; the
> log is the *record*.

## What's in this repo

**`app/` (Layer 1 — reconciled from git):**

- `app/compose.yaml` — the `api` service (backend image, host bind mount for
  SQLite per D9) behind a `caddy` service that terminates TLS. Only Caddy
  publishes ports (80/443); `api` stays internal to the compose network.
- `app/Caddyfile` — four sites, all with automatic Let's Encrypt TLS, bot-scan
  paths edge-dropped and the same transport hardening (HSTS, body cap):
  `{$API_DOMAIN}` → `api:8787`, `{$POTOK_DOMAIN}` → `potok-api:8788` (the
  village portal, whole host — that container serves its own frontend),
  `{$PAGI_DOMAIN}` → `pagi-site:8080` (the Porta Pagi landing page) and
  `{$PAGI_DEMO_DOMAIN}` → `pagi-demo:8788` (its demo village, the product's
  demo image on a tmpfs). See its comments, and `infra-log.md` for the wiring.
  The two Porta Pagi sites alone keep an access log, address cut to /24 or
  /48, in `/srv/gaias-choice/caddy-logs` on the VM; porta-pagi-cloud's
  `task lens` reads it.
- `.doco-cd.yml` (repo root) — `name`, `working_dir: deploy/app`, and the
  **non-secret** `environment:` (`API_DOMAIN`, `POTOK_DOMAIN`, `PAGI_DOMAIN`, `PAGI_DEMO_DOMAIN`, `PAGI_SITE_TAG`, `PAGI_DEMO_TAG`, `CORS_ORIGINS`,
  `BE_TAG`).

**`controller/` (Layer 0 — the doco-cd daemon, synced with `task doco:sync`):**

- `controller/compose.yaml` — the daemon + the Apprise notification sidecar
  (docker socket, polling, `PASS_ENV`, deploy→Telegram notifications). Mirror of
  the VM's `/opt/doco-cd/compose.yaml`.
- `controller/poll.yaml` — what doco-cd watches (this repo, `main`, every 30s;
  no inbound port, no webhook).
- `controller/secrets.env.example` — the secret **key list** (no values); the
  real `secrets.env` is VM-only (below).
- `controller/sync.sh` — `task doco:sync` runs this (scp non-secret files → VM +
  `docker compose up -d`).
- `controller/push-secrets.sh` — `task doco:secrets` runs this: generate
  `/opt/doco-cd/secrets.env` from the repo-root `.env` (+ derived
  `APPRISE_NOTIFY_URLS`), stream it 0600 over ssh, reload. The one write path for
  secret values; never a local temp, never git.
- `controller/bootstrap-vm.sh` — idempotent from-scratch VM provisioner (Docker,
  `/srv` dirs, fetch controller config, seed secrets template, bring the daemon
  up). Doubles as OpenTofu server userdata.
- `release.sh` — `task be:deploy` runs this (bump `BE_TAG` in `.doco-cd.yml`,
  commit + push).

## What is NOT in this repo

- **`/opt/doco-cd/secrets.env`** — the VM-only secrets (`chmod 600`, never
  committed), loaded into the daemon and forwarded into the app stack via
  `PASS_ENV`. Holds only real secrets — the non-secret `API_DOMAIN`,
  `CORS_ORIGINS`, `BE_TAG` live in `.doco-cd.yml`. The **key list** is
  `controller/secrets.env.example`; each is optional and an unset secret 503s
  just its feature (`app/compose.yaml` comments). The three porta-pagi
  feedback keys (`RESEND_API_KEY`, `FEEDBACK_TO`, `EMAIL_FROM`) are read by
  both the village's `potok-api` and this stack's `pagi-demo`; unset, their
  feedback goes to the container log. **To wire them (owner's step):**
  1. In Resend, create an API key (sending access) and verify a sending
     domain — add the DNS records its dashboard shows.
  2. Add to the repo-root `.env`: `RESEND_API_KEY=…`, `FEEDBACK_TO=<inbox>`,
     `EMAIL_FROM=Porta Pagi <feedback@<verified domain>>`.
  3. `task doco:secrets`.
  4. **Untested:** whether a stack sees new `PASS_ENV` values before its own
     next deploy. Check with `docker inspect mokri-potok-potok-api-1 --format
     '{{.Config.Env}}'` (and the same for `gaias-choice-pagi-demo-1`); if the
     keys are missing, push any change that redeploys that stack.
  5. Send one feedback from the portal. The mail arrives, and `task vm:logs`
     in the village repo shows no `FEEDBACK (…)` line — that line is the
     log-only or failed path. Record the day in `infra-log.md`. **Written reproducibly, not by
  hand:** the repo-root `.env` (gitignored) is the single source of truth for
  values (same ones local dev uses); `task doco:secrets` (→
  `controller/push-secrets.sh`) streams `.env` + a derived `APPRISE_NOTIFY_URLS`
  over ssh into the 0600 file and reloads the daemon. Move the notify target by
  editing `TELEGRAM_CHAT_ID` in `.env` and re-running it.
- **The Hetzner edge firewall + SSH hardening** — currently applied
  imperatively (see `infra-log.md` "Security posture"); **to be codified in
  OpenTofu later**, not as shell here.
- **The backup host-cron** — `sqlite3 /srv/gaias-choice/data/gaia.db ".backup
  '/srv/gaias-choice/backups/gaia-$(date +%F).db'"` plus an offsite copy. Stays
  a **host cron**, per doco-cd's guidance — never the deploy's job. Always
  `.backup`, **never `cp`** a live WAL db. Litestream is the upgrade path if the
  portal ever holds loss-sensitive data (a sidecar in `app/compose.yaml`, no app
  changes).

## From-scratch rebuild

The VM is reproducible via `controller/bootstrap-vm.sh` (run it on a fresh VM,
or wire it as OpenTofu userdata):

1. **DNS** — A/AAAA record for `{$API_DOMAIN}` → VM IP (Caddy's automatic TLS
   depends on this; independent of the site's future custom domain).
2. **Image delivery** — `.github/workflows/build-backend.yml` pushes
   `ghcr.io/denisolnce/gaias-choice-be` (public package ⇒ VM pulls
   unauthenticated). Pin the sha in `BE_TAG`.
3. **Run `bootstrap-vm.sh`** — installs Docker, creates `/srv/gaias-choice/…`,
   fetches `controller/{compose,poll}.yaml` into `/opt/doco-cd/`, seeds
   `secrets.env` from the example.
4. **Fill secrets, then bring the daemon up.** From a laptop with the repo-root
   `.env`: `task doco:secrets` (writes `/opt/doco-cd/secrets.env` 0600 + reloads).
   On the box with no `.env`: hand-fill `/opt/doco-cd/secrets.env` (keys in
   `secrets.env.example`), then `cd /opt/doco-cd && docker compose up -d`. doco-cd
   reconciles the app stack from git within ~30s.
5. **Point the Pages build at the API** — `VITE_API_URL=https://{$API_DOMAIN}/api`
   in the Pages workflow build env.

## Local validation (no VM needed)

```sh
# app compose renders with dummy env
API_DOMAIN=api.example.com POTOK_DOMAIN=portal.example.com \
  CORS_ORIGINS=https://denisolnce.github.io BE_TAG=latest \
  docker compose -f deploy/app/compose.yaml config

# Caddyfile parses. Both domain vars are required — an empty one leaves an
# unnamed site block and the parse fails.
docker run --rm -e API_DOMAIN=api.example.com -e POTOK_DOMAIN=portal.example.com \
  -v "$PWD/deploy/app/Caddyfile:/etc/caddy/Caddyfile" \
  caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile

```

The controller compose can't be rendered locally — it `env_file`s the VM-only
`secrets.env`. It's validated on the VM by `task doco:sync`'s `docker compose up`.
