// Package config loads application configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all runtime configuration.
type Config struct {
	Port   int    // PORT, default 3000
	DBPath string // DB_PATH, default /data/cadence.db

	// Claude API
	ClaudeAPIKey    string // CLAUDE_API_KEY
	ClaudeModel     string // CLAUDE_MODEL, default claude-opus-4-8
	ClaudeMaxTokens int    // CLAUDE_MAX_TOKENS, fallback 1000 (matches client.ts)

	EnablePublicRegistration bool // ENABLE_PUBLIC_REGISTRATION
	DisableRateLimiting      bool // DISABLE_RATE_LIMITING
	CookieSecure             bool // COOKIE_SECURE, default true (TLS terminates at nginx)

	AppVersion string // APP_VERSION, informational
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		Port:                     3000,
		DBPath:                   "/data/cadence.db",
		ClaudeAPIKey:             os.Getenv("CLAUDE_API_KEY"),
		ClaudeModel:              "claude-opus-4-8",
		ClaudeMaxTokens:          1000,
		EnablePublicRegistration: os.Getenv("ENABLE_PUBLIC_REGISTRATION") == "true",
		DisableRateLimiting:      os.Getenv("DISABLE_RATE_LIMITING") == "true",
		CookieSecure:             true,
		AppVersion:               os.Getenv("APP_VERSION"),
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
	if v := os.Getenv("COOKIE_SECURE"); v != "" {
		cfg.CookieSecure = v == "true"
	}
	return cfg, nil
}
