package markdown

import (
	"strings"
	"testing"
)

// The package renders user-authored card content as well as Claude's replies,
// so the "no raw HTML" guarantee is load-bearing rather than incidental. It
// rests entirely on html.WithUnsafe being left off in Render's goldmark
// options; these tests fail if someone turns it on.
//
// goldmark's unsafe-off behaviour is to *omit* raw HTML, not to escape it, so
// the assertion is that the markup is gone rather than that it is visible.
func TestRenderOmitsRawHTML(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"script tag", `<script>alert(1)</script>`},
		{"img onerror", `<img src=x onerror="alert(1)">`},
		{"inline span", `hello <span onclick="x()">there</span>`},
		{"html block", "<div>\n<b>block</b>\n</div>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(Render(c.src))
			for _, leak := range []string{"<script", "onerror=", "onclick=", "<div>", "<span", "<img"} {
				if strings.Contains(got, leak) {
					t.Errorf("Render(%q) leaked %q: %s", c.src, leak, got)
				}
			}
			if !strings.Contains(got, "raw HTML omitted") {
				t.Errorf("Render(%q) = %s, want the raw HTML omitted", c.src, got)
			}
		})
	}
}

// goldmark's IsDangerousURL filters link and image destinations whenever the
// renderer is not in unsafe mode. Card content is user-authored, so this is
// the guard that keeps a javascript: link out of the study screen.
func TestRenderDropsDangerousURLs(t *testing.T) {
	dangerous := []string{
		`[x](javascript:alert(1))`,
		`[x](JaVaScRiPt:alert(1))`,
		`[x](vbscript:msgbox)`,
		`[x](data:text/html,<script>alert(1)</script>)`,
		`![x](javascript:alert(1))`,
	}
	for _, src := range dangerous {
		got := strings.ToLower(string(Render(src)))
		if strings.Contains(got, "javascript:") || strings.Contains(got, "vbscript:") ||
			strings.Contains(got, "data:text/html") {
			t.Errorf("Render(%q) kept a dangerous destination: %s", src, got)
		}
	}

	safe := string(Render(`[x](https://example.com/a)`))
	if !strings.Contains(safe, `href="https://example.com/a"`) {
		t.Errorf("Render dropped a safe https link: %s", safe)
	}
}

func TestPlainText(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"empty", "", ""},
		{"plain passes through", "just some text", "just some text"},
		{"bold", "**bold**", "bold"},
		{"italic and code span", "an *emphatic* `call()`", "an emphatic call()"},
		{"heading", "# Heading", "Heading"},
		{"bullet list", "- one\n- two", "one two"},
		{"ordered list", "1. one\n2. two", "one two"},
		{"paragraph break", "a\n\nb", "a b"},
		{"soft line break", "a\nb", "a b"},
		{"link keeps its label", "[label](https://example.com)", "label"},
		{"image keeps its alt text", "![alt text](https://example.com/i.png)", "alt text"},
		{"fenced code keeps contents", "```go\nfmt.Println(1)\n```", "fmt.Println(1)"},
		{"blockquote", "> quoted", "quoted"},
		{"gfm table", "| a | b |\n|---|---|\n| 1 | 2 |", "a b 1 2"},
		{"strikethrough", "~~gone~~", "gone"},
		{"raw html is dropped", "before <b>x</b> after", "before x after"},
		{"leading and trailing space trimmed", "  padded  ", "padded"},
		{"task list", "- [x] done\n- [ ] todo", "done todo"},
		{"explicit autolink", "<https://example.com>", "https://example.com"},
		// Label, not URL: URL() would prepend the scheme the author omitted.
		{"gfm linkified bare domain", "see www.example.com now", "see www.example.com now"},
		// Escapes and entities resolve, matching what Render shows...
		{"backslash escape resolves", `escaped \* star`, "escaped * star"},
		{"entity resolves", "a &amp; b", "a & b"},
		// ...except inside a code span, which is raw in the renderer too.
		{"escape stays literal in code", "`raw \\* star`", `raw \* star`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PlainText(c.src); got != c.want {
				t.Errorf("PlainText(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// The whole reason PlainText walks the AST instead of pattern-matching the
// source: a literal asterisk in prose is not emphasis, and only the parser
// knows the difference.
func TestPlainTextLeavesLiteralAsterisksAlone(t *testing.T) {
	for _, src := range []string{"2 * 3 * 4", "a_b_c_d", "5 - 3 = 2"} {
		if got := PlainText(src); got != src {
			t.Errorf("PlainText(%q) = %q, want it unchanged", src, got)
		}
	}
}

// The table cells PlainText feeds are single-line, so no output may contain a
// newline no matter how block-heavy the source is.
func TestPlainTextIsSingleLine(t *testing.T) {
	src := "# Title\n\nPara one.\n\n- a\n- b\n\n```\ncode\nlines\n```\n\n> quote\n"
	got := PlainText(src)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("PlainText kept a line break: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("PlainText left a double space: %q", got)
	}
}
