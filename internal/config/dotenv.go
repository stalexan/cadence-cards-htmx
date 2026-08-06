package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// dotEnvVar is one key/value pair parsed out of a .env file.
type dotEnvVar struct {
	Key   string
	Value string
}

// parseDotEnv reads KEY=VALUE lines. It is pure: no I/O beyond r, and it never
// touches the process environment.
//
// Blank lines and lines whose first non-space character is '#' are skipped, an
// optional "export " prefix is stripped, and the split happens on the first '='
// only (so '=' inside a value is safe). A value wrapped in matching single or
// double quotes has them removed; inside double quotes the escapes \n, \r, \t,
// \\ and \" are interpreted.
//
// An unquoted value runs literally to the end of the line — there is no inline
// "# comment" stripping. That is deliberate: truncating at " #" would silently
// mangle a secret containing a hash, whereas keeping the text produces a loud
// validation error from Load instead.
func parseDotEnv(r io.Reader) ([]dotEnvVar, error) {
	var vars []dotEnvVar
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if line == 1 {
			text = strings.TrimPrefix(text, "\ufeff") // UTF-8 BOM
		}
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(text, "export "); ok {
			text = strings.TrimSpace(rest)
		}

		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '='", line)
		}
		key = strings.TrimSpace(key)
		if !validKey(key) {
			return nil, fmt.Errorf("line %d: invalid key %q", line, key)
		}
		vars = append(vars, dotEnvVar{Key: key, Value: unquote(strings.TrimSpace(value))})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

// loadDotEnv reads path and exports any key that is not already present in the
// environment, so a real environment variable always wins over the file. A
// missing file is not an error. The returned counts are for logging.
func loadDotEnv(path string) (set, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer f.Close()

	vars, err := parseDotEnv(f)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	for _, v := range vars {
		if _, present := os.LookupEnv(v.Key); present {
			skipped++
			continue
		}
		if err := os.Setenv(v.Key, v.Value); err != nil {
			return set, skipped, fmt.Errorf("%s: set %s: %w", path, v.Key, err)
		}
		set++
	}
	return set, skipped, nil
}

// validKey reports whether s matches [A-Za-z_][A-Za-z0-9_]*.
func validKey(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// unquote strips a matching pair of surrounding quotes, interpreting escapes
// only inside double quotes (shell single-quote semantics: literal).
func unquote(s string) string {
	if len(s) < 2 || s[0] != s[len(s)-1] {
		return s
	}
	switch s[0] {
	case '\'':
		return s[1 : len(s)-1]
	case '"':
		return unescape(s[1 : len(s)-1])
	}
	return s
}

// unescape interprets \n, \r, \t, \\ and \" and leaves any other backslash
// sequence untouched.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
