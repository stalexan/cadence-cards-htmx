# Architecture Guide

A tour of how Cadence Cards fits together, for a developer who's read the README and needs to
start making changes. It walks top-down: what the app is, how it starts up, how a request flows
through it, then a full trace of one representative feature (studying a card) to make the pieces
concrete — followed by the domain model, a package map, and how the whole thing is tested.

For "how do I..." conventions (adding a page, error handling, test patterns) see the root
[`CLAUDE.md`](../CLAUDE.md) — this doc explains *why* the app is shaped the way it is; `CLAUDE.md`
is the terse reference for *how* to work within that shape. [`VERSIONING.md`](VERSIONING.md)
covers release numbering, which isn't touched on here.

## What this is

An SM-2 spaced-repetition flashcard app where Claude quizzes you about each card instead of just
showing the answer. It's a from-scratch Go + HTMX rewrite of a sibling SvelteKit app
(`cadence-cards-svelte`), built to ship as **one binary and one SQLite file** — zero NPM
dependencies, no frontend build step, one container to run.

## The shape: server-rendered HTML, not a JSON API

The single biggest thing to internalize: **there is no client-side app**. A conventional SPA ships
JSON and lets JavaScript build HTML in the browser; this app inverts that. The Go server always
renders HTML — either a full page or a fragment of one — and [HTMX](https://htmx.org) just fetches
fragments and swaps them into the DOM. There's no client-side templating, no JSON API, nothing to
bundle.

Concretely, every handler produces one of two things:

- a **full page** (`s.render`) — the app shell (sidebar, layout) plus page content, for a direct
  navigation or a refresh
- a **fragment** (`s.fragment`) — a single named `{{define}}` block, for an HTMX-driven partial
  update

The same store/business-logic calls feed both, so a handler doesn't need to know or care whether
it's being hit by a browser navigation or an HTMX request — it just decides which template to
execute. That's why handlers stay thin: parse form → call store/AI → render.

### How HTMX turns HTML into an app

Interactive elements declare their behavior as `hx-*` attributes instead of JS event handlers:

```html
<button hx-get="/study/1/next?deckIds=2&completed=3" hx-target="#study-area" hx-swap="innerHTML">
  Next Card
</button>
```

That single attribute set *is* the entire client-side wiring for "next card" — no JS click handler
exists anywhere for it. A few HTMX mechanisms recur throughout the app and are worth knowing by
name:

- **`hx-trigger="load"`** — fires a request as soon as an element lands in the DOM. This is how
  the study page auto-generates its Claude question right after a card fragment swaps in.
- **Out-of-band swaps** (`hx-swap-oob`) — one response updates *two* unrelated parts of the page.
  Fetching the next card returns the card itself *and* a replacement progress bar that lives
  outside the swap target, in one payload (`study_card.html`).
- **`HX-Redirect` header** — when an HTMX request's session has expired, a normal HTTP redirect
  would just get swapped into a small fragment target as raw HTML. Instead the handler sets
  `HX-Redirect: /login`, telling HTMX to do a full browser navigation.
- **Selective error swapping** — HTMX normally ignores error responses, but `base.html`'s
  `htmx-config` meta tag whitelists specific statuses: **409** (optimistic-locking conflicts) and
  **422** (validation errors) swap; other 4xx/5xx deliberately don't. That's what makes the
  conflict flow work (see below) — the "error" response is itself useful UI — but it also means an
  error fragment returned with any *other* status is silently discarded, so error-bearing
  fragments must use one of the whitelisted codes.
- **`HX-Push-Url` response header** — fragment handlers, not `hx-push-url` attributes, own the
  address bar. When a fragment response should change what a refresh or bookmark restores (card
  filters, deck search, study progress), the handler sets `HX-Push-Url`
  (`handlers_cards.go`, `handlers_decks.go`, and study's `SessionURL`). Per-element attributes
  would go stale as state threads through swaps; the server always knows the canonical URL.

### Where state lives, given there's no client-side app

Anything that needs to survive a swap or a page refresh has to live somewhere durable, since
there's no in-memory client state:

- **Study session progress** (selected decks, filters, how many cards completed) lives in the
  **URL query string**, re-threaded through every fragment request. The server is stateless per
  request — and because Next/Skip push the updated session URL via `HX-Push-Url`, refreshing or
  hitting back mid-session loses nothing.
- **Chat history** lives in a **hidden form input** in the DOM, round-tripped on every chat POST.
- **Everything else** (cards, decks, schedules, users) lives in SQLite, queried fresh each request.

### Why this shape fits the project's constraints

The "zero NPM dependencies" and strict CSP (no inline scripts, no `unsafe-eval`) goals fall out
naturally from this model: there's no JS bundle to build because there's no client-side app to
bundle. The handful of things plain HTML+HTMX can't express — auto-resizing a textarea, toggling
an answer panel, setting a computed progress-bar width without an inline `style=` — are handled by
a small `app.js` using delegated `data-*` listeners, re-initialized after every swap via
`htmx:afterSwap`. That's the escape hatch, not the primary mechanism.

## Startup and lifecycle

`cmd/cadence/main.go` is wiring and nothing else: parse flags → configure the JSON `slog` handler →
`config.Load` → `store.Open` (which runs migrations) → `server.New` → serve → graceful shutdown on
SIGINT/SIGTERM. Two details are deliberate rather than incidental:

- **The CLI flags short-circuit at different depths.** `-version` returns before the logger,
  `config.Load` and `store.Open`, so asking a binary its version prints one bare line, works with
  a broken environment, and never migrates a database. `-backup` returns after `config.Load` (it
  needs `DB_PATH`) but still before `store.Open`, because a snapshot must not migrate either.
  `-create-user` is the one that does open the store.
- **An hourly maintenance ticker** deletes expired sessions and stale rate-limiter entries. It is
  the only background goroutine in the app; a cancelled context stops it during shutdown.

The release number comes from `version.go` in the repo root, which `go:embed`s the root `VERSION`
file as `cadence.Version` — a root package is required because `go:embed` can't reach outside its
own directory. Nothing at runtime can override it. See [`VERSIONING.md`](VERSIONING.md).

## Request flow

```
browser/htmx → middleware chain → handler → store → SQLite
                                      ↓
                                  template → HTML response
```

**Middleware** (`internal/server/middleware.go`), in the order a request meets them — outermost
first: `logRequests` (structured `slog` line per request, with a request ID and duration) →
`resolveClientIP` (trusts only the nginx-set `X-Real-IP`) → `rateLimit` → `securityHeaders` (CSP,
`X-Frame-Options`, etc.) → `loadSession` (attaches the user to the request context if the session
cookie is valid) → `checkOrigin` (CSRF defense via `Sec-Fetch-Site`/`Origin`) → the mux.

Note that `server.go` *builds* the chain in the reverse of that list — each line wraps the previous
one, so the last line wrapped is the first one entered. The order is load-bearing in at least one
place: `resolveClientIP` has to sit outside `rateLimit`, or the limiter would bucket every request
behind the proxy under the same socket address.

Authenticated routes additionally wrap in `s.requireUser`, which redirects browsers to `/login` but
sends HTMX requests an `HX-Redirect` instead.

**Sessions** are opaque server-side tokens, not signed cookies — there's no `AUTH_SECRET` and
nothing is encoded in the cookie value. Logging in mints 32 random bytes, stores only their SHA-256
in the `sessions` table, and hands the raw token back as an `HttpOnly`, `SameSite=Lax`, 30-day
cookie (`internal/store/sessions.go`, `internal/server/session.go`). Revocation is therefore just a
`DELETE` — which is what lets a password change invalidate every *other* session while keeping the
current one alive, something a stateless signed token can't do; the hourly maintenance ticker
sweeps expired rows. `SameSite=Lax` is also the first half of the CSRF defense — the `checkOrigin`
middleware is the second.

**Routing** (`internal/server/server.go`) is a plain `http.ServeMux` using Go 1.22+ method+pattern
syntax (`"GET /decks/{id}"`) — no router library. Handlers are grouped one file per resource:
`handlers_topics.go`, `handlers_decks.go`, `handlers_cards.go`, `handlers_study.go`,
`handlers_chat.go`, etc.

**Templates** (`internal/server/render.go`) are parsed once at startup: one `*template.Template`
per *page* (layout + every partial + that page's own file — so any partial is reachable from any
page's HTMX swaps), plus a partials-only set for fragment rendering. `s.render` executes a full
page; `s.fragment` executes one named partial.

**The store** (`internal/store/`) is the only package that touches SQL, one file per aggregate
(`cards.go`, `decks.go`, `schedules.go`, ...). Two invariants apply almost everywhere in this
package:

- **Authorization happens inside the query itself.** Every read and write scopes rows via joins up
  to `topics.user_id` in the same statement or transaction as the operation — e.g.
  `GetCard`'s `WHERE c.id = ? AND t.user_id = ?`. Handlers never check ownership separately;
  missing-or-not-owned collapse to one error, `store.ErrNotFound`.
- **Optimistic locking** on `cards.version` and `schedules.version`: mutating writes are
  `UPDATE ... WHERE id = ? AND version = ?`, and zero rows affected means someone else won the
  race — surfaced as `store.ErrVersionConflict`, which handlers map to HTTP 409.

## A feature walked end to end: studying a card

This traces one full click — grading a card and advancing to the next — because it exercises
nearly every mechanism described above.

**1. The grade buttons post to the store.** `grade_area.html`'s form does
`hx-post="/study/schedules/{id}/grade"`, carrying the schedule's `version` and the whole study
session state as hidden inputs.

**2. `handleStudyGrade` picks a store method.** A first-time grade calls
`store.RecordReview`; changing a grade on an already-graded card (the buttons stay live and
re-clickable) calls `store.RegradeReview` instead.

**3. `RecordReview` runs one transaction:** re-fetch the schedule (ownership-scoped), compare
`version`, snapshot the pre-review state into `prev_*` columns, run the pure SM-2 function
(`internal/sm2.CalculateNextInterval`) to get the new easiness/interval/rep-count, then
`UPDATE ... WHERE id = ? AND version = ?`. `RegradeReview` reapplies SM-2 to that `prev_*`
snapshot instead of the current state — so changing your mind is "undo, then redo," not
"compound a second review on the first."

**4. The response is success or a 409.** Success re-renders the `grade_area` fragment in place,
now showing the new interval and the chosen grade highlighted. A version conflict renders
`grade_conflict` at HTTP 409 — an out-of-band chat notice plus a self-refreshing element
(`hx-trigger="load delay:1200ms"`) that automatically re-fetches a fresh card. This only works
because `base.html`'s `htmx-config` whitelists 409 for swapping.

**5. Clicking "Next Card" repeats the pattern.** `hx-get="/study/{topicId}/next?..."` re-encodes
the entire session state (deck IDs, priority, limit, completed count) in the URL.
`handleStudyNext` calls `store.NextDue`, which walks priority tiers A → B → C and picks randomly
within the first non-empty tier — unless the session pinned a single `priority`, in which case that
tier is the only one queried. The response fragment (`study_card`) does three things in one
payload: an out-of-band progress-bar update, the new card's content and grade buttons, and a
pending chat bubble with `hx-trigger="load"` that immediately fires the request to generate
Claude's question for the new card.

Reading `internal/server/handlers_study.go` end to end alongside `web/templates/partials/study_card.html`
and `grade_area.html` is the fastest way to see the full pattern in one sitting.

## Domain model

```
users → topics → decks → cards → schedules
```

All cascade-deleting downward (`ON DELETE CASCADE`). A `topic` is both a folder of decks *and* the
context fed into Claude prompts — it carries six prompt-config columns (`topic_description`,
`expertise`, `focus`, `context_type`, `example`, `question`; the topic's `name` joins them as the
seventh field of `claude.TopicConfig`, see `internal/claude/prompts.go`). A `card` has one `schedule` row **per study
direction** (`UNIQUE(card_id, is_reversed)`); bidirectional decks track forward and reverse
knowledge independently, since knowing "hola → hello" doesn't imply knowing "hello → hola." SM-2
state (easiness, interval, rep count, last grade) lives entirely on the schedule, not the card.

**Due-ness is a calendar question, not an elapsed-time one.** `sm2.IsDue` compares *local-midnight
day boundaries* (`time.Local`), so a card due "in 3 days" becomes due at the start of that day,
not 72 hours after the review — which is what a user studying at inconsistent hours expects. The
consequence is that the process timezone is load-bearing: the deployed container must set `TZ` to
the users' timezone or cards surface on the wrong day. (One deliberate divergence from the JS
source: `DaysBetween` *rounds* instead of floors, so a 23-hour "day" after DST spring-forward
still counts as one day — the Svelte app shares the flooring bug.) It's also why `now` is an injected parameter
throughout `internal/sm2` rather than a call to `time.Now` — the day-boundary logic has to be
testable without waiting for midnight.

Schema lives in `internal/store/migrations/` as forward-only numbered `.sql` files — there's no
separate `schema.sql` to read; the migrations *are* the schema.

## Package map

| Package | Owns |
|---|---|
| `cmd/cadence` | Entrypoint: wiring, the maintenance ticker, the CLI flags |
| `version.go` (root) | Embeds `VERSION` as `cadence.Version`; stdlib-only, so `internal/` can import it |
| `internal/server` | HTTP: routing, middleware, sessions, templates, handlers |
| `internal/store` | All SQL; typed errors; optimistic locking; authorization scoping |
| `internal/sm2` | The pure SM-2 algorithm — no DB, no clock (time is always an injected param) |
| `internal/claude` | Anthropic SDK client, prompt building, the three study operations |
| `internal/markdown` | Renders Claude's replies to HTML (escaped — this is the XSS boundary) |
| `internal/yamlio` | Deck import/export, byte-compatible with the sibling Svelte app's format |
| `internal/config` | Env var parsing (`DB_PATH`, `CLAUDE_*`, `.env` loading) |
| `internal/ratelimit` | In-memory per-IP/per-account rate limiting |
| `web/` | Templates, static assets — embedded into the binary via `go:embed` |

## Go types worth knowing

Go doesn't have classes; these are the structs that carry real behavior, not just per-handler view
models:

- **`store.Store`** — wraps `*sql.DB`; every domain method hangs off it.
- **`server.Server`** — wraps `Store`, the `AI` interface, the rate limiter, and parsed templates;
  every HTTP handler is a method on it.
- **`claude.Client`** — wraps the Anthropic SDK; implements the `server.AI` interface, which is
  what lets handler tests substitute a stub instead of calling the real API. Prompts carry
  `cache_control` breakpoints at their stable-prefix boundaries (topic instructions, card block,
  conversation history), so repeated requests in a study session hit Anthropic's prompt cache —
  the `claude request` log line's `cacheReadInputTokens` shows when they do. Question generation
  can run on a cheaper model/effort than the tutoring chat (`CLAUDE_QUESTION_MODEL` /
  `CLAUDE_QUESTION_EFFORT`), and API failures are classified into typed sentinels
  (`ErrRateLimited`, `ErrOverloaded`, `ErrBadAuth`, `ErrRefused`) that handlers map to distinct
  error-bubble copy — studying and grading never depend on AI availability.
- **`sm2.State`** — pure data (no methods beyond `IsDue`/`NextDueDate`); passed by value through
  the pure `CalculateNextInterval` function.

Domain models mirroring the DB tables (`store.User`, `Topic`, `Deck`, `Card`, `Schedule`) live in
`internal/store/models.go`.

## How it's tested

Three tiers, matching the three kinds of code above:

- **Pure packages** (`sm2`, `yamlio`, `markdown`) test against an injected `now` and fixed inputs.
  `yamlio`'s golden tests pin the export format byte-for-byte against the Svelte app's output — if
  one fails, the code is wrong, not the fixture, unless the format change is deliberate on both
  apps.
- **The store** gets a real SQLite file in a `t.TempDir()` per test — migrations and all. There's no
  mocking layer and no interface in front of `*store.Store`; it's fast enough not to need one.
- **Handlers** run the *entire* middleware chain through `httptest`, driven by the `newTestApp`
  helper in `internal/server/server_test.go` — which is why its `do` method sets `X-Real-IP` and
  `Sec-Fetch-Site` headers, without which `resolveClientIP` and `checkOrigin` would reject the
  request. Extend `testApp` rather than hand-rolling requests.

This is the architectural reason `AI` is an interface (`server.go:18`) rather than a concrete
`*claude.Client`: it's the one dependency that would otherwise make a handler test hit the network,
so it's the one dependency that gets inverted. `stubAI` fills it in tests. Keep new AI calls behind
that interface or the handler tests stop being runnable offline.

## Dependencies

Small on purpose. The load-bearing ones:

- **`modernc.org/sqlite`** — pure-Go SQLite driver (no cgo)
- **`github.com/anthropics/anthropic-sdk-go`** — the Claude API client
- **`github.com/yuin/goldmark`** — Markdown rendering for chat replies
- **`go.yaml.in/yaml/v4`** — deck import/export
- **`golang.org/x/crypto`** — password hashing

Everything else in `go.mod` is a transitive dependency of one of those — mostly of the Anthropic
SDK and of `modernc.org/sqlite`, whose pure-Go C runtime (`modernc.org/libc` and friends) accounts
for much of the list. Routing, templating,
JSON, and the HTTP server itself are all stdlib — the "zero dependencies" philosophy applies to
the Go side, not just the frontend.

## Where to go next

- [`CLAUDE.md`](../CLAUDE.md) — conventions, commands, load-bearing invariants (optimistic
  locking, CSP, client-IP trust) in reference form.
- [`VERSIONING.md`](VERSIONING.md) — how the release number is embedded and bumped.
- `internal/server/handlers_study.go` + `web/templates/partials/study_card.html`,
  `grade_area.html` — the fullest example of the request/fragment/OOB-swap pattern in one place.
- `internal/sm2/sm2.go` — short, pure, and worth reading end to end; it's the algorithm the whole
  app exists to schedule around.
- The sibling `cadence-cards-svelte` repo — the feature-parity reference. Never edit it from here,
  but it's the source of truth when a port's intended behavior is unclear.

## External references

Outside this repo, these map directly onto patterns used throughout the codebase:

- **[HTMX docs](https://htmx.org/docs/)** — the entire client-side interaction model. The
  [attributes reference](https://htmx.org/reference/) and the sections on
  [out-of-band swaps](https://htmx.org/docs/#oob_swaps) and
  [response headers](https://htmx.org/docs/#response-headers) (`HX-Redirect`, etc.) map straight
  onto patterns in `grade_area.html` and `handleStudyGrade`.
- **[Go 1.22 `net/http` routing enhancements](https://go.dev/blog/routing-enhancements)** —
  explains the `"GET /decks/{id}"` method+pattern syntax `server.go` uses instead of a router
  library.
- **[`html/template` docs](https://pkg.go.dev/html/template)** — the contextual auto-escaping this
  app relies on for XSS safety, plus how `{{define}}`/`{{template}}` composition works (relevant
  to `render.go`'s one-template-set-per-page approach).
- **[SQLite WAL mode](https://www.sqlite.org/wal.html)** — explains why `store.Open` sets
  `SetMaxOpenConns(1)` and `busy_timeout=5000`; worth reading before touching anything
  transaction-related in `internal/store`.
- **[The original SM-2 paper/description](https://www.supermemo.com/en/blog/application-of-a-computer-to-improve-the-results-obtained-in-working-with-the-supermemo-method)**
  — also linked in `internal/sm2/sm2.go`'s header comment; the algorithm is small, but the *why*
  behind the easiness/interval formula is easier to absorb from the source than from the Go port.
- **[Anthropic API docs](https://docs.claude.com/)** — for `internal/claude`, particularly prompt
  construction and the Messages API shape the SDK wraps.
- **[MDN: Content-Security-Policy](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Security-Policy)**
  — context for why `securityHeaders` is written the way it is (no `unsafe-inline`/`unsafe-eval`)
  and what breaks if a future change tries to add an inline `<script>` or `style=`.
