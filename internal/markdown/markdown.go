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

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
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

// PlainText reduces markdown to its visible text: formatting markers are
// dropped and every block boundary collapses to a single space. It backs the
// one-line contexts — card table cells, the dashboard's recent activity —
// where rendered HTML would break the layout but a literal "**bold**" is just
// noise. It parses with the same md as Render, so the two cannot disagree
// about what the source means.
func PlainText(src string) string {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	var b strings.Builder
	// Walk never returns an error here: the callback below cannot fail.
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			// The end of a block is a word boundary — without this "a\n\nb"
			// would come out as "ab".
			if n.Type() == ast.TypeBlock {
				b.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			v := t.Value(source)
			if !t.IsRaw() {
				// The same unescaping goldmark's HTML writer does before
				// escaping. Without it a source "\*" or "&amp;" would survive
				// here as "\*" and "&amp;" while the rendered card shows "*"
				// and "&". Raw text (code spans) is deliberately left alone,
				// exactly as the HTML renderer leaves it.
				v = util.UnescapePunctuations(v)
				v = util.ResolveNumericReferences(v)
				v = util.ResolveEntityNames(v)
			}
			b.Write(v)
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		case *ast.AutoLink:
			// Label, not URL: for a GFM-linkified "www.example.com" the URL
			// carries an "http://" the author never typed.
			b.Write(t.Label(source))
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			// Code blocks keep their content in Lines(), not in Text children.
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i) // Value has a pointer receiver.
				b.Write(seg.Value(source))
			}
		}
		// Anything else (RawHTML, HTMLBlock) is dropped, which matches
		// Render's posture of never letting source HTML through.
		return ast.WalkContinue, nil
	})
	// Collapse every whitespace run to one space, and trim the ends.
	return strings.Join(strings.Fields(b.String()), " ")
}
