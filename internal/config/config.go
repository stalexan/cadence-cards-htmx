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
	ClaudeMaxTokens int    // CLAUDE_MAX_TOKENS, fallback 16000 (caps thinking + reply)

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
		Port:            3000,
		DBPath:          "/data/cadence.db",
		ClaudeAPIKey:    os.Getenv("CLAUDE_API_KEY"),
		ClaudeModel:     "claude-opus-5",
		ClaudeMaxTokens: 16000,
		CookieSecure:    true,
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
	if v := os.Getenv("CLAUDE_MAX_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("invalid CLAUDE_MAX_TOKENS %q", v)
		}
		cfg.ClaudeMaxTokens = n
	}
	return cfg, nil
}
