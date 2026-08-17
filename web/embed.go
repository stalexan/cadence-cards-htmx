// Package web embeds the templates and static assets into the binary.
package web

import "embed"

// Templates holds layouts, partials, and page templates.
//
//go:embed templates
var Templates embed.FS

// Static holds htmx, app.js, app.css, and the favicon.
//
//go:embed static
var Static embed.FS

// Samples holds the bundled sample topics offered at /samples, one topic-format
// YAML file each. They live here rather than on disk so they ship in the single
// binary and are present in the container; internal/samples parses them.
//
// Deliberately outside Static: these are never served as assets, so they must
// not feed assetVersion's fingerprint or gain a cache-busting URL.
//
//go:embed samples
var Samples embed.FS
