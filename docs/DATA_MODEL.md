# Data Model

Every table in the database, what it holds, and how it relates to the others — followed by how
SQLite itself is used (pragmas, transactions, migrations, backups) and the query patterns worth
knowing before you write SQL here.

This is the reference companion to [`ARCHITECTURE.md`](ARCHITECTURE.md), which covers the same
domain at a paragraph's depth as part of the wider tour. Read that first if you want the shape of
the app; read this when you need the columns.

**There is no `schema.sql`.** The schema *is* the forward-only migration sequence in
`internal/store/migrations/`, applied in filename order. This document describes the state after
all of them; when it disagrees with the `.sql` files, the `.sql` files are right.

## The graph

```
users ─┬─< sessions
       │
       └─< topics ─┬─< decks ──< cards ──< schedules ─┬─< generated_questions (0..1)
                   │                                  │
                   └─< conversations >────────────────┘ (nullable)
                            │
                            └─< chat_messages
```

Every arrow is `ON DELETE CASCADE`. Deleting a user erases everything they own; deleting a topic
takes its decks, cards, schedules and conversations with it. There are no soft deletes and no
orphan-cleanup jobs — cascade does all of it, which is why `foreign_keys(ON)` is set on every
connection rather than left at SQLite's off-by-default.

The spine is **`users → topics → decks → cards → schedules`**. Everything else hangs off it:
`sessions` off users, `conversations`/`chat_messages` off topics, `generated_questions` off
schedules.

## Column conventions

These hold across every table, so they're stated once here rather than repeated per column.

