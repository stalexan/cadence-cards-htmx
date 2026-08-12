package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []dotEnvVar
	}{
		{
			name:  "basic pair",
			input: "FOO=bar\n",
			want:  []dotEnvVar{{"FOO", "bar"}},
		},
		{
			name:  "blank lines and comments skipped",
			input: "\n# a comment\n   # indented comment\nFOO=bar\n\n",
			want:  []dotEnvVar{{"FOO", "bar"}},
		},
		{
			name:  "export prefix stripped",
			input: "export FOO=bar\n",
			want:  []dotEnvVar{{"FOO", "bar"}},
		},
		{
			name:  "whitespace around key and value trimmed",
			input: "  FOO  =  bar  \n",
			want:  []dotEnvVar{{"FOO", "bar"}},
		},
		{
			name:  "equals inside value preserved",
			input: "TOKEN=abc==\n",
			want:  []dotEnvVar{{"TOKEN", "abc=="}},
		},
		{
			name:  "empty value",
			input: "FOO=\n",
			want:  []dotEnvVar{{"FOO", ""}},
		},
		{
			name:  "double quotes stripped and escapes interpreted",
			input: "FOO=\"a\\nb\\tc\\\\d\\\"e\"\n",
			want:  []dotEnvVar{{"FOO", "a\nb\tc\\d\"e"}},
		},
		{
			name:  "single quotes are literal",
			input: "FOO='a\\nb'\n",
			want:  []dotEnvVar{{"FOO", `a\nb`}},
		},
		{
			name:  "quotes preserve surrounding whitespace",
			input: "FOO=\"  spaced  \"\n",
			want:  []dotEnvVar{{"FOO", "  spaced  "}},
		},
		{
			name:  "unmatched quote left alone",
			input: `FOO="bar` + "\n",
			want:  []dotEnvVar{{"FOO", `"bar`}},
		},
		{
			name:  "hash in unquoted value is not a comment",
			input: "KEY=sk-ant-a#b\n",
			want:  []dotEnvVar{{"KEY", "sk-ant-a#b"}},
		},
		{
			name:  "trailing inline comment is kept verbatim",
			input: "NUM=4000 # tokens\n",
			want:  []dotEnvVar{{"NUM", "4000 # tokens"}},
		},
		{
			name:  "CRLF line endings",
			input: "FOO=bar\r\nBAZ=qux\r\n",
			want:  []dotEnvVar{{"FOO", "bar"}, {"BAZ", "qux"}},
		},
		{
			name:  "UTF-8 BOM stripped from first line",
			input: "\ufeffFOO=bar\n",
			want:  []dotEnvVar{{"FOO", "bar"}},
		},
		{
			name:  "no trailing newline",
			input: "FOO=bar",
			want:  []dotEnvVar{{"FOO", "bar"}},
		},
		{
			name:  "underscore and digits in key",
			input: "_A1_B=x\n",
			want:  []dotEnvVar{{"_A1_B", "x"}},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDotEnv(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("parseDotEnv() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseDotEnv() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("var %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseDotEnvErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"missing equals", "FOO=bar\nNOEQUALS\n", "line 2: missing '='"},
		{"empty key", "=value\n", `line 1: invalid key ""`},
		{"key starting with digit", "1FOO=bar\n", `line 1: invalid key "1FOO"`},
		{"key with dash", "FOO-BAR=baz\n", `line 1: invalid key "FOO-BAR"`},
		{"key with space", "FOO BAR=baz\n", `line 1: invalid key "FOO BAR"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseDotEnv(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("parseDotEnv() expected an error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("parseDotEnv() error = %q, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDotEnvMissingFileIsNotAnError(t *testing.T) {
	t.Parallel()

	set, skipped, err := loadDotEnv(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("loadDotEnv() error = %v, want nil", err)
	}
	if set != 0 || skipped != 0 {
		t.Errorf("loadDotEnv() = (%d, %d), want (0, 0)", set, skipped)
	}
}

// The environment must win over the file, so an inline override such as
// CLAUDE_MAX_TOKENS=99 go run ./cmd/cadence keeps working.
func TestLoadDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	path := writeTempEnv(t, "PRESET=fromfile\nFRESH=fromfile\n")

	t.Setenv("PRESET", "fromenv")
	os.Unsetenv("FRESH")

	set, skipped, err := loadDotEnv(path)
	if err != nil {
		t.Fatalf("loadDotEnv() error = %v", err)
	}
	if set != 1 || skipped != 1 {
		t.Errorf("loadDotEnv() = (set %d, skipped %d), want (1, 1)", set, skipped)
	}
	if got := os.Getenv("PRESET"); got != "fromenv" {
		t.Errorf("PRESET = %q, want %q (file must not override the environment)", got, "fromenv")
	}
	if got := os.Getenv("FRESH"); got != "fromfile" {
		t.Errorf("FRESH = %q, want %q", got, "fromfile")
	}
	t.Cleanup(func() { os.Unsetenv("FRESH") })
}

// An empty value in the file still counts as set, shadowing nothing.
func TestLoadDotEnvSetsEmptyValue(t *testing.T) {
	path := writeTempEnv(t, "EMPTY_VAL=\n")
	os.Unsetenv("EMPTY_VAL")
	t.Cleanup(func() { os.Unsetenv("EMPTY_VAL") })

	if _, _, err := loadDotEnv(path); err != nil {
		t.Fatalf("loadDotEnv() error = %v", err)
	}
	v, ok := os.LookupEnv("EMPTY_VAL")
	if !ok || v != "" {
		t.Errorf("EMPTY_VAL = (%q, %v), want (\"\", true)", v, ok)
	}
}

func TestLoadDotEnvParseErrorNamesFile(t *testing.T) {
	t.Parallel()

	path := writeTempEnv(t, "GOOD=1\nBROKEN\n")
	_, _, err := loadDotEnv(path)
	if err == nil {
		t.Fatal("loadDotEnv() expected an error, got nil")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "line 2: missing '='") {
		t.Errorf("loadDotEnv() error = %q, want it to name %q and line 2", err, path)
	}
}

func writeTempEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp .env: %v", err)
	}
	return path
}

// Boolean env vars must parse strictly: "True" and "1" work, garbage errors
// loudly instead of silently flipping a security default.
func TestLoadStrictBoolParsing(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "True")
	cfg, err := Load()
	if err != nil || !cfg.CookieSecure {
		t.Errorf(`COOKIE_SECURE="True": cfg=%+v err=%v, want secure`, cfg.CookieSecure, err)
	}

	t.Setenv("COOKIE_SECURE", "0")
	cfg, err = Load()
	if err != nil || cfg.CookieSecure {
		t.Errorf(`COOKIE_SECURE="0": cfg=%+v err=%v, want insecure`, cfg.CookieSecure, err)
	}

	t.Setenv("COOKIE_SECURE", "yes")
	if _, err := Load(); err == nil {
		t.Error(`COOKIE_SECURE="yes": want a load error, got nil`)
	}
}
