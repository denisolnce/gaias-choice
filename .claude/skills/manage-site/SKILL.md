---
name: manage-site
description: >-
  This is the user's website — "the site," "the page," "the roadmap," "a
  review" mean Gaia's Choice (this repo). Always invoke first, before touching
  any file, for ANY site change: content, copy, reviews, guides, pages,
  nav/footer, images, roadmap, theme/palette work, and building, previewing,
  deploying, or debugging the site. Even trivial-looking requests ("fix this
  typo") must go through it — house rules make one-line edits non-trivial.
  Skip only if the user names a different repo/project or asks a general
  web-dev question unrelated to their site.
---

# Managing Gaia's Choice

A content/affiliate site about natural living on the road with a baby — honest
gear reviews, free courses (the Compass), a handwritten Journal — built as a
Vite + React SPA where **all content is Markdown/YAML in `content/`** and all
tooling runs in containers. Read the repo's `CLAUDE.md`
for architecture; this skill is the operating manual on top of it.

## First, classify the task

| Task smells like… | Do this |
| --- | --- |
| Reviews, Compass courses, Journal entries, pages, site copy, images, roadmap, voice/tone | Read `references/content-editing.md` |
| Writing/finishing Compass course chapters (any batch size) | Also invoke the `write-epic-course` skill — the production workflow |
| Build, verify, preview, deploy (the static site), routes, themes, components, CSS | Read `references/development.md` |
| A marked screenshot + "fix this UI element" | The four-step recipe: `references/development.md` "UI fix from a marked screenshot" (the `frontend-fluffer` skill routes here too) |
| The Go API, login/auth, the live-edit portal, LLM enrich/translate, backend deploy, `be:*` tasks | Read `references/backend.md` — backend detail is out of `CLAUDE.md` and loads only here |
| Both (e.g. "add a page" = content + route) | Read both — page wiring spans them |

## Text shortcuts (end-of-prompt toggles)

A symbol combo at the very end of the owner's prompt includes/excludes steps
from the playbooks for that prompt only:

- `!!!` — **no commit, no push.** Do the work and verify as usual, but skip
  the end-of-task commit question entirely; leave everything in the working
  tree.

New shortcuts get added to this list as the owner defines them.

## Big tasks: options doc → action plan → execute

For any big task — architectural changes, anything touching build/deploy or
the content model, or work that will span sessions — don't jump to code.
The owner's chosen process:

1. **Options doc first.** Write `context/<topic>/<topic>-paths.md`: the
   current state as *verified in the repo* (read the actual files — no
   assumptions), 2–4 candidate paths with tradeoffs, cross-cutting decisions,
   and a recommendation. Paths must respect the house constraints (repo
   simplicity, minimal deps, content-as-data, browser speed).
2. **Owner picks or alters a path.** Don't proceed on the recommendation
   alone.
3. **Action plan second.** Write `context/<topic>/action-plan.md` for the
   picked path: numbered steps, each with the files it touches and a
   verifiable success criterion; call out risks and their mitigations.
4. **Execute only after the owner agrees to the action plan**, following it
   step by step and updating it if reality diverges.
5. **When done**, move the `context/<topic>/` dir to `context/archive/` and
   fold any durable facts into the proper docs (per "One fact, one home").

Small tasks (copy edits, single components, content entries) skip this —
classify-and-do as usual.

## Engineering audits ("is X awesome engineering?")

The owner periodically asks for an audit of one area (the Taskfile, a
directory, configs). The format that works — audit first, execute only what's
approved:

1. **Verify everything in the repo** — read the actual files, grep the actual
   references; no findings from memory. Before flagging something as a bug,
   check whether it's a *documented decision* (code comments, docs) — e.g.
   upcoming review stubs deliberately live in `en/` only (`content.ts`
   `upcomingFor`).
2. **Report:** verdict up front → "what's already right" (verified, so the
   owner knows it was checked, not skipped) → ranked fix-worthy findings →
   "nits considered and skipped" with one-line reasons.
3. **Wait for the owner to pick items.** They often take some themselves
   (especially their own in-progress content — don't touch that uninvited).
4. **Execute the picked items, verify proportionally, commit per
   behavior.yaml, and fold any durable learnings into the docs.**

## Non-negotiables (violating any of these is the failure mode)

1. **Truth-first content.** The site's only asset is trust. Never write a
   review or experience claim the owners haven't actually lived. If asked to
   add a review, get the real testing notes (or use what the user provided);
   if there are none, say so and either decline or clearly mark the entry as
   placeholder. Never invent durations ("after 3 months of use…"), photos, or
   test results.
2. **npm never touches the host.** All install/build/typecheck runs via
   `task <cmd>` inside `node:22-alpine` (deps in a Docker volume). Never run
   plain `npm install`/`npm run` on the host, never add a dep with install
   scripts (`.npmrc` has `ignore-scripts=true`), and treat adding *any*
   dependency as a decision to surface to the owner.
3. **Content is data, code is layout.** Adding/editing a review, guide, theme,
   or nav item must not require touching `src/` — if it seems to, the content
   model is being bypassed. The one exception: standalone pages need a route
   in `App.tsx` (documented in the content reference).
4. **Affiliate compliance.** No real affiliate program links until every
   placeholder review is deleted (grep `EXAMPLE`). Any page with affiliate
   links needs a disclosure line before the first link; `/disclosure` holds
   the full policy (Amazon's required sentence is pre-staged in a comment
   there). Affiliate links get `rel="sponsored"` (handled by frontmatter
   `affiliateUrl`).
5. **The almanac is serious astrology.** `lib/astro.ts` computes a real
   ephemeris (Daragan-style, tropical ecliptic-of-date). Never replace it with
   a pop "sun-sign horoscope" API or dumb it down; extend the derivations
   instead.
6. **Compass is the ONLY computer-assisted section; everything else is written
   by hand.** The site's content contract — this rule is its single canonical
   statement; other docs link here rather than restating it. **Compass**
   (`/compass`, courses) is openly AI-drafted from the owners'
   context/influences/voice, then edited — disclosed by the
   `compass.provenance` banner on the landing page. **Journal** (`/journal`,
   the human blog) and **reviews** are written by the owners; truth-first
   applies with full force there (no fabricated trips, scenes, or durations).
   Don't blur the line: don't quietly AI-author a Journal entry or review, and
   don't strip Compass's provenance disclosure. **The one allowed machine
   assist outside Compass is *translation*** (`setPostState`/`saveFile` in
   `contentEditor.tsx` → `POST /content/translate`), governed by one rule:
   **for reviews and journal, Russian is the human original; English is
   machine-translated from it, never the reverse.** This is the *human-authoring*
   axis — keep it apart from `en/` being the *fallback* source of truth that must
   stay complete so a missing locale file never breaks the site (the fallback
   axis lives in `content/locales/README.md`). The sync is driven by the **state
   toggle**: flipping an RU review/journal post **Active** (re)translates it into
   its EN sibling (title/excerpt pinned to any existing EN wording so re-runs
   don't churn; scores/price/tags flow from RU); flipping it **Upcoming** just
   mirrors the state onto EN (no re-translation). Both the state flip and the EN
   write land in ONE commit. Editing content never auto-translates the **prose** —
   the next Active flip carries the edits over — with **one exception: the shared
   media (cover `image:` + `gallery:`) mirrors RU→EN on save** (it's shared media,
   not prose; inline body images stay per-locale). A **brand-new draft auto-seeds
   a stub of its sibling locale in the same create commit** (so the other locale's
   "in the works" rail entry is readable), or a verbatim copy when the model is
   offline. The two directions are deliberately asymmetric in **both what they
   translate and how they're marked**: a **RU draft's EN stub** is a permanent
   machine export — **frontmatter-only** (title/excerpt), stamped
   `translatedFrom: ru`, its body fully translated on the first Active flip (so
   translating it at create would be wasted). An **EN draft's RU stub** is
   scaffolding the owner **hand-finishes into the human source** — the **whole
   enriched file, translated** (a title-only stub gave them nothing to refine),
   left *unmarked* (RU never carries an AI-translation mark). This is RU's **only**
   machine assist: seeding never overwrites an existing sibling, and **editing an
   EN post never pushes prose to RU** — so hand-written RU is never overwritten by
   AI. Every
   machine-translated file
   carries a `translatedFrom:` frontmatter mark, rendered on the page ("Translated
   from Russian" / etc.), so the assist is disclosed. This is allowed because it
   *translates* existing human content — it invents nothing. Authoring (not
   translating) any Journal/review with an LLM stays forbidden.

   **Cross-locale mirror contract** — what auto-crosses locales, and which way
   (the human-authoring axis; every locale still falls back to `en` for a missing
   file regardless):

   | Section | Human original | Auto RU→EN | EN→RU |
   | --- | --- | --- | --- |
   | **Reviews** (`products/`) | RU | New RU draft seeds an EN frontmatter skeleton (same commit); Active-flip (re)translates body + carries scores/price/tags, title/excerpt pinned; Upcoming-flip mirrors state; cover + gallery mirror on **save** | New EN draft seeds an **unmarked, whole-file-translated** RU skeleton (owner hand-finishes it into the human source); never overwrites an existing RU post, never auto-translates prose afterward |
   | **Journal** | RU | Same as reviews, minus scores | Same as reviews |
   | **Compass** chapters | EN (AI-drafted + edited) | None — both locales produced together by `write-epic-course`; diagrams are one shared template filled per locale (`inside-websites` is EN-only) | n/a |
   | **`site.yaml`** non-localized (name, support, social, sidebar…) | EN | Inherited via `getSite` shallow-merge — authored once in `en/` | n/a |
   | **`site.yaml`** localized (nav, values…) + **pages** (about, contact, legal, **roadmap**) | per-locale, hand-written | None — roadmap is ticked in **both** locales by hand | never |

## Coding principles

- **DRY by default.** Every value — content, config, markup, copy — should have
  a single source of truth. Don't duplicate a wallet address, URL, label, or
  block of markup across files/locales; lift it to one place (config, a UI
  string, a shared component/template) and reference it. Non-localized data
  (payment details, brand/tech constants) is authored once in `en/site.yaml`
  and inherited by other locales via `getSite`'s fall-back-to-en merge — don't
  copy it into `ru/site.yaml`. Existing DRY machinery to reuse before you
  duplicate: shared diagram templates (`content/shared/diagrams/`), `t()` UI
  strings, `epics:`/`support:` config lists, and content-as-data
  generally.
- **Readability can outrank DRY** *after* a decision is settled. If collapsing a
  duplication makes the code/content meaningfully harder to follow (an
  over-clever abstraction, indirection for a value that will never change
  together), keep the small repetition and leave a one-line note on why. DRY is
  the default, not a mandate to abstract prematurely.

## Strategic decisions → the gaia-mentor skill

Whenever the discussion touches the project's mission, vision, positioning,
product strategy, business model, editorial direction, community, branding,
philosophy, prioritization, roadmap, growth, or any major trade-off, invoke
the **gaia-mentor** skill first — it carries the worldview (the six threads,
the teachers), the growth order (identity → community → trust → reviews →
revenue), and the questions every big decision must pass.

## Project phase awareness

The site is in **bootstrap mode** (see `/roadmap` and the "Current content
state" section of `CLAUDE.md`): the Compass founder course is a founder-facing
playbook, the Journal has one seed template entry, and product reviews are still
AI placeholders slated for deletion. The About story is
**real** (the owners really are a camper family — verified facts in
`context/persona-context.md`, "The family — real biography"); what's still
open is retelling it in their own words. Before editing content,
check the roadmap so your change moves the current phase forward instead of
polishing something scheduled for deletion. (Ticking the roadmap when a change
lands is part of the "Update the docs" loop below.)

## Thinking budget — proportional

Match reasoning effort to the task; don't burn extended thinking on mechanical
work, and don't skip it where a wrong call is expensive.

- **Think** (keep reasoning on) for: the *first* step of anything ambiguous or
  architectural — planning a change, choosing between paths, writing an options
  doc/action plan (see "Big tasks"), a git-history/commit-split decision,
  debugging a non-obvious failure, or any content/legal/provenance judgment
  where truth-first or the non-negotiables are in play.
- **Don't think** (fast, low-effort replies) for: a *batch* of known,
  mechanical executions once the plan is set — applying agreed edits across
  files, path/string renames, copy-paste-shaped edits, running the verify
  commands, staging/committing/pushing a decided change, status checks. The
  decision was already made; execution just needs to be correct, not reasoned.
- **The switch:** decide the approach with thinking on, then drop to fast
  execution for the run of steps that follow. If a "mechanical" step surprises
  you (an edit doesn't apply, a check fails, reality diverges from the plan),
  turn thinking back on for that step before continuing.
- The owner may force either mode per-prompt ("no thinking, fast replies" /
  "think about this"); that override wins for that prompt.

## Verify before declaring done — proportionally

```bash
task typecheck && task build   # vite build alone does NOT type-check
```

Run this when the change could plausibly break the build: anything under
`src/`, frontmatter or any YAML (`site.yaml`, `themes.yaml` — a quoting error
blanks a page), diagram blocks, config, or the build/deploy pipeline.

**Don't over-verify.** Skip the build when the change can't touch it: prose
edits inside a markdown body (frontmatter untouched), docs that aren't bundled
into the site (`CLAUDE.md`, skills, `references/`, `context/`, `README.md`),
or re-verifying work that was already verified and just pushed. When ~95%
certain nothing functional changed, reading your own diff *is* the
verification — say so and move on. When genuinely unsure, run it.

**Any layout/CSS/component change gets checked at mobile width too — always,
not just desktop.** This site has a real, load-bearing mobile breakpoint
(`900px`, sometimes `480px`/`820px` — see `references/development.md`), and a
change that looks right at desktop width has broken mobile before (e.g. a
rail's `order:-1` shoving content out of place). Use `preview_resize` with the
`mobile` preset (or narrower) and screenshot it — don't skip this step because
the desktop screenshot looked fine.

**Report longer runs as a woven summary.** When a run changed several files,
end with a succinct summary that interweaves *what changed where and why it
hangs together* — not a flat file list. Quote a small diff/example only where
seeing it beats describing it; plain words win otherwise. Pitch it at the
owner: a slightly rusty software engineer and a current devops — spell out
FE/framework idioms, skip explaining infra basics.

**The done-message format.** The final "work is done" message of a task (not
intermediate replies or questions) follows a fixed pattern — everything,
without miss, but skimmable, never one dense blob. Read it like a good MR
description written for a busy reviewer deciding **"safe to ship?" in under a
minute**: the diff already shows *what* changed — the message carries only
**what the diff can't** (why, blast radius, risk, proof, non-obvious calls).
**Never narrate the diff back.** One line per dimension; **drop a dimension
entirely rather than pad it with "N/A".**

- Start the message with ✅ and end it with ✅.
- Body, grouped for skimming (short lines/bullets, key nouns bold, one idea
  per line), covering only the diff-can't-tell dimensions that apply:
  - **what + why** — the headline, ≤2 sentences.
  - `💥 <blast radius>` — what re-renders, redeploys, or changes behavior; the
    #1 question on a deploy-on-push site, so **lead with it whenever it's
    non-trivial**. Say `💥 zero render impact` when nothing user-facing moves.
    Skip the line entirely for obviously self-contained changes (a typo, a doc).
  - **risk / rollback** and **proof** — proof is the claim + a pointer (build
    green, a screenshot, the compare) — **never the transcript**.
  - non-obvious **decisions**, as *"X over Y because Z"*.
- **Tiny diffs unfolded (non-negotiable):** whenever the body names a file
  whose change is **≤10 lines**, you MUST show that diff inline in a fenced
  ` ```diff ` block right there — it's small enough that seeing it beats
  describing it and saves the reader opening the file. Not optional, not
  "when it helps": if the diff is ≤10 lines, it goes in. Larger changes stay
  described, not pasted.
- Then one line per step still left: `🚀 <step that is left>` (commit
  pending, deploy pending, follow-up work…).
- Then, per open question **or** forward-looking tradeoff worth flagging:
  `⚖️ <the thing to weigh>` — also the place to offer a "when you're ready, the
  move is…" next step the owner hasn't asked for yet.
- Unrelated dirty files / a contaminated worktree get their own part
  starting with the line `🪨 Dirty Worktree:`.
- Omit the 💥/🚀/⚖️/🪨 parts entirely when there's nothing for them.

If Docker is down: `open -a OrbStack` and wait ~15s. For visual checks, serve
`dist/` statically (`task dev` needs a TTY and fails under preview harnesses) —
full recipe in `references/development.md`.

## Update the docs — every time, before you're done

**Any** change to a component, behavior, content model, workflow, or convention
**must** be reflected in the docs in the same session, so the next run instantly
knows where a thing lives and how to change it. This is not optional cleanup —
it's part of "done". Concretely, after the work:

- Update the **"Where things live" map** and the relevant **"Common dev tasks"**
  entry in `references/development.md` (add the component/task if it's new).
- Update `CLAUDE.md` when an architecture fact or contract changed (e.g. a new
  `site.yaml` field, a component that stopped being a dropdown, a new locale
  hook). Fix any line the change made stale — don't leave the docs describing a
  state the code no longer matches.
- Update this `SKILL.md` when a rule/process changed.
- Update `README.md` when the public pitch, stack, commands, or deploy story
  changed — it's a short pitch that must never rot.
- Update the **public roadmap** when the change lands, advances, or adds scope
  to a roadmap item — tick/edit the item **in both locales**
  (`content/locales/en/pages/roadmap.md` + `ru/…`) and refresh both "Last
  updated" footers. It's prone to drift and it's public; maintenance rules in
  `references/content-editing.md` "The roadmap".

If you changed something and touched no doc, assume you forgot one — re-check.

**One fact, one home.** Each fact has exactly one canonical location; other
docs link to it instead of restating it: process rules + the provenance
contract → this `SKILL.md`; architecture + invariants → `CLAUDE.md`;
static-site change-X-edit-Y mechanics → `references/development.md`; the Go
API + auth + live-edit portal → `references/backend.md`; authoring procedures
→ `references/content-editing.md`; voice/worldview/course outlines →
`context/`; public pitch → `README.md`.

**Docs describe current state only — updating means shrinking too.** No dates,
no "renamed from X", no "shipped on <date>" changelog framing — git history
answers those. When you touch a doc, also delete anything that has become
history rather than state, and collapse restated facts into a link to their
canonical home. A doc that only ever grows ends up skimmed, not read.

**Periodic hygiene (roughly monthly, or whenever the docs feel heavy):**
compact the docs per the rules above, move finished plans from `context/` to
`context/archive/`, prune stale one-off entries from
`.claude/settings.local.json`, and run the `consolidate-memory` skill
(`/anthropic-skills:consolidate-memory`) so the assistant's cross-session
memory gets the same treatment — memories should hold preferences and
pointers, not facts the repo docs already record.

## Deepen the persona — one closing question (optional)

At the **end of a content-working session**, you *may* close with a single
ultra-deep question for the owner, per the **`deep-questions`** skill — it keeps
the site's worldview alive and feeds `context/persona-context.md`. Optional and
non-naggy: skip it when the session was heavy, purely mechanical, or the owner is
mid-flow. If they answer, record it verbatim per that skill.

## Committing & shipping

**Read `.claude/behavior.yaml` first — it gates this whole section.** The two
flags there (`AUTO_COMMIT_PUSH`, `COMMIT_PUSH_FOLLOWING`) decide whether to ask
and whether to follow the deploy; the steps below describe *how* to ship once
the gate says go. The `!!!` end-of-prompt shortcut always overrides both to "no
commit, no push".

**The gate.** Once a change is verified (typecheck + build green):
- `AUTO_COMMIT_PUSH: false` → **ask the owner whether to commit** (plain yes/no);
  proceed only on confirmation.
- `AUTO_COMMIT_PUSH: true` → **skip the ask**; commit + push automatically. (The
  verify gate is *not* skippable — typecheck + build must be green first, since a
  push to `main` is a production deploy.) **Shared-tree guard:** if the working
  tree holds another agent's uncommitted work (your edits share files with
  theirs, or unrelated dirty files you didn't touch are present), do *not*
  auto-commit — surface it and ask how to scope the commit, even with the flag
  on. Never sweep another agent's half-done work into your push.

Then, in either case:

1. **Commit message = Conventional Commits:** `<type>: <short imperative
   summary>`, `<type>` being the semantically correct one — `feat`, `fix`,
   `docs`, `content`, `refactor`, `style`, `chore`. One logical change per
   commit. Examples: `feat: default site language to Russian`,
   `docs: add do-not-translate glossary for translations`. End the message with
   the `Co-Authored-By: Claude ...` trailer.
2. **Current phase — direct to `main`.** Commit on `main` and `git push origin
   main`, then follow the deploy per `COMMIT_PUSH_FOLLOWING`.

   **Do it inline by default — don't spawn a background ship agent for a simple
   push.** A commit + push (even a two-commit split) is mechanical; run it in
   the main session and background only the single deploy-conclusion check (a
   `run_in_background` bash `sleep && gh run list`) so the owner isn't blocked
   waiting on CI. **Reserve the background *agent*** (Agent tool,
   `run_in_background: true`) for a genuinely long follow-through that would
   otherwise stall the owner — chiefly any `backend/**` change (poll image
   build → `task be:deploy`). Whoever ships, the sequence is the same:
   - **`COMMIT_PUSH_FOLLOWING` governs the *Pages* watch only:** `false` ⇒ wait
     ~60s and check the "Deploy to Pages" run's **conclusion once**
     (`gh run list`; no `gh run watch`, no curl); `true` ⇒ poll "Deploy to
     Pages" to its conclusion.
   - **Backend deploy is fully automatic on a `backend/**` change.** The pushed
     diff touching `backend/**` fires the "Build backend image" CI run
     (`paths`-filtered), which builds the image **and rolls the deploy itself** —
     its final step bumps `BE_TAG` in `.doco-cd.yml` (a `[skip ci]` commit) and
     pushes, so doco-cd on the VM reconciles within ~30s. You don't run
     `task be:deploy` for a normal ship; just watch the CI run go **green** and
     confirm the reconcile landed (live `/api/healthz`, or on the VM `docker logs
     $(docker ps -q --filter label=com.docker.compose.service=doco-cd)` — by
     label, the name grows per self-update handover). `task be:deploy` is now **manual
     rollback/pin only** (`BE_TAG=sha-…`) — see `references/backend.md`.

   Either way: GitHub Pages fails transiently on its own side ("Deployment
   failed, try again later" while the build is green) — on that failure
   recover with `gh workflow run "Deploy to Pages" --ref main` (after
   confirming remote `main` HEAD is still the pushed commit), up to 2 fresh
   runs, re-checking each. **Never `gh run rerun`** — build+deploy are one job,
   so a rerun re-uploads the `github-pages` artifact into the same run and
   deterministically fails with "Multiple artifacts named github-pages"
   (documented in `.github/workflows/deploy-pages.yml`; workflow_dispatch starts
   clean). Every deploy publishes the full current build, so a failed run is
   fully recovered by the next successful one.
3. **Future (not yet — do this only when the owner switches to it):** work on a
   `feature/<slug>` branch, push it, and open a GitHub PR instead of committing
   to `main` directly.

Details and the exact command sequence live in `references/development.md`.