| Convention | How it looks | Why |
|---|---|---|
| Surrogate keys | `id INTEGER PRIMARY KEY AUTOINCREMENT` | Alias for SQLite's `rowid`. `AUTOINCREMENT` prevents id reuse after deletes, so a stale URL can never silently address a different row. It costs an internal `sqlite_sequence` table. |
| Timestamps | `TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))` | RFC3339 UTC with millisecond precision, mirroring the source app's Prisma `TIMESTAMP(3)`. `store.timeFormat` is the Go side of the same format; `fmtTime`/`parseTime` are the only converters. |
| Booleans | `INTEGER NOT NULL DEFAULT 0 CHECK (x IN (0,1))` | SQLite has no boolean type; the `CHECK` is what makes it one. Go converts at the scan boundary (`rev == 1`, `boolInt(b)`). |
| Enums | `TEXT CHECK (col IN ('A','B','C'))` | Ports Postgres enums. The Go-side counterparts are `sm2.Priority` and `sm2.Grade`. |
| Lists | JSON text with `CHECK (json_valid(...))` | Only `cards.tags`. Queried with `json_each`, not `LIKE`. |
| Optimistic-lock counters | `version INTEGER NOT NULL DEFAULT 0` | On `cards` and `schedules` only. See [Optimistic locking](#optimistic-locking). |

`updated_at` is **not** maintained by triggers — every `UPDATE` sets it explicitly with the same
`strftime` expression the column default uses. If you write a new update statement, carry that
clause along or the row's timestamp silently freezes.

## Tables

### `users`

An account. Deliberately minimal: no roles, no email verification, no OAuth identities.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `name` | TEXT | Nullable; display name only. |
| `email` | TEXT UNIQUE | Nullable in the schema (a port artifact of the source app's Auth.js user table), but every code path that creates a user sets it — it's the login identifier. |
| `password_hash` | TEXT | bcrypt (`golang.org/x/crypto/bcrypt`). Never leaves `internal/store`/`internal/server` auth paths. |
| `created_at`, `updated_at` | TEXT | |

Accounts are created by public registration (when `ENABLE_PUBLIC_REGISTRATION=true`) or the
`-create-user` CLI. `ErrDuplicate` on the `email` unique constraint is what surfaces "that email is
taken".

### `sessions`

Server-side login sessions. **The primary key is the SHA-256 of the cookie token, not the token** —
a database dump can't be replayed as a set of live logins.

| Column | Type | Notes |
|---|---|---|
| `token_hash` | TEXT PK | `hex(sha256(raw token))`. The raw 32-byte token exists only in the user's cookie. |
| `user_id` | INTEGER FK → `users(id)` CASCADE | |
| `created_at` | TEXT | |
| `expires_at` | TEXT | `created_at + store.SessionDuration` (30 days). |

Indexes: `idx_sessions_user_id` (bulk logout on password change), `idx_sessions_expires_at` (the
hourly sweep).

Lifecycle: minted on login; deleted on logout (one row), on password change (all *other* rows), on
CLI password reset (all rows), and on expiry by the hourly maintenance ticker
(`DeleteExpiredSessions`). Expiry is also checked at read time, so a not-yet-swept row can't
authenticate anything.

### `topics`

A folder of decks **and** the Claude prompt configuration. The dual role is the reason topics carry
seven text fields that have nothing to do with foldering.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `user_id` | INTEGER FK → `users(id)` CASCADE | **The ownership root.** Every authorization check in the app terminates here. |
| `name` | TEXT NOT NULL | Also the first field of `claude.TopicConfig`. |
| `topic_description`, `expertise`, `focus`, `context_type`, `example`, `question` | TEXT | The other six prompt-config fields, consumed by `internal/claude/prompts.go`. All nullable; blanks fall back to `claude.PromptDefaults`, which the topic form also shows as input placeholders. |
| `created_at`, `updated_at` | TEXT | |

Constraints: `UNIQUE (name, user_id)` — topic names are unique per user, not globally. Index:
`idx_topics_user_id` (the unique index is `(name, user_id)`, so `user_id` isn't a usable prefix of
it — this index is doing real work).

### `decks`

A group of cards within a topic, plus the field labels and study-direction setting.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `topic_id` | INTEGER FK → `topics(id)` CASCADE | |
| `name` | TEXT NOT NULL | |
| `field1_label`, `field2_label` | TEXT | Nullable; `Deck.FieldLabels()` supplies the `"Front"`/`"Back"` fallbacks. Labels are cosmetic — they never change which column is `front`. |
| `is_bidirectional` | INTEGER 0/1 | Whether the deck is studied in both directions. |
| `created_at`, `updated_at` | TEXT | |

Constraints: `UNIQUE (name, topic_id)`. Index: `idx_decks_topic_id`.

**`is_bidirectional` is a gate, not a source of truth about which schedules exist.** Enabling it
back-fills reverse schedules for every existing card (`UpdateDeck`); disabling it leaves those rows
in place, dormant — `dueSchedules` and `DashboardStats` skip reverse schedules whose deck is
unidirectional. So re-enabling restores the old reverse progress instead of resetting it. YAML
import can flip the flag on by itself: a file carrying reverse SM-2 params is a statement that the
deck is studied both ways, and importing it into a unidirectional deck would otherwise hide every
card just imported.

### `cards`

The flashcard content. Note what is *not* here: no SM-2 state, no due date. Those live on
`schedules`.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `deck_id` | INTEGER FK → `decks(id)` CASCADE | Mutable — a card can be moved between decks, which is ownership-checked against the *target* deck in the same transaction. |
| `front`, `back` | TEXT NOT NULL | **Markdown source**, rendered by `internal/markdown` wherever read. |
| `note` | TEXT | Markdown too; shown alongside the answer and fed to Claude. |
| `priority` | TEXT CHECK `('A','B','C')` | Study bands: A is drained before B before C. |
| `tags` | TEXT NOT NULL DEFAULT `'[]'` CHECK `json_valid` | JSON array of strings. |
| `version` | INTEGER | Optimistic lock. |
| `created_at`, `updated_at` | TEXT | |

Index: `idx_cards_deck_id`.

Two consequences of storing markdown rather than HTML: card **search** does `LIKE` against the raw
source, so a phrase straddling markup (`the **quick** brown`) won't match; and table cells /
dashboard rows run the source through `markdown.PlainText` (an AST walk, not a regex) so layout
isn't broken by block elements.

### `schedules`

One row per **study direction** of a card, holding all SM-2 state. This is the table the study loop
actually reads and writes.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | The study UI's handle on "the current card" — the URL carries `scheduleId`, never card content. |
| `card_id` | INTEGER FK → `cards(id)` CASCADE | |
| `is_reversed` | INTEGER 0/1 | 0 = front→back, 1 = back→front. |
| `easiness` | REAL DEFAULT 2.5 | Kept to two decimals by `sm2.RoundEasiness`; floor 1.3, no upper cap. |
| `interval` | INTEGER DEFAULT 1 | Days until next due. |
| `rep_count` | INTEGER DEFAULT 0 | Consecutive successful reviews. |
| `grade` | TEXT CHECK, nullable | `INCORRECT` \| `CORRECT_WITH_HESITATION` \| `CORRECT_PERFECT_RECALL`. NULL = never reviewed. |
| `last_seen` | TEXT, nullable | NULL = never reviewed; combined with `interval` this is what due-ness is computed from. |
| `prev_easiness`, `prev_interval`, `prev_rep_count`, `prev_grade`, `prev_last_seen` | nullable | Snapshot of the state the *most recent* review started from. |
| `version` | INTEGER | Optimistic lock. |
| `created_at`, `updated_at` | TEXT | |

Constraints: `UNIQUE (card_id, is_reversed)` — at most two schedules per card. Index:
`idx_schedules_card_id` (redundant with the unique index, whose leading column is `card_id`;
harmless, and left as written).

**There is no `due_at` column.** Due-ness is derived (`last_seen + interval`, compared at
*local-midnight* day boundaries by `sm2.IsDue`) and evaluated **in Go, not in SQL** — see
[Query patterns](#query-patterns-and-their-costs).

**The `prev_*` snapshot** is what makes "change my grade" work while the card is still on screen.
`RecordReview` stashes the pre-review state; `RegradeReview` re-derives from that baseline instead
of compounding a second review onto the first, and deliberately leaves the snapshot in place so
successive corrections all start from the same point. `prev_easiness IS NOT NULL` is the "a
baseline exists" marker — the other four are legitimately NULL or zero after a first-ever review.
`ResetProgress` clears the snapshot along with the state (nothing left to rewind); YAML import
clears it too.

### `conversations` and `chat_messages`

Server-owned chat transcripts. The browser holds only a conversation ID, so history can't be forged
client-side and request bodies don't grow with turn count.

`conversations`:

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `topic_id` | INTEGER FK → `topics(id)` CASCADE | **No `user_id` column** — ownership resolves through the topic, per the authorization rule below. |
| `schedule_id` | INTEGER FK → `schedules(id)` CASCADE, nullable | NULL = free-form topic chat; set = study chat about that card direction. |
| `created_at`, `updated_at` | TEXT | `updated_at` is bumped on every append and drives TTL pruning. |

`chat_messages`:

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | Also the ordering key — messages are read `ORDER BY id`, not by timestamp. |
| `conversation_id` | INTEGER FK → `conversations(id)` CASCADE | |
| `role` | TEXT CHECK `('user','assistant')` | |
| `content` | TEXT NOT NULL | Raw text; markdown rendering happens at display time. |
| `created_at` | TEXT | |

Indexes: `idx_conversations_topic_id`, `idx_conversations_schedule_id` (restoring the chat after a
mid-card refresh), `idx_conversations_updated_at` (the TTL sweep),
`idx_chat_messages_conversation_id`.

Retention: conversations untouched for `store.ConversationTTL` (7 days) are deleted by the hourly
ticker; messages cascade. This is the only user data in the app that expires on its own.

### `generated_questions`

Questions pre-generated overnight by `internal/pregen` via the Anthropic Message Batches API, for
cards coming due within 24h.

| Column | Type | Notes |
|---|---|---|
| `schedule_id` | INTEGER **PK**, FK → `schedules(id)` CASCADE | The PK *is* the uniqueness rule: at most one unused question per schedule. |
| `question` | TEXT NOT NULL | |
| `model` | TEXT NOT NULL | Which model produced it — useful when the question model changes. |
| `generated_at` | TEXT | |

This is a cache, not a record: rows are **consumed on serve** (read + delete in one transaction, so
a question is served at most once), **invalidated on card edit** (`UpdateCard` deletes them inside
its own transaction — a question built from old `front`/`back`/`note` must never be shown) and on
Reset Progress, and cascade away with the schedule. A miss is not an error; the handler falls back
to live generation.

### `schema_migrations`

Bookkeeping: `version INTEGER PRIMARY KEY`, `applied_at TEXT`. One row per applied migration file,
where `version` is the file's 1-based position in sorted order. Created by `store.migrate()` itself,
not by a migration.

## Cross-cutting rules

### Authorization is a join, not a column

Only `users`, `sessions` and `topics` reference a user directly. Everything deeper is reached by
joining up to `topics.user_id`:

```sql
FROM schedules s
JOIN cards c  ON c.id = s.card_id
JOIN decks d  ON d.id = c.deck_id
JOIN topics t ON t.id = d.topic_id
WHERE s.id = ? AND t.user_id = ?
```

Three properties follow, and they're load-bearing:

- **Every mutating store method takes `userID`** and applies that scope **inside the same
  transaction as the write** — including via `INSERT ... SELECT` and `UPDATE ... WHERE id IN
  (SELECT ...)`, so a non-owned reference affects zero rows rather than being checked separately
  and then written. Handlers never check ownership themselves.
- **Missing and not-owned are the same error** (`store.ErrNotFound` → HTTP 404), so the app can't
  be used to probe which IDs exist.
- **Denormalizing `user_id` downward would create a second source of truth** for the same
  question. The joins are three levels at worst on tables this size.

### Optimistic locking

`cards.version` and `schedules.version` are compare-and-incremented:

```sql
UPDATE schedules SET ..., version = version + 1 WHERE id = ? AND version = ?
```

Zero rows affected on a row that exists means someone else wrote first → `ErrVersionConflict` →
**HTTP 409**, which the study UI relies on: the `grade_conflict` fragment auto-refetches the card
rather than showing an error. (htmx only swaps error bodies for 409 and 422, so this status is part
of the contract, not just a label.)

Not everything is version-checked. Topics and decks aren't (no concurrent-edit surface worth the
ceremony), and `setScheduleState` isn't, because import creates the card and its schedules in one
transaction — there's no prior version for a caller to have read.

### Where SM-2 numbers are allowed to come from

`easiness` is written through `sm2.RoundEasiness` on **every** path — reviews, regrades, and YAML
import (`setScheduleState`). Import matters because a Svelte-app export carries full float64 drift
(`2.8000000000000003`), and migration `0005_round_easiness.sql` cleaned exactly that out of
existing rows. Two decimals is finer than the smallest increment the formula produces (0.02), so no
due date moves.

## How SQLite is used

### Driver and connection

`modernc.org/sqlite` — a pure-Go, cgo-free SQLite. That's what lets the app cross-compile and ship
as a static-ish binary in a plain `ubuntu` image with no build toolchain.

`store.Open` sets, per connection:

| Setting | Value | Why |
|---|---|---|
| `SetMaxOpenConns(1)` | 1 | The driver is single-writer. At this app's traffic level one connection removes `SQLITE_BUSY` churn entirely, at the cost of serializing reads too. |
| `journal_mode` | WAL | Readers don't block the writer; makes the live-server backup below safe. |
| `foreign_keys` | ON | SQLite defaults to **off** — every cascade in this document depends on this pragma. |
| `busy_timeout` | 5000 ms | |
| `synchronous` | NORMAL | The standard WAL trade-off: durable against process crash, not against OS crash. |

`inTx` is the only transaction helper: begin → run → commit, rollback on any error. Anything doing
more than one statement goes through it.

### Migrations

Embedded `.sql` files in `internal/store/migrations/`, applied by `store.migrate()` in filename
order, each in its own transaction, tracked in `schema_migrations`. **Forward-only** — add
`0006_*.sql`; never edit an applied file, because applied files aren't re-run and every deployed
database would diverge from a fresh one.

Migrations run automatically at startup, so a deploy is just a restart. The exceptions are
deliberate: `-version` is handled before `store.Open` (works with a broken environment, never
migrates) and `OpenForBackup` never migrates, since `docker compose exec` runs the *image's*
binary, which can be newer than the running server's — migrating the live file underneath an older
process would corrupt it.

SQLite's `ALTER TABLE` is limited (no drop/alter column in older versions, no constraint changes).
The 12-step rebuild — create the new table, copy, drop, rename, inside the migration's transaction
— is the fallback when a column needs to change shape.

### SQLite features this app leans on

- **`json_each`** for `cards.tags`: `EXISTS (SELECT 1 FROM json_each(c.tags) je WHERE je.value = ?)`
  filters, and a `JOIN json_each(...)` + `DISTINCT` builds the tag dropdown. Never `LIKE '%tag%'`,
  which would match substrings across tag boundaries.
- **`strftime('%Y-%m-%dT%H:%M:%fZ','now')`** as both column default and explicit `updated_at` value.
- **`COLLATE NOCASE`** on every user-visible name/text ordering, so sorting isn't ASCII-cased.
- **`LIKE ... ESCAPE '\'`** with `escapeLike()` on the Go side, so a user searching for `100%` gets
  a literal match instead of a full scan wildcard.
- **`ON CONFLICT(schedule_id) DO UPDATE`** — upserting a pre-generated question.
- **`VACUUM INTO`** for backups (below).

### Unique violations

Detected by **error-string match** (`strings.Contains(err.Error(), "UNIQUE constraint failed")`) in
`isUniqueViolation`, and surfaced as `store.ErrDuplicate`. The driver doesn't expose a typed
constraint error, so this is the available option; it's contained to one function, and the store
tests exercise the paths that depend on it.

### Backups

`cadence -backup <dest>` (`docker compose exec -T app cadence -backup -` in production) opens the
database in a dedicated mode and runs **`VACUUM INTO`**:

- Safe against a live server — the snapshot is taken inside a read transaction, so under WAL
  concurrent writers are neither blocked nor half-included, and committed transactions still in the
  WAL *are* captured (a plain file copy would lose them).
- Produces one compacted file with no `-wal`/`-shm` sidecars, so restoring is a single move.
- Deliberately no `PRAGMA wal_checkpoint` first: that takes the checkpointer lock and mutates the
  production database for no benefit. The one interaction is that the WAL can't reset while the read
  transaction is open, so it may grow for the duration.
- The finished snapshot is opened read-only and checked with `PRAGMA quick_check`, so a corrupt
  backup fails on the day it's taken rather than the day it's needed.

Three open modes exist for this reason (`store.go`): `modeServer` (read-write, migrating),
`modeBackup` (read-write but never migrating, 30 s busy timeout — nobody is waiting on a backup),
and `modeVerify` (read-only, no `journal_mode` pragma, since setting it writes the file header).

## Query patterns and their costs

**Due-ness is computed in Go, not SQL.** `dueSchedules` selects every candidate schedule for a
topic/priority/deck filter, then filters in Go with `sm2.IsDue`. The reason is the day-boundary
rule: due-ness compares *local-midnight* boundaries (`time.Local`), which SQLite's date functions
can't express without embedding the process timezone into the query — and the Go version is the one
that's unit-tested. Consequences worth knowing:

- The process **`TZ` is load-bearing**. A container with the wrong `TZ` surfaces cards on the wrong
  day.
- Due *counts* are the same walk. `StudyStats` runs it once per priority band; `TopicDueCounts`
  runs it per topic × per band; `DashboardStats` scans every one of the user's schedules. All are
  linear in the user's collection, which is fine at personal-collection scale and is the first
  thing to revisit if it isn't — [`FUTURE_WORK.md`](FUTURE_WORK.md) covers the SQL-due-ness option
  and what would justify it.

**Card search is `LIKE` over raw markdown**, across `front`/`back`/`note`. No FTS5 table, so no
ranking, no stemming, and no match for a phrase interrupted by markup.

**Schedules are loaded separately from cards.** `ListCards` runs the card query, then one
`WHERE card_id IN (...)` for the whole page — one extra query per page, not per card.

**Counts are computed, never stored.** `Topic.DeckCount`/`CardCount` and the dashboard totals are
aggregates at read time; there are no counter columns to drift.

**The only randomness in a query is card selection.** `NextDue` walks priority bands A→B→C and
picks uniformly at random within the first non-empty one, in Go — deliberately not `ORDER BY
RANDOM() LIMIT 1`, since the due filter already happens in Go.

## Changing the schema

1. Add `internal/store/migrations/000N_what_it_does.sql`. Lead with a comment saying *why* — the
   existing files are the style reference.
2. Add or update the SQL in the matching `internal/store/*.go` aggregate file. All SQL lives in
   this package; handlers never write queries.
3. Update the model struct in `models.go` and its scan helper. Scans are positional — a column
   added mid-`SELECT` breaks every scanner using that column list (`scheduleCols`, `cardSelect`).
4. Carry `userID` scoping into any new mutating method, and `version = version + 1` if the table is
   version-checked.
5. Test it: store tests get a real SQLite file per test in `t.TempDir()`, migrations and all, so a
   broken migration fails the suite rather than production.
6. If it changes what YAML export/import writes or reads, round-trip against the sibling Svelte app
   — its exports must import here and vice versa (`TestImportSvelteExportFixture`,
   `TestExportGolden`).

## Where to go next

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — how a request reaches the store in the first place, and
  the study flow traced end to end.
- [`../CLAUDE.md`](../CLAUDE.md) — the same invariants in terse reference form.
- [`FUTURE_WORK.md`](FUTURE_WORK.md) — deferred storage work (SQL due-ness, a reader pool) with the
  triggers that would make each worth building.
- `internal/store/store.go` — pragmas, migration runner, transaction helper, in ~240 lines.
- **[SQLite WAL mode](https://www.sqlite.org/wal.html)** and
  **[the JSON1 functions](https://www.sqlite.org/json1.html)** — the two SQLite features this
  schema depends on most.
