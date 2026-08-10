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

### The VERSION file is not read by the binary

The file is a git-side record only. Nothing in the build embeds it. What the
running app reports comes from the `APP_VERSION` environment variable, which
`internal/config` reads into `cfg.AppVersion` and `cmd/cadence` logs on startup:

```
{"msg":"cadence-cards listening","port":3000,"version":"1.0.3",...}
```

`docker-compose.yml` passes `APP_VERSION=${APP_VERSION:-}` through to the
container, so the value comes from the deploy environment or `./.env`. It
defaults to empty, and an unset or stale `APP_VERSION` is silently accepted —
the app logs whatever it is given, including nothing at all.

**So a release is two edits, not one:** bump `VERSION`, and bump `APP_VERSION`
in the deployment's `.env` (`.env.example` carries a sample value that should
move with it). If these drift, the git history and the running app disagree
about what is deployed.

### Creating a New Version

1. **Update the VERSION file:**
   ```bash
   echo "1.0.3" > VERSION
   ```

2. **Update `APP_VERSION` to match**, in `.env.example` and in the `.env` of
   any deployment, so the running app reports the same number.

3. **Commit the version change:**
   ```bash
   git add VERSION
   git commit -m "chore: bump version to 1.0.3"
   ```

4. **Tag that commit** — the tag and the VERSION file must point at the same
   place, so tag immediately after committing, while it is still `HEAD`:
   ```bash
   git tag -a v1.0.3 -m "Release 1.0.3"
   ```

5. **Push to origin:**
   ```bash
   git push --follow-tags origin main
   ```

   `--follow-tags` pushes the branch along with any annotated tags reachable
   from it. Prefer it over `--tags`, which pushes *every* tag in the local
   repository, including ones you were not ready to publish.

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
