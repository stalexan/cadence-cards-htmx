# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Cadence Cards: an SM-2 spaced-repetition flashcard app with Claude-powered study assistance. It is
a from-scratch Go + HTMX rewrite of the sibling `cadence-cards-svelte` project
(SvelteKit/Prisma/PostgreSQL), with **zero NPM dependencies**, **SQLite**, and a **single
container**. The Svelte repo is the reference for feature parity — never modify it from here.

## Commands

```bash
go test ./...        # all tests (pure logic + store + httptest handler tests)
go vet ./...
gofmt -l .           # must print nothing

# Run locally (no Docker needed; CLAUDE_API_KEY optional — AI features
# degrade to error bubbles without it).  These inline vars are optional if
# they are set in ./.env, which the binary reads at startup; anything already
# in the environment takes precedence over the file.
DB_PATH=./dev.db COOKIE_SECURE=false ENABLE_PUBLIC_REGISTRATION=true \
  go run ./cmd/cadence            # → http://localhost:3000

# Create a user when public registration is off (prompts for name/password)
go run ./cmd/cadence -create-user you@example.com

# Container (final image is ubuntu:resolute; unit tests run inside the build)
docker compose up -d --build
```

There is no frontend build step: `web/templates` and `web/static` are embedded
via `go:embed` and served from the binary (as is the root `VERSION` file).
Editing them requires a rebuild.

### Leave nothing running

**The user starts the app.** Don't run `go run ./cmd/cadence` or `docker compose up` to hand over a
live instance — say what to start and let them start it. If you need a server for your own
verification, run it on a non-default port against a throwaway `DB_PATH` (never `./dev.db`), and
**kill it before you finish the task**, not at the start of the next one. The same goes for any
other background process, watcher, or container you start: clean up in the turn that created it, and
say so. Ending a turn with something still listening is a bug.

## Architecture

Request flow: **middleware chain → handler → store → SQLite**, with templates rendered server-side
and HTMX swapping fragments.

- `version.go` (repo root, `package cadence`) — embeds the root `VERSION` file as
  `cadence.Version`, the **single source of truth** for the release number: the startup log, the
  sidebar badge, and `-version` all read it, and nothing at runtime can override it. Keep it
  stdlib-only — `internal/` packages import it, so any `cadence-cards/...` import here would cycle.
  A root package is required because `go:embed` cannot reach outside its own directory, and
  `git describe` is unavailable at build time (`.git` is `.dockerignore`d). See `docs/VERSIONING.md`.
- `cmd/cadence/` — wiring only: config → store/migrations → server → graceful shutdown; hourly
  maintenance ticker; `-version`, `-create-user`, and `-backup` CLI. `-version` is handled before
  the logger, `config.Load`, and `store.Open`, so it works with a broken env and never migrates.
- `internal/config/` — env parsing (`PORT`, `DB_PATH`, `CLAUDE_*`, `ENABLE_PUBLIC_REGISTRATION`,
  `COOKIE_SECURE`, `DISABLE_RATE_LIMITING`).  `Load` first reads `./.env` (`dotenv.go`) and exports
  only keys **not already in the environment**, so the real environment always wins and a stray
  `.env` can never override compose. A missing file is fine; `.env` is `.dockerignore`d, so this is
  local-dev only.
- `internal/sm2/` — pure SM-2 algorithm, a port of the Svelte app's `sm2.ts` **including its
  tests**, verbatim except that `DaysBetween` rounds instead of floors (deliberate DST fix the JS
  source lacks). `now` is always injected as a parameter.  Due-ness uses **local-midnight day
  boundaries** (`time.Local`), so the container's `TZ` matters.
- `internal/yamlio/` — deck export/import, **byte-compatible** with the Svelte app's npm-yaml format
  (key order, defaults, `# ...` metadata header, date-only `LastSeen` as UTC calendar date,
  `Reverse*` twins, Easiness ≥ 1.3 with **no upper cap**). Golden tests pin the format — if they
  fail, fix the code, not the golden file, unless the format change is deliberate on both apps.
- `internal/store/` — **all SQL lives here**, one file per aggregate.  Migrations are embedded
  `.sql` files in `store/migrations/`, applied in filename order and tracked in `schema_migrations`
  (forward-only; add `0002_*.sql` etc., never edit an applied migration).
- `internal/ratelimit/` — in-memory port of the Svelte rate limiter (general
  1000 req/15 min per IP; auth 3 fails/30 min; IP cooldown/lockout; account lockout). Constants
  mirror `rate-limiter.ts` — keep them in sync conceptually.
- `internal/claude/` — `anthropic-sdk-go` client (`client.go`), prompt builders ported verbatim from
  `prompts/study-assistance.ts` (`prompts.go`), and the three operations (`study.go`):
  GenerateQuestion (extracts `<question>` tag), ChatAboutQuestion, ChatAboutTopic. Card content is
  always fetched **server-side by scheduleId** — the browser never supplies prompt text. Prompts
  carry `cache_control` breakpoints at stable-prefix boundaries (max 4 per request — count them
  when adding one); question generation routes to `CLAUDE_QUESTION_MODEL`/`_EFFORT` when set; API
  failures map to typed sentinels (`ErrRateLimited`/`ErrOverloaded`/`ErrBadAuth`/`ErrRefused`)
  that `internal/server/ai_errors.go` turns into distinct error-bubble copy at HTTP 200.
- `internal/markdown/` — goldmark wrapper for Claude replies. Raw HTML is escaped (no
  `html.WithUnsafe`); this is the XSS boundary — do not change it.
