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

`CLAUDE_API_KEY` is only needed for the study-question/chat features; without it the UI degrades to
error bubbles.

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
survive the round trip.

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
