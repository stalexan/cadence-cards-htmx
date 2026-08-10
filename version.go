// Package cadence exposes the project's release version.
//
// VERSION at the repository root is the single source of truth: it is embedded
// into the binary at build time, so the number the app reports cannot drift
// from the number git records. Nothing at runtime can override it.
//
// Keep this package dependency-free (stdlib only) — internal packages import
// it, so a cadence-cards/... import here would cycle.
package cadence

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is VERSION with surrounding whitespace trimmed (the file ends in a
// newline). Well-formedness is asserted by TestVersionWellFormed, which runs
// inside the Docker build — a malformed VERSION fails the image build.
var Version = strings.TrimSpace(versionFile)
