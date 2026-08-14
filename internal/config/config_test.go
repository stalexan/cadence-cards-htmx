package config

import (
	"strings"
	"testing"
)

// clearClaudeEnv blanks the Claude keys so ambient developer environment
// variables can't leak into assertions (Load treats "" as unset).
func clearClaudeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CLAUDE_MODEL", "CLAUDE_EFFORT", "CLAUDE_QUESTION_MODEL", "CLAUDE_QUESTION_EFFORT", "CLAUDE_MAX_TOKENS"} {
		t.Setenv(k, "")
	}
}

func TestLoadClaudeDefaults(t *testing.T) {
	clearClaudeEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeModel != "claude-opus-5" || cfg.ClaudeEffort != "high" {
		t.Errorf("defaults = model %q effort %q", cfg.ClaudeModel, cfg.ClaudeEffort)
	}
	if cfg.ClaudeQuestionModel != "" || cfg.ClaudeQuestionEffort != "" {
		t.Errorf("question routing should default to empty (= main model/effort), got %q/%q",
			cfg.ClaudeQuestionModel, cfg.ClaudeQuestionEffort)
	}
}

func TestLoadClaudeQuestionRouting(t *testing.T) {
	clearClaudeEnv(t)
	t.Setenv("CLAUDE_EFFORT", "xhigh")
	t.Setenv("CLAUDE_QUESTION_MODEL", "claude-haiku-4-5")
	t.Setenv("CLAUDE_QUESTION_EFFORT", "low")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeEffort != "xhigh" || cfg.ClaudeQuestionModel != "claude-haiku-4-5" || cfg.ClaudeQuestionEffort != "low" {
		t.Errorf("routing = %q/%q/%q", cfg.ClaudeEffort, cfg.ClaudeQuestionModel, cfg.ClaudeQuestionEffort)
	}
}

func TestLoadRejectsInvalidEffort(t *testing.T) {
	for _, key := range []string{"CLAUDE_EFFORT", "CLAUDE_QUESTION_EFFORT"} {
		t.Run(key, func(t *testing.T) {
			clearClaudeEnv(t)
			t.Setenv(key, "extreme")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v, want mention of %s", err, key)
			}
		})
	}
}
