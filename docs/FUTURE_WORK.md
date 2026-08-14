# Future work

Items considered in the August 2026 scalability/correctness review and deliberately deferred or
rejected. Each deferred item notes the trigger that would make it worth building — until that
trigger fires, building it would be premature for a single-user, one-container deployment.

The review's implemented outcomes, for context: prompt caching and per-operation model routing,
typed AI error handling with distinct error bubbles, server-owned chat transcripts, stable cards
across refresh, and nightly batch question pre-generation.

## Deferred

### AI job queue with polling fragments

Move Claude calls out of the HTTP request path: handlers enqueue a job and return a placeholder
fragment that polls `GET /study/{topicId}/job/{jobId}` until the rendered bubble is ready. A
bounded worker pool becomes the single knob controlling API concurrency; jobs need TTL/eviction
and per-user scoping. An in-memory implementation is enough — see "Rejected" for why not Redis.

**Build when:** multiple people study concurrently and requests visibly queue behind each other,
or AI latency starts holding open enough connections to matter. With one user, a synchronous
handler *is* a queue of depth one; nightly pre-generation already removes the common question-time
wait.

### Streaming responses (SSE)

Stream chat replies token-by-token: time-to-first-token drops from seconds to a few hundred
milliseconds, the biggest *felt* improvement available. Notes from the review:

- Without a job queue this is **simpler than the original plan assumed**: stream straight from
  the handler via `client.Messages.NewStreaming` — no worker/pub-sub machinery needed.
- `connect-src 'self'` already permits same-origin SSE; verify rather than loosening the CSP.
- The `loggingWriter` wrapper in `internal/server/middleware.go` must pass through
  `http.Flusher` or flushes silently stop at the middleware.
- **Markdown rendering stays server-side** (`internal/markdown` is the XSS boundary): re-render
  the accumulated text per flush, or stream plain text and render once at the end.
- htmx needs its SSE extension (another vendored file — still zero NPM) or a hand-rolled
  `EventSource` in `app.js`. Keep a non-streaming path as fallback.

**Build when:** the remaining dead spot — the first chat turn on a card whose question was
pre-generated — feels slow enough to bother the people actually using the app.

### Due-ness in SQL

`store.dueSchedules` loads all candidate rows and evaluates `sm2.State.IsDue` in Go, and
`TopicDueCounts` calls it per topic × 3 priorities (plus an ownership check each — 6 queries per
topic on every `/study` load). Expressing due-ness as a SQL predicate would collapse these into
single grouped queries and let `NextDue` pick `ORDER BY RANDOM() LIMIT 1`.

**The risk that kept this out of scope:** the SQL predicate must agree with `sm2.State.IsDue`
*exactly* — local-midnight day boundaries in the container's `TZ`, and `DaysBetween`'s rounding
(a deliberate DST fix the JS source lacks). Doing this requires a property-style test seeding
`last_seen`/`interval` combinations across DST transitions and asserting the SQL and Go paths
return identical sets, plus benchmarks (seed ~20 topics / 20k cards; time `TopicDueCounts`,
`StudyStats`, `NextDue`) to prove the win.

**Build when:** dashboard or study-index loads are measurably slow. At a few thousand cards,
SQLite serves the current N+1 in milliseconds.

### SQLite reader pool

Split `SetMaxOpenConns(1)` into a reader pool (N ≈ GOMAXPROCS) plus a single writer connection,
both WAL. Pragmas are per-connection; `backup_test.go`'s serialization comment would need
revisiting, and `VACUUM INTO` must be re-verified against concurrent readers.

**Build when:** actual read/write contention appears — e.g. the nightly batch upserts blocking
interactive reads. The store's own comment is right that one connection is fine at this traffic
level, and it eliminates `SQLITE_BUSY` handling entirely.

### Header-driven admission control

A token bucket sized from the `anthropic-ratelimit-*` response headers, ramping rather than
jumping to full concurrency (acceleration limits). The SDK's built-in retries (which honor
`retry-after` on 429 and back off on 5xx) plus typed error bubbles cover the single-user case.

**Build when:** concurrent users make it possible to *saturate* the org's rate limit, so requests
need to be smoothed before they 429 rather than retried after.

## Rejected

- **Redis** — proposed for job-queue and quota state shared across replicas. This app is one
  binary, one SQLite file, one container by design ("one container to run, one file to back up"),
  and SQLite pins it to a single writer node anyway, so there are no replicas to share state
  between. Everything above has an in-process design that works for the deployment this app
  actually has.
- **Per-user AI quotas** — meaningful in a multi-tenant service where one runaway session can
  consume the org's token budget. On an invite-only, effectively single-user instance the quota
  would only rate-limit the operator against themselves; the Anthropic console's own spend limits
  are the right guardrail.
