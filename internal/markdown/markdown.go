// Package markdown renders Claude's replies to safe HTML. Raw HTML in the
// source is escaped (goldmark's default — html.WithUnsafe is deliberately not
// enabled), so model output cannot inject markup.
package markdown

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
)

// Render converts markdown to sanitized HTML. On a rendering error the text
// is returned escaped rather than dropped.
func Render(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}
