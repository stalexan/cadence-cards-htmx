// Package claude wraps the official anthropic-sdk-go for the study-assistance
// features (port of web/src/lib/server/claude/).
package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cadence-cards/internal/config"
)

// Sentinel errors for AI failures. Handlers match these with errors.Is to show
// distinct user-facing copy (rate-limited vs overloaded vs a broken API key).
var (
	// ErrRefused reports that Claude's safety classifiers declined the request.
	// It arrives as a successful response with no text block, so handlers must
	// treat it like any other AI failure rather than rendering an empty reply.
	ErrRefused = errors.New("claude declined the request")

	// ErrRateLimited is a 429 that survived the SDK's automatic retries (which
	// honor the retry-after header before each attempt).
	ErrRateLimited = errors.New("claude API rate limit exceeded")

	// ErrOverloaded is an Anthropic-side 529/5xx that survived retries.
	ErrOverloaded = errors.New("anthropic API overloaded")

	// ErrBadAuth is a 401/403: invalid key, revoked key, or out of credit.
	// Retrying won't help until the operator fixes the key.
	ErrBadAuth = errors.New("claude API key rejected")
)

// Message is one visible chat turn.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// part is one text block inside a turn. cache marks a prompt-caching
// breakpoint: everything up to and including this block is the stable prefix
// the next request re-reads.
type part struct {
	text  string
	cache bool
}

// turn is one message built from explicit blocks, so prompt builders can put
// cache breakpoints at stable-prefix boundaries.
type turn struct {
	role  string // "user" | "assistant"
	parts []part
}

// historyTurns converts visible chat history into turns. The final message
// carries a cache breakpoint so each request re-reads the conversation prefix
// the previous request wrote (the standard incremental-caching pattern; a
// breakpoint below the model's minimum cacheable prefix is a harmless no-op).
func historyTurns(msgs []Message) []turn {
	out := make([]turn, 0, len(msgs))
	for i, m := range msgs {
		out = append(out, turn{role: m.Role, parts: []part{{text: m.Content, cache: i == len(msgs)-1}}})
	}
	return out
}

// TokenTracker accumulates usage across the process lifetime (port of the
// tokenTracker in client.ts).
type TokenTracker struct {
	mu                  sync.Mutex
	inputTokens         int64
	outputTokens        int64
	cacheCreationTokens int64
	cacheReadTokens     int64
	requests            int64
}

func (t *TokenTracker) record(in, out, cacheWrite, cacheRead int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inputTokens += in
	t.outputTokens += out
	t.cacheCreationTokens += cacheWrite
	t.cacheReadTokens += cacheRead
	t.requests++
}

// Totals returns cumulative usage.
func (t *TokenTracker) Totals() (requests, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requests, t.inputTokens, t.outputTokens, t.cacheCreationTokens, t.cacheReadTokens
}

// reqOptions is the per-operation request shape. Question generation is a
// small extraction task and can run on a cheaper model / lower effort than the
// tutoring conversation (CLAUDE_QUESTION_MODEL / CLAUDE_QUESTION_EFFORT).
type reqOptions struct {
	model     string
	effort    anthropic.OutputConfigEffort
	maxTokens int64
}

// Client is the app-wide Claude client.
type Client struct {
	api          anthropic.Client
	chatOpts     reqOptions
	questionOpts reqOptions
	Tracker      TokenTracker
}

// New builds a Client from configuration.
func New(cfg config.Config) *Client {
	chat := reqOptions{
		model:     cfg.ClaudeModel,
		effort:    effortValue(cfg.ClaudeEffort),
		maxTokens: int64(cfg.ClaudeMaxTokens),
	}
	question := chat
	if cfg.ClaudeQuestionModel != "" {
		question.model = cfg.ClaudeQuestionModel
	}
	if cfg.ClaudeQuestionEffort != "" {
		question.effort = effortValue(cfg.ClaudeQuestionEffort)
	}
	return &Client{
		// 2 is the SDK default, made explicit: it retries 429 (waiting out
		// retry-after first) and 5xx/connection errors with jittered backoff.
		api:          anthropic.NewClient(option.WithAPIKey(cfg.ClaudeAPIKey), option.WithMaxRetries(2)),
		chatOpts:     chat,
		questionOpts: question,
	}
}

