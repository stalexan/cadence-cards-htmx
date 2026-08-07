# Cadence Cards (Go + HTMX)

A flashcard app with SM-2 spaced repetition and Claude-powered study assistance. This is
a from-scratch rewrite of `cadence-cards-svelte` with:

- **Go** backend (stdlib `net/http`, Go 1.22+ route patterns)
- **HTMX** frontend (vendored `htmx.min.js`, ~250 lines of vanilla JS, hand-written CSS)
- **SQLite** (pure-Go driver `modernc.org/sqlite` — no CGo, single data file)
- **Zero NPM dependencies** and no frontend build step
- **One container**, based on `ubuntu:resolute`

## Features

- Topics → decks → cards, with per-direction SM-2 schedules (bidirectional decks study both
  front→back and back→front)
- Study sessions: Claude generates a practice question per card, you answer in a chat, reveal the
  answer, and grade yourself (SM-2 scheduling with optimistic locking — concurrent grading gets
  a 409 and auto-refreshes)
- Free-form topic chat with Claude (topic-configurable prompts)
- YAML deck export/import, byte-compatible with the Svelte app's format (including SM-2 progress and
  reverse-direction parameters)
- Server-side search/filter/sort/pagination on the card list
- Credentials auth with bcrypt, server-side sessions, progressive login lockouts, strict CSP (no
  inline scripts/styles, no `unsafe-eval`)

## Differences from the Svelte app

- The study setup filters actually work: multiple `deckIds` are honored, and `priority`
  / `includeNew` / `limit` filter the session (all were dropped or ignored upstream).
- Dead code was not ported: the unfinished Claude card-creation flow, `/api/chat/initial`, and the
  duplicate `/api/study/cards` endpoint.
- JSON API endpoints became server-rendered pages/fragments; `/api/health` remains for the reverse
  proxy's health checks.
- Sessions are server-side (revocable) instead of JWTs — `AUTH_SECRET` is gone.
- Card content for Claude prompts is resolved server-side by schedule ID; the browser never supplies
  prompt text.

## Development

```bash
go test ./...                                     # all unit + handler tests
DB_PATH=./dev.db COOKIE_SECURE=false ENABLE_PUBLIC_REGISTRATION=true go run ./cmd/cadence
# → http://localhost:3000
```

The binary reads `.env` from the working directory at startup, so once you have copied
`.env.example` and uncommented `DB_PATH`, `COOKIE_SECURE`, and `ENABLE_PUBLIC_REGISTRATION` there,
a bare `go run ./cmd/cadence` is enough. Variables already set in the environment always win over
the file, so the inline form above still works and remains handy for one-off overrides
(`CLAUDE_MAX_TOKENS=99 go run ./cmd/cadence`).

`CLAUDE_API_KEY` is only needed for the study-question/chat features; without it the UI degrades to
error bubbles. The startup log line reports `claudeConfigured` so you can tell at a glance whether
the key actually reached the process.

## Configuration

All configuration is environment variables. `.env.example` is the template; copy it to `.env`, which
both `docker compose` and the binary itself read — compose for `${VAR}` substitution, the app via
`config.Load` at startup.

Precedence is one rule: **a variable already present in the environment is never overwritten by
`.env`.** So compose-supplied values always win in the container, and an inline
`VAR=x go run ./cmd/cadence` always wins locally. `.env` is listed in `.dockerignore`, so the image
never contains one and the file is effectively a local-development convenience. A missing `.env` is
not an error; a malformed line fails startup with the file and line number.

