package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cadence-cards/internal/claude"
)

// A pre-generated question is served without any AI call (the stub errors, so
// touching it would render an error bubble) and is consumed on serve: the
// second request falls back to the live path.
func TestPreGeneratedQuestionServedWithoutAICall(t *testing.T) {
	app := newTestApp(t, stubAI{err: claude.ErrOverloaded})
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()
	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")

	if err := app.store.UpsertGeneratedQuestion(ctx, u.ID, sched.ID, "Pre-baked: what is hola?", "claude-haiku-4-5"); err != nil {
		t.Fatal(err)
	}

	w := app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Pre-baked: what is hola?") {
		t.Fatalf("pre-generated question not served: %d, %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "overloaded") {
		t.Error("AI was consulted despite a stored question")
	}
	// It still opened a conversation seeded with the question.
	conv, msgs, err := app.store.LatestConversationForSchedule(ctx, u.ID, sched.ID)
	if err != nil || conv.ID == 0 || len(msgs) != 1 || msgs[0].Content != "Pre-baked: what is hola?" {
		t.Errorf("conversation after pre-generated serve = %+v, %+v, %v", conv, msgs, err)
	}

	// Consumed: the next request hits the (erroring) live path.
	w = app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "overloaded") {
		t.Errorf("second request should fall back to the live path: %d, %q", w.Code, w.Body.String())
	}
}
