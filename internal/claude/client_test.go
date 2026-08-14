package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestClassifyAPIError(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{429, ErrRateLimited},
		{401, ErrBadAuth},
		{403, ErrBadAuth},
		{500, ErrOverloaded},
		{529, ErrOverloaded},
	}
	for _, c := range cases {
		err := classifyAPIError(fmt.Errorf("request failed: %w", &anthropic.Error{StatusCode: c.status}))
		if !errors.Is(err, c.want) {
			t.Errorf("status %d classified as %v, want %v", c.status, err, c.want)
		}
	}

	// Other API statuses and non-API errors pass through unclassified.
	if err := classifyAPIError(&anthropic.Error{StatusCode: 404}); errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrOverloaded) || errors.Is(err, ErrBadAuth) {
		t.Errorf("404 should not classify, got %v", err)
	}
	plain := errors.New("dial tcp: connection refused")
	if err := classifyAPIError(plain); err != plain {
		t.Errorf("non-API error changed: %v", err)
	}
}

// Wire shapes for asserting on the marshalled request JSON.
type wireBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireParams struct {
	System   []wireBlock   `json:"system"`
	Messages []wireMessage `json:"messages"`
}

func marshalParams(t *testing.T, params anthropic.MessageNewParams) wireParams {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var w wireParams
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// The question request must carry cache_control on the system block and on the
// assistant ack (the end of the topic-stable prefix), and nowhere else — the
// per-card prompt after the breakpoint must stay uncached.
func TestQuestionParamsCacheBreakpoints(t *testing.T) {
	cfg := NewTopicConfig("Spanish", nil, nil, nil, nil, nil, nil)
	system, turns := buildQuestionTurns(cfg, CardContent{Front: "hola", Back: "hello"})
	w := marshalParams(t, buildParams(reqOptions{model: "m", effort: anthropic.OutputConfigEffortLow, maxTokens: 100}, system, turns))

	if len(w.System) != 1 || w.System[0].CacheControl == nil {
		t.Errorf("system block missing cache_control: %+v", w.System)
	}
	if len(w.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(w.Messages))
	}
	if w.Messages[1].Role != "assistant" || w.Messages[1].Content[0].CacheControl == nil {
		t.Errorf("assistant ack should carry the breakpoint: %+v", w.Messages[1])
	}
	for _, i := range []int{0, 2} {
		for _, b := range w.Messages[i].Content {
			if b.CacheControl != nil {
				t.Errorf("message %d block %q should not carry cache_control", i, b.Text[:min(40, len(b.Text))])
			}
		}
	}
}

// historyTurns marks only the final message, so each request re-reads the
// conversation prefix the previous request wrote.
func TestHistoryTurnsMarksLastMessage(t *testing.T) {
	turns := historyTurns([]Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	})
	if len(turns) != 3 {
		t.Fatalf("turns = %d", len(turns))
	}
	for i, tn := range turns {
		want := i == 2
		if tn.parts[0].cache != want {
			t.Errorf("turn %d cache = %v, want %v", i, tn.parts[0].cache, want)
		}
	}
}
