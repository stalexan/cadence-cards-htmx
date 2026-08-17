# User Management and Authentication

How accounts come into existence, how the app decides who you are on each request, and what the
operator can do from the command line. This is the reference for the `-create-user`,
`-reset-password` and `-list-users` CLI commands and for everything behind the login form.

Companion docs: [`ARCHITECTURE.md`](ARCHITECTURE.md) sketches the same ground in a paragraph as part
of the request-flow tour; [`DATA_MODEL.md`](DATA_MODEL.md) has the column-level detail for the
`users` and `sessions` tables. This doc is the one that goes end to end.

## The account model

An account is an email, a display name, and a bcrypt password hash. That is the whole of it.
Deliberately absent — none of these exist anywhere in the codebase, so don't go looking:

- **No roles, no admin user.** Every account is equivalent; there is no in-app view of anyone else's
  data. Operator powers are the CLI, which is reached by having shell access to the host or
  container.
- **No email verification, no mailer.** The app sends no email at all, which is also why there is no
  self-service "forgot password" link — see [Resetting a forgotten password](#resetting-a-forgotten-password).
- **No OAuth or SSO.** The `users` table's nullable `email`/`name` columns are a port artifact of the
  source app's Auth.js schema, not room for federated identities.
- **No 2FA, no session-listing UI.** Sessions are revocable, but only in bulk (see
  [Session lifecycle](#session-lifecycle)).
- **No self-service account deletion.** See [Deleting an account](#deleting-an-account).

Authorization is not part of this doc, because there is nothing to configure: every store method
takes the acting user's ID and scopes rows by joining up to `topics.user_id` inside the same
transaction as the read or write. Owning a session for user *N* is exactly the set of permissions
user *N* has.

## Creating accounts

There are two paths in, and which ones are open is a deployment decision.

### Public registration

`GET /register` and `POST /register` are gated on `ENABLE_PUBLIC_REGISTRATION=true`. When it is
off — **the default** — both redirect to `/login`. A single-user or invite-only instance is the
expected shape; the flag is mostly there so a fresh local checkout can get its first account through
the browser.

Note that the login page's "Don't have an account? Register" link is unconditional, so with
registration off it simply bounces back to `/login`. Harmless, but it does mean the flag is enforced
at the route, not by hiding the entrance.

The handler (`handleRegister` in `internal/server/handlers_auth.go`) validates name and email
present, email parseable by `net/mail`, password 8–72 bytes, and password equal to its confirmation,
then bcrypts at cost 12 and inserts. A duplicate email comes back from the store as
`ErrDuplicate` and is re-rendered as "An account with this email already exists." On success it
redirects to `/login?registered=true` — registration does **not** log you in.

### `-create-user`

```bash
go run ./cmd/cadence -create-user you@example.com          # local
docker compose run --rm app -create-user you@example.com   # container
```

Prompts for a name and a password, then exits. This is the intended way to make the first account on
a deployment that leaves public registration off, and it works whether or not the server is running.

Two differences from the registration form are worth knowing, because they are asymmetries rather
than oversights:

| | Registration form | `-create-user` |
|---|---|---|
| Email syntax | validated with `net/mail` | **not validated** — whatever you type becomes the login identifier |
| Password | 8–72 bytes, typed twice | 8–72 bytes, typed once (no confirmation) |
| Name | required | required |
| Duplicate email | re-renders the form with an error | exits non-zero with the store's error |

The CLI trusts the operator: it is a local admin tool, and a typo'd email is fixable with
`-list-users` plus the profile page (or a second account).

### How the CLI handles input

Both `-create-user` and `-reset-password` read through a single `bufio.Reader` over stdin, and the
password specifically goes through `promptPasswordHash`:

- **On a terminal**, `term.ReadPassword` turns off echo, so the password never appears on screen or
  in the scrollback. Line-wise reading of the name beforehand is safe here because a terminal in
  canonical mode hands over one line per read — nothing is left buffered for the password prompt to
  eat.
- **On a pipe**, each prompt consumes the next line verbatim, spaces preserved. That makes the
  commands scriptable:

  ```bash
  printf 'Ada Lovelace\ncorrect-horse-battery\n' | \
    go run ./cmd/cadence -create-user ada@example.com
  ```

  The prompts still print to stdout; only the echo suppression is skipped.
- **Length is checked before hashing**: under 8 characters is rejected, and so is over 72 *bytes* —
  bcrypt's hard ceiling, past which input is silently truncated. The check is on bytes, not runes, so
  a passphrase of multi-byte characters hits the limit sooner than its character count suggests.
- **Cost is 12**, matching the web handlers and the source app's `password.ts`. Expect roughly a
  quarter-second of CPU per hash; that is the point.

## The CLI commands

Every command below is a flag on the same binary — `cadence -create-user ...` — so in the container
they are arguments to the image's entrypoint. `docker compose run --rm app <flag>` starts a
throwaway container against the same `cadence_data` volume; `docker compose exec app cadence <flag>`
runs inside the live one. Both work: SQLite is in WAL mode with `busy_timeout=5000`, so a second
process can write while the server is running.

| Command | What it does | Touches |
|---|---|---|
| `-create-user <email>` | Prompts for name + password, inserts the account, prints its ID. | users |
| `-reset-password <email>` | Prompts for a new password, updates the hash, **deletes every session that user has**. | users, sessions |
| `-list-users` | Prints ID, email, name and creation date (local time) as a table, oldest first. Never prints hashes. | read-only |

For completeness, the other two flags are not user management: `-version` prints the release and
exits before the logger, config and store are touched, and `-backup` snapshots the database via
`VACUUM INTO` (see the README's backup section and [`VERSIONING.md`](VERSIONING.md)).

**Order matters in `main`.** `-version` and `-backup` are handled *before* `store.Open`, precisely so
they never migrate. The three user commands are handled *after* it, so they do. That is a real
consequence in production: `docker compose run --rm app -create-user ...` runs the **image's**
binary, which may be newer than the running server, and opening the database applies any migrations
that binary carries. If the image is newer than the container in service, create the user *after*
deploying it, not before.

### Resetting a forgotten password

The profile page's change-password form requires the current password, which is no help to someone
who has lost it, and there is no mailer to send a reset link. The CLI is the whole recovery story:

```bash
go run ./cmd/cadence -reset-password you@example.com          # local
docker compose run --rm app -reset-password you@example.com   # container
```

An unknown email fails with `no user with email "..."` rather than silently doing nothing — use
`-list-users` if you're unsure which address an account was created under. On success every session
belonging to that user is deleted, not just the ones other than yours: the CLI has no session of its
own to preserve, and a reset means the old ones are no longer trusted.

### Deleting an account

There is no command and no UI for this. `sessions`, `topics` and everything below them are
`ON DELETE CASCADE` from `users`, and `foreign_keys(ON)` is set on every connection, so a single
`DELETE FROM users WHERE email = ?` erases the account and all of its content. The runtime image
ships no `sqlite3` binary, so doing it in production means either a `-backup` snapshot plus an
external tool, or adding the command — deliberate friction on an irreversible operation.

## Authentication

### Logging in

`POST /login` (`handleLogin`) runs in this order, and the order is the security property:

1. **IP lockout and auth-attempt rate limit** — checked before the database is touched at all, so a
   locked-out address cannot use login attempts to make the server do work.
2. **Email and password non-empty.**
3. **Account lockout** for that email address.
4. **User lookup.** If the email is unknown, the handler still runs `bcrypt.CompareHashAndPassword`
   against a hardcoded cost-12 `dummyHash` before failing. Without that, the ~250 ms gap between "no
   such user" and "wrong password" would enumerate which addresses have accounts.
5. **Password comparison.** An empty stored hash is treated as a failure, never as a match.
6. On success: **clear the account's failure record**, mint a session, set the cookie, redirect to
   `/dashboard`.

Every failure path renders the same login page with "Invalid email or password." at **HTTP 401** and
logs a `slog` warning carrying the email and IP. Lockouts get their own message ("Too many failed
attempts. Please try again later.") because the distinction is actionable for a real user and tells
an attacker nothing they can't measure anyway.

### Sessions and the cookie

Sessions are opaque server-side tokens, not signed cookies. There is no `AUTH_SECRET` and nothing is
encoded in the cookie value.

`CreateSession` reads 32 random bytes from `crypto/rand`, base64url-encodes them into the token, and
stores **only** `hex(sha256(token))` in `sessions.token_hash`. The raw token exists in exactly one
place: the user's cookie. A database dump therefore cannot be replayed as a set of live logins.

| Cookie attribute | Value | Why |
|---|---|---|
| Name | `cadence_session` | |
| `HttpOnly` | yes | JavaScript never needs it; this puts it out of reach of any XSS that gets past the CSP. |
| `Secure` | `COOKIE_SECURE`, default **true** | TLS terminates at nginx in the deployed setup. Over plain `http://` you must set `COOKIE_SECURE=false` or the browser silently drops the cookie and you bounce straight back to the login page. |
| `SameSite` | `Lax` | First half of the CSRF defense; `checkOrigin` is the second. |
| `Path` | `/` | |
| `Max-Age` | 30 days | `store.SessionDuration`, matching the source app's Auth.js `maxAge`. |

### A leaked `token_hash` is not a session

The stored value is not a credential. `GetSessionUser` hashes the *incoming* cookie before comparing,
so pasting a `token_hash` straight into a `cadence_session` cookie has the server compare
`sha256(hash)` against `hash` — no match. Using a leaked row would mean inverting SHA-256 over a
preimage of 32 `crypto/rand` bytes: 256 bits of entropy, nothing to brute-force, nothing a
precomputed table covers.

That entropy is also why the hash is unsalted and uncosted, which would be indefensible for a
password. The defence here is the preimage's randomness, not a KDF's cost — the same reason
`-list-users` printing password hashes would be a mistake while a session hash leaking is a
non-event.

Two things this does *not* protect against, and they are the ones worth designing around:

- **The raw token leaking.** XSS reading the cookie (kept out of reach by `HttpOnly` plus the
  no-`unsafe-inline` CSP), a cleartext hop (`Secure`, on by default), or the value being written
  somewhere it gets logged — the app never logs it. This is the realistic session-theft path.
- **Write access to the database**, as opposed to a read-only dump. An attacker who can
  `INSERT INTO sessions` chooses their own token and hashes it themselves. Hashing at rest defeats
  dump-and-replay; it does nothing against someone already inside the file.

If you suspect a token is loose, `-reset-password` is the blunt fix — it drops every session that
user has (see [Session lifecycle](#session-lifecycle)).

### Resolving the session on each request

`loadSession` sits in the middleware chain (after `securityHeaders`, before `checkOrigin`). If the
cookie is present it calls `GetSessionUser`, which resolves `token_hash` **and** checks
`expires_at > now` in the same query — so a row the hourly sweep hasn't reached yet still cannot
authenticate anything. A valid session attaches the `*store.User` to the request context; an invalid
one attaches nothing. `loadSession` never rejects: public pages need to work either way.

`requireUser` is the per-route gate, wrapped around every authenticated handler in `server.go`. No
user in context means a `303` redirect to `/login` for a normal navigation, or an `HX-Redirect: /login`
header with `401` for an HTMX request — the latter because swapping a login page into a fragment slot
would be useless.

### Session lifecycle

| Event | What is deleted | Where |
|---|---|---|
| Login | — (a row is created) | `CreateSession` |
| `POST /logout` | that one session | `DeleteSession` |
| Password change via profile | every session **except** the current one | `DeleteOtherSessions` |
| `-reset-password` | every session for that user | `DeleteUserSessions` |
| Expiry | all rows past `expires_at` | `DeleteExpiredSessions`, hourly ticker |
| Account deleted | all of that user's rows | FK cascade |

Revocation being a plain `DELETE` is the payoff of server-side tokens: keeping the current session
alive while killing every other one is not something a stateless signed token can express.

### Expiry is absolute, not idle

`expires_at` is written once, at `INSERT`, and from then on it is only read — by the lookup's
`expires_at > ?` and by the sweep. There is no `UPDATE sessions` statement anywhere in the codebase,
so **activity never extends a session**: one minted today dies 30 days from today whether it is used
hourly or never touched again. There is no inactivity timeout either — the two are the same absence.

The cookie's `Max-Age` is the same 30 days and is also set only at login, so browser-side and
server-side expiry normally lapse together. Because `Max-Age` is set at all, the cookie is
persistent rather than per-browser-session: **closing the browser does not sign you out.**

The practical consequence is that an unattended logged-in browser stays usable for the rest of the
window, and the only early exits are the rows in the table above. Adding an idle timeout would mean
refreshing `expires_at` on read, which turns every authenticated GET into a write — worth weighing
deliberately against `SetMaxOpenConns(1)` rather than adding by reflex.

## Changing your own details

`GET /profile` has two forms, both plain `POST` round-trips rather than HTMX fragments.

**Account** (`POST /profile`) updates name and email. Email is required and must parse; a collision
with another account re-renders with "An account with this email already exists." Changing your email
changes the address you log in with, immediately — there is no confirmation step and no verification
mail.

**Change Password** (`POST /profile/password`) requires the current password, a new one of 8–72
bytes, and its confirmation. On success it rehashes at cost 12, writes the new hash, and calls
`DeleteOtherSessions` — so a stolen session elsewhere dies the moment you change your password, while
the browser you're sitting at stays signed in. The page says so, and it is the reason the current
password is required: without it, an unattended logged-in browser could be used to lock the owner out.

## Rate limiting and lockouts

`internal/ratelimit` is an in-memory port of the source app's `rate-limiter.ts`. It holds three maps —
general per-IP request counts, per-IP auth failures, per-email auth failures — pruned hourly by the
maintenance ticker.

| Limit | Threshold | Duration | Applies to |
|---|---|---|---|
| General request limit | 1000 requests | 15-minute window | every request, keyed by IP |
| Auth-attempt limit | 3 failures | 30-minute window | login, keyed by IP |
| IP cooldown | 3 failures | 5 minutes | login, keyed by IP |
| IP lockout | 5 failures | 15 minutes | login, keyed by IP |
| Account lockout | 3 failures | 30 minutes | login, keyed by email |

Three consequences worth internalizing:

- **State is per process and in memory.** A restart clears every lockout, and there is no shared
  store, so this is protection against brute force, not a distributed defense.
- **`X-Real-IP` must be set by the proxy.** It is the only client-IP header the app trusts; anything
  else is spoofable. Without it every request behind the proxy shares the socket address of the proxy
  itself, and the first three bad logins lock out *everyone*.
- **A successful login clears the email's failure record but deliberately leaves the IP's alone.**
  Otherwise an attacker who owns one valid account on that address could launder the IP's brute-force
  budget by logging in between guesses.

`DISABLE_RATE_LIMITING=true` short-circuits all of it. It exists for local development and load
testing; setting it on a public host removes the only brute-force defense the app has.

## Configuration summary

| Variable | Default | Effect on this doc's subject |
|---|---|---|
| `ENABLE_PUBLIC_REGISTRATION` | `false` | Opens `/register`. Off means accounts come from `-create-user` only. |
| `COOKIE_SECURE` | `true` | `Secure` flag on the session cookie. Must be `false` to log in over plain HTTP. |
| `DISABLE_RATE_LIMITING` | `false` | Disables every limit and lockout in the table above. |

## Where the code lives

| Concern | File |
|---|---|
| CLI commands and prompts | `cmd/cadence/main.go` (`runCreateUser`, `runResetPassword`, `runListUsers`, `promptPasswordHash`) |
| Login, logout, registration | `internal/server/handlers_auth.go` |
| Profile and password change | `internal/server/handlers_profile.go` |
| Cookie, `loadSession`, `requireUser` | `internal/server/session.go` |
| Client IP, CSRF origin check, headers | `internal/server/middleware.go` |
| User SQL | `internal/store/users.go` |
| Session SQL and token hashing | `internal/store/sessions.go` |
| Lockouts and limits | `internal/ratelimit/ratelimit.go` |
| Schema | `internal/store/migrations/0001_init.sql` |