// effortValue maps a config effort string to the SDK constant. config.Load
// validates the set, so the fallback is defensive only.
func effortValue(s string) anthropic.OutputConfigEffort {
	switch s {
	case "low":
		return anthropic.OutputConfigEffortLow
	case "medium":
		return anthropic.OutputConfigEffortMedium
	case "xhigh":
		return anthropic.OutputConfigEffortXhigh
	case "max":
		return anthropic.OutputConfigEffortMax
	default:
		return anthropic.OutputConfigEffortHigh
	}
}

// buildParams assembles the request (adaptive thinking, per-op effort, no
// temperature). Blocks marked cache get an ephemeral (5-minute) cache_control
// breakpoint; the system block is always marked — it usually sits under the
// model's minimum cacheable prefix on its own (a no-op), but message-block
// breakpoints cache the system prompt with them, and marking costs nothing.
func buildParams(opts reqOptions, system string, turns []turn) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.model),
		MaxTokens: opts.maxTokens,
		System: []anthropic.TextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: opts.effort,
		},
	}
	for _, t := range turns {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(t.parts))
		for _, p := range t.parts {
			tb := anthropic.TextBlockParam{Text: p.text}
			if p.cache {
				tb.CacheControl = anthropic.NewCacheControlEphemeralParam()
			}
			blocks = append(blocks, anthropic.ContentBlockParamUnion{OfText: &tb})
		}
		if t.role == "assistant" {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(blocks...))
		}
	}
	return params
}

// classifyAPIError wraps API failures with the matching sentinel so handlers
// can show distinct copy. Non-API errors (context deadline, transport) pass
// through unchanged.
func classifyAPIError(err error) error {
	var apierr *anthropic.Error
	if !errors.As(err, &apierr) {
		return err
	}
	switch {
	case apierr.StatusCode == 429:
		return fmt.Errorf("%w: %v", ErrRateLimited, err)
	case apierr.StatusCode == 401, apierr.StatusCode == 403:
		return fmt.Errorf("%w: %v", ErrBadAuth, err)
	case apierr.StatusCode >= 500: // includes 529 overloaded_error
		return fmt.Errorf("%w: %v", ErrOverloaded, err)
	}
	return err
}

// generateText sends one request and returns the first text block, mirroring
// generateText in client.ts.
func (c *Client) generateText(ctx context.Context, opts reqOptions, system string, turns []turn) (string, error) {
	// Budget: one slow generation plus up to two SDK retries (a 429 retry
	// waits out retry-after before re-sending). The context is the hard
	// user-facing cap — the SDK gives up as soon as it expires.
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	params := buildParams(opts, system, turns)

	start := time.Now()
	resp, err := c.api.Messages.New(ctx, params)
	if err != nil {
		err = classifyAPIError(err)
		slog.Error("claude request failed", "model", opts.model, "error", err)
		return "", err
	}

	c.Tracker.record(resp.Usage.InputTokens, resp.Usage.OutputTokens,
		resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens)
	slog.Info("claude request",
		"model", opts.model,
		"durationMs", time.Since(start).Milliseconds(),
		"stopReason", string(resp.StopReason),
		"inputTokens", resp.Usage.InputTokens,
		"outputTokens", resp.Usage.OutputTokens,
		"cacheCreationInputTokens", resp.Usage.CacheCreationInputTokens,
		"cacheReadInputTokens", resp.Usage.CacheReadInputTokens,
	)

	// A safety-classifier refusal is an HTTP 200 with no text block, so it has
	// to be caught here or it renders as an empty chat bubble.
	if resp.StopReason == anthropic.StopReasonRefusal {
		slog.Warn("claude declined the request", "model", opts.model)
		return "", ErrRefused
	}

	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			return block.Text, nil
		}
	}
	// No text for any other reason (e.g. thinking consumed the whole
	// max_tokens budget) must be an error, or the handlers render an empty
	// assistant bubble and store an empty turn in the chat history.
	slog.Warn("claude returned no text", "model", opts.model, "stopReason", string(resp.StopReason))
	return "", fmt.Errorf("claude returned no text (stop reason %q)", resp.StopReason)
}