- `internal/server/` — handlers stay thin: parse form → store/AI call → render page or fragment.
  `render.go` parses one independent template set per page (layout + all partials + page) and
  a partials-only set for fragments.  `server.go` registers every route (Go 1.22+ method patterns on
  stdlib mux).
- `web/` — `templates/layout/` (base = app shell with sidebar, auth = centered card),
  `templates/pages/` (each defines `title` + `content`), `templates/partials/` (named `{{define}}`s
  used as HTMX fragments), `static/` (vendored `htmx.min.js` 2.0.10, `app.js`, `app.css`, favicon).

### Domain model

`users → topics → decks → cards → schedules`, all cascade-deleting downward.  A card has one
schedule per study direction (`is_reversed`, `UNIQUE(card_id, is_reversed)`); SM-2 state lives on
the schedule. Topics carry six prompt-config columns consumed by `internal/claude` (plus `name`,
the seventh `TopicConfig` field). Tags are
a JSON array in `cards.tags` (queried with `json_each`). Timestamps are RFC3339 UTC `TEXT` with
millisecond precision (`store.timeFormat`).

## Load-bearing invariants

- **Optimistic locking**: `cards.version` and `schedules.version` are compare-and-incremented (`...
  WHERE id = ? AND version = ?`); a miss returns `store.ErrVersionConflict`, which handlers map to
  **HTTP 409**. The study UI depends on the 409 flow (`grade_conflict` fragment auto-refetches the
  card).  htmx swaps error bodies only for 409 and 422 (validation) because `base.html` whitelists
  them via `responseHandling` in the `htmx-config` meta tag — other 4xx/5xx are discarded, so an
  error fragment must ship with one of those two statuses.
- **Authorization in the transaction**: every mutating store method takes `userID` and scopes rows
  via joins up to `topics.user_id` *inside the same transaction as the write* (ports the Svelte
  app's ATOMICITY.md discipline).  Handlers never check ownership themselves. Missing-or-not-owned
  is one error: `store.ErrNotFound`.
- **Strict CSP, deliberately tighter than the Svelte app**: no `unsafe-inline`, no `unsafe-eval`, no
  nonces. Consequences: no inline `<script>`/`style=` attributes in templates (the progress bar uses
  `data-progress` + `app.js` for this reason), no `hx-on:*` attributes, and htmx runs with
  `allowEval:false`. All client behavior is declarative htmx attributes or delegated `data-*`
  listeners in `app.js` (re-initialized on `htmx:afterSwap`).
- **Study session state lives in the query string** (`deckIds` repeated, `priority`, `includeNew`,
  `limit`, `total`, `completed`) — the server is stateless and sessions survive refresh. Always read
  repeated params with `r.URL.Query()["deckIds"]` / `r.Form["deckIds"]`; reading only the first
  value was a bug in the Svelte app that this rewrite deliberately fixes (along with honoring
  `priority`/`includeNew`/`limit`).
- **Client IP**: trust the nginx-set `X-Real-IP` header only; everything else is spoofable. CSRF
  protection is SameSite=Lax cookies + the `Sec-Fetch-Site`/Origin check middleware on non-GET
  requests.
- **SQLite specifics**: single connection (`SetMaxOpenConns(1)`), WAL, `busy_timeout=5000`, foreign
  keys ON — set in `store.Open`. Unique-constraint violations are detected by error-string match in
  `isUniqueViolation` and surfaced as `store.ErrDuplicate`.

## Conventions

- Adding a page: create `web/templates/pages/<name>.html` defining `title` and `content` (it is
  auto-discovered; login/register are the only pages using the auth layout), add the handler in the
  matching `handlers_*.go`, register the route in `server.go`. Fragments go in `templates/partials/`
  and are rendered with `s.fragment(w, status, "name", data)`.
- Template helpers live in the `funcMap` in `render.go` (`dict`, `deref`, `formatDatePtr`, `json`,
  `markdown`, `progressPercent`, ...). Prefer adding a helper over pushing logic into templates.
- Structured logging via `log/slog` (JSON in production); never `fmt.Println` in server code. Auth
  events log with IP/email; the request logger adds request IDs and durations.
- Store methods return the typed errors (`ErrNotFound`, `ErrVersionConflict`, `ErrDuplicate`);
  handlers translate via `s.storeError` or bespoke form re-renders. Compare with `errors.Is`.
- Tests: pure packages test against injected `now`; store tests use a temp-file DB per test; handler
  tests (`internal/server/server_test.go`) run the full middleware stack via `httptest` with
  `stubAI` — extend `testApp` rather than hand-rolling requests. AI calls must stay behind the
  `server.AI` interface so they remain stubbable.
- YAML/SM-2 behavior changes must round-trip against the Svelte app: its exports must import here
  and vice versa (see `TestImportSvelteExportFixture` and `TestExportGolden`).

## Deployment notes

Compose joins an **external** network (`SHARED_NETWORK_NAME`, default `cadence-cards-shared`) with
alias `cadence-cards`, so the existing external nginx (set up for the Svelte app) proxies to
`cadence-cards:3000` unchanged.  The SQLite file lives in the `cadence_data` volume
(`/data/cadence.db`); back it up with `docker compose exec -T app cadence -backup -`, which snapshots
via `VACUUM INTO` (WAL-safe against the live server, and deliberately non-migrating — `exec` runs the
*image's* binary, which can be newer than the running one). `TZ` should be set to the users' timezone for correct
SM-2 due dates. There is no `AUTH_SECRET` — sessions are random server-side tokens (SHA-256-hashed
at rest in the `sessions` table).