| Variable | Default | Read by | Notes |
| --- | --- | --- | --- |
| `PORT` | `3000` | app | Compose hardcodes `3000`; effectively local-dev only. |
| `DB_PATH` | `/data/cadence.db` | app | Compose hardcodes the volume path. Set `./dev.db` locally. |
| `CLAUDE_API_KEY` | *(empty)* | app | Study-question generation and chat. Unset ⇒ those features return error bubbles; the rest of the app works. |
| `CLAUDE_MODEL` | `claude-opus-4-8` | app | |
| `CLAUDE_MAX_TOKENS` | `1000` | app | Compose passes `4000` unless overridden. Must be ≥ 1. |
| `ENABLE_PUBLIC_REGISTRATION` | `false` | app | When off, create users with `-create-user`. |
| `COOKIE_SECURE` | `true` | app | Set `false` only when serving plain HTTP. **Not forwarded by compose** — local dev only. |
| `DISABLE_RATE_LIMITING` | `false` | app | Dev only. **Not forwarded by compose.** |
| `APP_VERSION` | *(empty)* | app | Informational; shown in the sidebar footer. |
| `TZ` | `UTC` | container, app | SM-2 due dates use local-midnight day boundaries, so this decides when a card becomes due. Set it to your users' timezone. Also honoured from `.env` locally: `config.Load` runs before anything resolves `time.Local`, so a `TZ` set there still applies. |
| `SHARED_NETWORK_NAME` | `cadence-cards-shared` | compose | External network the reverse proxy lives on. Not seen by the app. |

Booleans are parsed strictly: only the literal string `true` enables them, anything else is false.
`PORT` and `CLAUDE_MAX_TOKENS` fail startup with an error if they are set but unparseable.

The four "rarely needed" variables in `.env.example` (`PORT`, `DB_PATH`, `COOKIE_SECURE`,
`DISABLE_RATE_LIMITING`) are not passed through by [`docker-compose.yml`](docker-compose.yml) —
uncommenting them in `.env` affects a local `go run` but has no effect on the container. Add them to
the `environment:` block if you need them there.

`.env` parsing is deliberately minimal: `KEY=VALUE`, blank lines and `#` comment lines skipped, an
optional `export ` prefix stripped, and the split on the first `=` only. Values may be wrapped in
single or double quotes (escapes are interpreted inside double quotes). There is **no inline-comment
stripping** — an unquoted value runs to end of line, so `NUM=4000 # note` yields the literal
`4000 # note` and a startup error rather than silently truncating. Quote any value containing `#`.

## Deployment

```bash
cp .env.example .env          # fill in CLAUDE_API_KEY etc.
docker network create cadence-cards-shared   # once, if it doesn't exist
docker compose up -d --build
```

The container joins the external `cadence-cards-shared` network with the alias `cadence-cards`, so
an nginx proxying to `cadence-cards:3000` (as set up for the Svelte app) keeps working. The SQLite
database lives in the `cadence_data` volume at `/data/cadence.db`.

First user (with public registration disabled):

```bash
docker compose run --rm app -create-user you@example.com
```

Backups are a file copy:

```bash
docker compose cp app:/data/cadence.db backup-$(date +%F).db
```

### Migrating from the Svelte app

No database migration is needed: export each deck from the Svelte app (Share → include SM-2 params)
and paste the YAML into **Import** here. Study progress, tags, and reverse-direction schedules
survive the round trip. If the YAML carries reverse SM-2 params (`ReverseLastSeen`, `ReverseGrade`,
…), the target deck is switched to bidirectional so that progress is actually studied — its existing
cards get a reverse schedule at the initial state.

## Layout

```
cmd/cadence/          entrypoint (+ -create-user CLI)
internal/config/      env configuration
internal/sm2/         SM-2 algorithm (pure port of sm2.ts, with its tests)
internal/yamlio/      YAML export/import (byte-compatible format)
internal/store/       all SQLite access; migrations in store/migrations/
internal/ratelimit/   in-memory request + login-failure limiting
internal/markdown/    goldmark wrapper (raw HTML escaped)
internal/claude/      anthropic-sdk-go client + prompt building
internal/server/      handlers, middleware, template rendering
web/templates/        html/template layouts, pages, HTMX fragments
web/static/           htmx.min.js (vendored), app.css, app.js, favicon
```
