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
