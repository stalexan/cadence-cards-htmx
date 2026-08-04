// Package claude wraps the official anthropic-sdk-go for the study-assistance
// features (port of web/src/lib/server/claude/).
package claude

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cadence-cards/internal/config"
)

// Message is one visible chat turn.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// TokenTracker accumulates usage across the process lifetime (port of the
// tokenTracker in client.ts).
type TokenTracker struct {
	mu           sync.Mutex
	inputTokens  int64
	outputTokens int64
	requests     int64
}

func (t *TokenTracker) record(in, out int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inputTokens += in
	t.outputTokens += out
	t.requests++
}

// Totals returns cumulative usage.
func (t *TokenTracker) Totals() (requests, inputTokens, outputTokens int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requests, t.inputTokens, t.outputTokens
}

// Client is the app-wide Claude client.
type Client struct {
	api       anthropic.Client
	model     string
	maxTokens int64
	Tracker   TokenTracker
}

// New builds a Client from configuration.
func New(cfg config.Config) *Client {
	return &Client{
		api:       anthropic.NewClient(option.WithAPIKey(cfg.ClaudeAPIKey)),
		model:     cfg.ClaudeModel,
		maxTokens: int64(cfg.ClaudeMaxTokens),
	}
}

// generateText sends one request and returns the first text block, mirroring
// generateText in client.ts (adaptive thinking, effort high, no temperature).
func (c *Client) generateText(ctx context.Context, system string, msgs []Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: c.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortHigh,
		},
	}
	for _, m := range msgs {
		block := anthropic.NewTextBlock(m.Content)
		if m.Role == "assistant" {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(block))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(block))
		}
	}

	start := time.Now()
	resp, err := c.api.Messages.New(ctx, params)
	if err != nil {
		slog.Error("claude request failed", "model", c.model, "error", err)
		return "", err
	}

	c.Tracker.record(resp.Usage.InputTokens, resp.Usage.OutputTokens)
	slog.Info("claude request",
		"model", c.model,
		"durationMs", time.Since(start).Milliseconds(),
		"stopReason", string(resp.StopReason),
		"inputTokens", resp.Usage.InputTokens,
		"outputTokens", resp.Usage.OutputTokens,
	)

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", nil
}
