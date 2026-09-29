# Passwordless auth (magic-link + passkeys) — options

> **Status: Path A (magic link) is implemented** — see CLAUDE.md "Backend"
> for the shipped state. This doc stays active for Path B (passkeys),
> which waits on the custom domain.

Goal: login becomes "type your email, click a link" and, on devices that have
one, "touch the fingerprint reader". No passwords for regular users.

## Current state (verified in repo)

- **Sessions are already method-agnostic.** `backend/internal/auth/auth.go`
  `issueSession(user)` mints an opaque 30-day bearer token (sha256 → `sessions`
  table). Login and register both end there. Any new auth method just needs to
  end there too — no session rework.
- Token plumbing to reuse: `randomToken()` / `hashToken()` in `auth.go`.
- Endpoints are spec-first: born in `backend/openapi.yaml`, `task be:gen`,
  handlers implement `StrictServerInterface`. Per-IP rate limiter already
  guards login/register.
- Store: numbered migrations in `backend/internal/store/migrations/` (next:
  `004_…`); all SQL in `store.go`.
- FE: `src/lib/session.tsx` owns token + login/register/signOut and renders
  `components/LoginDialog.tsx` (email+password form today). `#edit` hash
  precedent exists for URL-triggered flows (`editMode.tsx`).
- **Domains:** site = `denisolnce.github.io/gaias-choice` (Pages, no
  custom domain). API = `gaias-choice.gardenofatlantis.com` (Hetzner VM,
  Caddy). The owner controls `gardenofatlantis.com` DNS.
- Go deps: 4 direct, adding one is an owner decision. No mail or WebAuthn
  code anywhere yet.

## The two building blocks

**Magic link** = email delivery + a short-lived single-use token that trades
for a normal session. Needs outbound SMTP; zero new Go deps (`net/smtp` is
stdlib and fine for authenticated submission on :587).

**Passkeys (WebAuthn)** = browser-native `navigator.credentials` (zero FE
deps) + server-side ceremony validation. Server side is CBOR/COSE/signature
verification — not hand-rollable; needs `github.com/go-webauthn/webauthn`
(the standard Go library, ~4 transitive deps).

**The passkey trap: RP ID is forever.** A passkey binds to the domain it was
created on. Today that would be `denisolnce.github.io` (valid RP ID —
github.io is on the Public Suffix List), and **every passkey dies the day the
site moves to its real domain**. Magic link has no such binding.

## Paths

### A — Magic link now, provider SMTP (recommended first step)

Email-only login. Unknown email = auto-register as viewer on verify (same
open-registration policy as today, minus the password). Password login stays
as the admin/bootstrap fallback until phase B.

- BE: `POST /api/auth/magic` (accepts `{email, locale}`, always 200 —
  no account enumeration; rate-limited like login) and
  `POST /api/auth/magic/verify` (`{token}` → session or 401). New
  `login_tokens` table (sha256, user email, ~15 min expiry, single-use).
  New `internal/mail` pkg: `net/smtp` STARTTLS submission, env
  `SMTP_HOST/PORT/USER/PASS/FROM` + `PUBLIC_SITE_URL`. Unset ⇒ magic
  endpoints answer 503, same pattern as the content seam.
- FE: LoginDialog becomes email-first ("send me a link"; password behind a
  small "use password" toggle for admin). Link lands on
  `PUBLIC_SITE_URL/#magic=<token>`; `session.tsx` spots the hash (like
  `#edit`), calls verify, stores the session, cleans the hash.
- Email: plain text, two hardcoded locale strings (ru/en) picked by the
  `locale` the FE sends. No template engine.
- New deps: **zero** (Go and JS).

### B — Passkeys, after the custom domain (the end state)

Prereq: site on its permanent domain (also unlocks nicer magic-link URLs and
same-site cookies later). Then:

- BE: add `go-webauthn/webauthn` (owner-approved dep), `credentials` table
  (credential id, public key, sign count, user id, label), 4 endpoints:
  begin/finish **register** (session-authed — you add a passkey while signed
  in via magic link) and begin/finish **login** (discoverable credentials, so
  usernameless "sign in with passkey" button). Both finish paths end in
  `issueSession`.
- FE: `navigator.credentials.create/get` in LoginDialog + an "add a passkey"
  row in AccountFields. Feature-detect; button hidden where unsupported.
- Magic link stays forever as the recovery/bootstrap path (new device with no
  synced passkey, lost device). Passwords can then be removed for everyone
  (drop the column is optional cleanup; stop offering the form is enough).

### C — Self-hosted SMTP (answered, not recommended)

The honest answer to "is hosting my own just DNS records + a compose edit":
**no.** The DNS part (SPF, DKIM, DMARC, PTR) is identical to path A. What's
extra: Hetzner blocks outbound port 25 on new accounts (unblock is a support
request, typically granted only after the account ages a month); a fresh VM
IP has zero sending reputation, so Gmail/Outlook will spam-folder or reject —
and a login email in spam means a user who **cannot log in at all**; plus
ongoing blocklist monitoring. docker-mailserver/maddy make the software easy;
deliverability is the hard part and it isn't software. Revisit only if the
site someday sends volume that makes provider pricing matter (it won't for
login links).

### D — Passkeys only, no email (rejected)

No SMTP at all. Rejected: no recovery path (lost/unsynced device = locked
out), no way to bootstrap a brand-new user's first credential without some
other channel. Magic link *is* the recovery channel; D collapses into B.

## Cross-cutting decisions

1. **SMTP provider** (path A prereq): **Postmark** (chosen — best
   deliverability reputation, 100/mo free covers login links). DNS (DKIM +
   return-path CNAME) is on Namecheap; progress and the env wiring live in
   `deploy/infra-log.md` §8 / `deploy/README.md` step 3.
2. **Auto-register on magic verify?** Recommended yes (mirrors today's open
   viewer registration; one email field is the whole UX). Alternative:
   verify-known-emails-only, separate register — more friction, no security
   gain given registration is open anyway.
3. **When to drop passwords entirely:** after B, once the admin has a passkey
   registered. Until then the bootstrap-admin password path stays.
4. **Custom domain** is the real gate for B — worth scheduling on its own
   merits (it also fixes `BASE_PATH` subpath quirks and brand URLs).

## Recommendation

**A now, B when the custom domain lands, never C.** A is a small, zero-dep,
spec-first change that reuses the whole session/rate-limit/503-degradation
machinery; B is one approved Go dep + four endpoints on top; the domain move
must precede B or every early passkey breaks.
