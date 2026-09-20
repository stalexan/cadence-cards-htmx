// Package markdown renders user- and model-authored markdown to safe HTML.
// Raw HTML in the source is replaced with an "omitted" comment rather than
// passed through (goldmark's default — html.WithUnsafe is deliberately not
// enabled), so neither Claude's output nor a card's contents can inject
// markup; the same setting makes goldmark drop javascript:, vbscript:, file:
// and non-image data: link and image destinations.
//
// Note the consequence for authors: typing literal HTML into a card does not
// display it as text, it makes it disappear. Markdown is the way to format.
package markdown

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"
)

// goldmark v2 has no single Markdown value: the parser and the renderer are
// built separately, and each extension comes in two halves that have to be
// wired to the matching side.  Both parse with the same parser, so Render and
// PlainText still cannot disagree about what the source means.  Neither value
// carries per-document state, so both are shared by every request.
var (
	mdParser   = parser.New(parser.WithExtensions(extension.GFMParser))
	mdRenderer = html.New(html.WithExtensions(extension.GFMHTMLRenderer))
)

// Render converts markdown to sanitized HTML. On a rendering error the text
// is returned escaped rather than dropped.
func Render(src string) template.HTML {
	source := []byte(src)
	var buf bytes.Buffer
	if err := mdRenderer.Render(&buf, source, mdParser.Parse(source)); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

// PlainText reduces markdown to its visible text: formatting markers are
// dropped and every block boundary collapses to a single space. It backs the
// one-line contexts — card table cells, the dashboard's recent activity —
// where rendered HTML would break the layout but a literal "**bold**" is just
// noise.
func PlainText(src string) string {
	source := []byte(src)
	doc := mdParser.Parse(source)
	var b strings.Builder
	// Walk never returns an error here: the callback below cannot fail.
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			// The end of a block is a word boundary — without this "a\n\nb"
			// would come out as "ab".  v2 dropped Node.Type(), so block-ness
			// is a type assertion on the marker interface instead.
			if _, ok := n.(ast.BlockNode); ok {
				b.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			// Value decodes with the decoder the parser attached to this node,
			// which is the work v1 made callers do by hand with
			// util.UnescapePunctuations and friends: a source "\*" or "&amp;"
			// becomes "*" and "&", matching what the rendered card shows. Str
			// would hand back the raw source instead.
			b.WriteString(t.Value.Value(source))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.AutoLink:
			// Label, not Destination: for a GFM-linkified "www.example.com"
			// the destination carries an "http://" the author never typed.
			b.WriteString(t.Label.Value(source))
		case *ast.CodeSpan:
			// A code span holds its own content in v2 instead of wrapping raw
			// Text children, so it needs a case of its own — without one, the
			// `call()` in a card would vanish from the table cell. Its value
			// carries the identity decoder, so an escape inside backticks
			// stays literal exactly as the renderer writes it.
			b.WriteString(t.Value.Value(source))
		case *ast.CodeBlock:
			// Fenced and indented code blocks are also one node in v2, with
			// their content on Value rather than the block's Lines(). Lines
			// has no decoder to apply, so Str is the whole of it.
			b.WriteString(t.Value.Str(source))
		}
		// Anything else (RawHTML, HTMLBlock) is dropped, which matches
		// Render's posture of never letting source HTML through.
		return ast.WalkContinue, nil
	})
	// Collapse every whitespace run to one space, and trim the ends.
	return strings.Join(strings.Fields(b.String()), " ")
}
