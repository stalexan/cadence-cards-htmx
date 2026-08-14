package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cadence-cards/internal/claude"
)

// Each AI failure class renders its own bubble copy, always at HTTP 200 — the
// question and chat fragments are not on the htmx 409/422 error-swap
// whitelist, so an error status would be silently discarded by the client.
func TestAIErrorBubblesPerFailureClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"rate-limited", claude.ErrRateLimited, "rate-limited"},
		{"overloaded", claude.ErrOverloaded, "overloaded"},
		{"bad-auth", claude.ErrBadAuth, "API key was rejected"},
		{"refused", claude.ErrRefused, "declined this request"},
		{"generic", fmt.Errorf("boom"), "something went wrong"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := newTestApp(t, stubAI{err: c.err})
			app.login("t@example.com")
			card := app.seed(false)
			sched := card.ForwardSchedule()

			w := app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("question = %d, want copy %q in body %q", w.Code, c.want, w.Body.String())
			}
			w = app.do("POST", "/study/1/chat", url.Values{
				"scheduleId": {itoa(sched.ID)}, "message": {"hi"}, "history": {"[]"},
			})
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("study chat = %d, want copy %q", w.Code, c.want)
			}
			w = app.do("POST", "/chat/1/message", url.Values{"message": {"hi"}})
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("topic chat = %d, want copy %q", w.Code, c.want)
			}
		})
	}
}

// Studying must never be blocked by AI unavailability: the card renders, the
// failed question is an error bubble with the card's own prompt, and grading
// still works.
func TestGradingWorksWhenAIUnavailable(t *testing.T) {
	app := newTestApp(t, stubAI{err: claude.ErrOverloaded})
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()

	w := app.do("GET", "/study/1/next?deckIds=1&total=1&completed=0", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hola") ||
		!strings.Contains(w.Body.String(), "grade-area") {
		t.Fatalf("next = %d, card and grade area must render before any AI call", w.Code)
	}

	w = app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "You can practice with the following prompt: hola") {
		t.Errorf("failed question should fall back to the card prompt, body %q", w.Body.String())
	}

	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"CORRECT_PERFECT_RECALL"}, "version": {"0"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "grade-on-green") {
		t.Errorf("grade with AI down = %d, body %q", w.Code, w.Body.String())
	}
}
