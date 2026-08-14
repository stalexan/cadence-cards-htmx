// Package config loads application configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// dotEnvPath is read at startup when present. It is absent from the container
// image (.dockerignore), so this is effectively local-development only.
const dotEnvPath = ".env"

// Config holds all runtime configuration.
type Config struct {
	Port   int    // PORT, default 3000
	DBPath string // DB_PATH, default /data/cadence.db

	// Claude API
	ClaudeAPIKey    string // CLAUDE_API_KEY
	ClaudeModel     string // CLAUDE_MODEL, default claude-opus-5
	ClaudeEffort    string // CLAUDE_EFFORT, default high (low|medium|high|xhigh|max)
	ClaudeMaxTokens int    // CLAUDE_MAX_TOKENS, fallback 16000 (caps thinking + reply)

	// Question generation is a small extraction task; these route it to a
	// cheaper model / lower effort than the tutoring chat. Empty means "same
	// as CLAUDE_MODEL / CLAUDE_EFFORT".
	ClaudeQuestionModel  string // CLAUDE_QUESTION_MODEL
	ClaudeQuestionEffort string // CLAUDE_QUESTION_EFFORT

	// Nightly question pre-generation (Message Batches). Runs at the first
	// hourly tick at or after QUESTION_PREGEN_HOUR local time — local because
	// SM-2 due dates already make TZ load-bearing.
	DisableQuestionPregen bool // DISABLE_QUESTION_PREGEN
	QuestionPregenHour    int  // QUESTION_PREGEN_HOUR, default 3 (0-23)

	EnablePublicRegistration bool // ENABLE_PUBLIC_REGISTRATION
	DisableRateLimiting      bool // DISABLE_RATE_LIMITING
	CookieSecure             bool // COOKIE_SECURE, default true (TLS terminates at nginx)
}

// Load reads configuration from the environment, first exporting any variables
// found in .env that are not already set (see loadDotEnv).
func Load() (Config, error) {
	set, skipped, err := loadDotEnv(dotEnvPath)
	if err != nil {
		return Config{}, err
	}
	if set+skipped > 0 {
		// Counts only — this file holds the API key.
		slog.Info("loaded .env", "path", dotEnvPath, "set", set, "skippedAlreadySet", skipped)
	}

	cfg := Config{
		Port:                3000,
		DBPath:              "/data/cadence.db",
		ClaudeAPIKey:        os.Getenv("CLAUDE_API_KEY"),
		ClaudeModel:         "claude-opus-5",
		ClaudeEffort:        "high",
		ClaudeMaxTokens:     16000,
		ClaudeQuestionModel: os.Getenv("CLAUDE_QUESTION_MODEL"),
		QuestionPregenHour:  3,
		CookieSecure:        true,
	}

	// Booleans parse strictly (true/false/1/0/t/f, per strconv.ParseBool):
	// a typo like COOKIE_SECURE="True " must fail loudly, not silently flip
	// a security setting to its zero value.
	for _, b := range []struct {
		name string
		dst  *bool
	}{
		{"ENABLE_PUBLIC_REGISTRATION", &cfg.EnablePublicRegistration},
		{"DISABLE_RATE_LIMITING", &cfg.DisableRateLimiting},
		{"DISABLE_QUESTION_PREGEN", &cfg.DisableQuestionPregen},
		{"COOKIE_SECURE", &cfg.CookieSecure},
	} {
		v := os.Getenv(b.name)
		if v == "" {
			continue
		}
		val, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid %s %q (want true or false)", b.name, v)
		}
		*b.dst = val
	}

	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return cfg, fmt.Errorf("invalid PORT %q", v)
		}
		cfg.Port = p
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("CLAUDE_MODEL"); v != "" {
		cfg.ClaudeModel = v
	}
	// Effort strings validate strictly, same rationale as the booleans: a typo
	// must fail loudly, not silently fall back to a default effort.
	for _, e := range []struct {
		name string
		dst  *string
	}{
		{"CLAUDE_EFFORT", &cfg.ClaudeEffort},
		{"CLAUDE_QUESTION_EFFORT", &cfg.ClaudeQuestionEffort},
	} {
		v := os.Getenv(e.name)
		if v == "" {
			continue
		}
		if !validEffort(v) {
			return cfg, fmt.Errorf("invalid %s %q (want low, medium, high, xhigh, or max)", e.name, v)
		}
		*e.dst = v
	}
	if v := os.Getenv("CLAUDE_MAX_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("invalid CLAUDE_MAX_TOKENS %q", v)
		}
		cfg.ClaudeMaxTokens = n
	}
	if v := os.Getenv("QUESTION_PREGEN_HOUR"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 23 {
			return cfg, fmt.Errorf("invalid QUESTION_PREGEN_HOUR %q (want 0-23)", v)
		}
		cfg.QuestionPregenHour = n
	}
	return cfg, nil
}

// validEffort reports whether s is one of the Claude API effort levels.
func validEffort(s string) bool {
	switch s {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}
