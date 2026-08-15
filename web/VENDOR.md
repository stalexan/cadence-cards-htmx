# Vendored assets

Third-party files committed under `static/`. There is no package manager here (zero NPM
dependencies is a project goal), so this file **is** the lockfile: it records what was downloaded,
from where, and what it hashed to.

`vendor_test.go` enforces it. If the recorded SHA-256 disagrees with the embedded file, or a version
appears in one field but not another, `go test ./...` fails — which also fails the Docker build,
since the image runs the suite. Update the file and this record in the same commit.

Files we wrote ourselves (`app.css`, `app.js`, `favicon.svg`) are not listed.

## htmx.js

- Version: 2.0.10
- Source: https://unpkg.com/htmx.org@2.0.10/dist/htmx.js
- SHA-256: 739498204ed3d437a37fd5ca3d16b1bda14e3b93353ea7440c7f129bd0bf93d5
- Vendored: 2026-08-15
- License: 0BSD

The unminified `dist/htmx.js`, deliberately in place of `dist/htmx.min.js`: it costs ~114K of
uncompressed asset size and buys a readable `git diff` on every upgrade, which is the only review
this dependency gets.

## Updating

```bash
curl -s https://registry.npmjs.org/htmx.org/latest | grep -o '"version":"[^"]*"'
curl -sfL -o web/static/htmx.js https://unpkg.com/htmx.org@X.Y.Z/dist/htmx.js
sha256sum web/static/htmx.js          # → the SHA-256 field above
go test ./...
```

Stay on the 2.x line. A 3.0 is a project with its own branch, not an update.

`go test` proves only that the recorded metadata is honest — **no Go test ever executes htmx**. The
behavior that can break lives entirely client-side, so after an upgrade walk these by hand with the
browser console open:

- a study grade that loses the optimistic-locking race → the `grade_conflict` fragment auto-refetches
  (depends on 409 being swappable)
- an import that fails validation → the 422 result panel swaps in
- a chat reply → the out-of-band swaps land in the transcript
- study next → `HX-Push-Url` updates the address bar, and a refresh re-serves the same card
- any page → no CSP violations in the console (htmx runs with `allowEval:false`, and the strict CSP
  has no `unsafe-inline`/`unsafe-eval` to fall back on)

Watch the release notes for changes to the `htmx-config` `responseHandling` key in particular:
`templates/layout/base.html` depends on it to make 409 and 422 swappable, and a rename there would
silently disable both flows above.
