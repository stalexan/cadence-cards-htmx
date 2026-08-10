# Versioning Guide

This project uses **Semantic Versioning (SemVer)** with a `VERSION` file
tracked in git.

## Semantic Versioning Format

Versions follow the `MAJOR.MINOR.PATCH` format:

- **MAJOR**: Incompatible API changes or major feature overhauls
- **MINOR**: New functionality in a backward-compatible manner
- **PATCH**: Backward-compatible bug fixes

## Version Progression

This project's first tagged release is `v1.0.3`, continuing the version line of
the sibling `cadence-cards-svelte` app rather than restarting at `0.1.0`. The
ladder below is general SemVer guidance for future work, not a record of this
repository's history.

### Pre-Release Versions (0.x.x)

A greenfield project starts at `0.1.0-beta` and progresses through testing
phases:

```
0.1.0-alpha   → Very early internal testing
0.1.0-beta    → Feature-complete enough for wider testing
0.1.1-beta    → Bug fixes during beta
0.2.0-beta    → Additional features during beta
0.3.0-beta    → More features/improvements
...
0.9.0-rc1     → Release candidate (final testing before production)
0.9.0-rc2     → Second release candidate (if needed)
1.0.0         → First production-ready release
```

### Production Versions (1.x.x+)

Once at `1.0.0`, follow strict SemVer:

```
1.0.0  → First stable release
1.0.1  → Bug fix
1.1.0  → New feature (backward-compatible)
2.0.0  → Breaking change
```

## Using the VERSION File

The VERSION file is committed to git with a commit message about the new
version number, creating a permanent record of what version each commit
represents. This means even if a tag is accidentally moved, you can always find
the correct commit by checking the VERSION file history.

Note: The VERSION file contains the raw version number (e.g., 1.0.3), while git
tags are prefixed with v (e.g., v1.0.3) by convention to distinguish them as
version tags.

### The VERSION file is the source of truth

The file is embedded into the binary at build time. `version.go` at the
repository root does:

```go
//go:embed VERSION
var versionFile string

var Version = strings.TrimSpace(versionFile)
```

Everything that reports a version reads `cadence.Version` — the startup log
line in `cmd/cadence`, the sidebar badge via `internal/server/render.go`, and
the `-version` flag. **Nothing at runtime can override it.** There is no
`APP_VERSION` environment variable; a leftover `APP_VERSION=` in a local `.env`
is simply ignored.

```
{"msg":"cadence-cards listening","port":3000,"version":"1.0.3",...}
```

This is why the file lives at the repository root rather than under a package:
Go's `embed` cannot reference paths outside the embedding package's directory
and rejects symlinks, so the root `VERSION` needs a root package to embed it.
Deriving the version from git instead is not an option — `.dockerignore`
excludes `.git`, so the build cannot run `git describe`.

**A release is therefore one edit:** bump `VERSION`. The number the app reports
cannot disagree with the number git records, because there is only one number.

### Creating a New Version

1. **Update the VERSION file:**
   ```bash
   echo "1.0.4" > VERSION
   ```

2. **Commit the version change:**
   ```bash
   git add VERSION
   git commit -m "chore: bump version to 1.0.4"
   ```

3. **Tag that commit** — the tag and the VERSION file must point at the same
   place, so tag immediately after committing, while it is still `HEAD`.
   Deriving the tag from the file makes a typo structurally impossible:
   ```bash
   git tag -a "v$(cat VERSION)" -m "Release $(cat VERSION)"
   ```

4. **Run the tests:**
   ```bash
   go test ./...
   ```

   `TestVersionMatchesGitTag` fails if `HEAD` carries a tag that disagrees with
   `VERSION`. It skips when git or `.git` is absent — which is exactly the case
   inside the Docker build — so it only protects you when run from a checkout.

5. **Push to origin:**
   ```bash
   git push --follow-tags origin main
   ```

   `--follow-tags` pushes the branch along with any annotated tags reachable
   from it. Prefer it over `--tags`, which pushes *every* tag in the local
   repository, including ones you were not ready to publish.

### Checking What Is Deployed

Three ways to ask a running or built image what it contains:

```bash
docker compose run --rm app -version   # prints "1.0.3" and exits
```

`-version` is handled before the logger, config loading, and the database open,
so it prints one bare line, works with a broken or empty environment, and never
migrates anything. Locally: `go run ./cmd/cadence -version`.

The other two are the startup log line shown above, and the version badge in the
sidebar (and mobile drawer) of every signed-in page.

### If VERSION Is Malformed

`TestVersionWellFormed` asserts the file is non-empty and shaped like
`MAJOR.MINOR.PATCH[-prerelease]`. The Dockerfile runs `go test ./...` as part of
the image build, so a malformed VERSION **fails `docker compose up --build`**
rather than shipping a binary with a blank or garbled version.

Note that bumping VERSION invalidates the Dockerfile's `COPY . .` layer, forcing
a full rebuild and test run. That is correct — the binary has to actually
contain the new string.

### Fixing a Bad Tag

If a tag landed on the wrong commit and has **not** been pushed, just move it:

```bash
git tag -d v1.0.3
git tag -a v1.0.3 -m "Release 1.0.3" <correct-commit>
```

If it has already been pushed, deleting it remotely is disruptive — anyone who
has fetched the tag keeps the old one until they prune, and checkouts pinned to
it change meaning underneath them. Prefer publishing a new patch version.
When you must:

```bash
git push origin --delete v1.0.3
git push origin v1.0.3
```

The VERSION file history is the tiebreaker in either case: it records what each
commit claimed to be, independent of where the tags ended up.

### Viewing Version History

To see all version changes in the project:

```bash
git log --oneline -- VERSION
```

The `--` separates paths from refs, so git does not have to guess whether
`VERSION` names a file or a branch. This shows every commit that modified the
file, giving you a complete version history.
