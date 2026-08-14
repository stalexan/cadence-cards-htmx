package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

// The question fragment opens a server-side conversation seeded with the
// generated question and hands its ID to the browser via the OOB input.
func TestStudyQuestionCreatesConversation(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()
	u, _, _ := app.store.GetUserByEmail(context.Background(), "t@example.com")

	w := app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="chat-conversation"`) {
		t.Fatalf("question = %d, body %q", w.Code, w.Body.String())
	}

	conv, msgs, err := app.store.LatestConversationForSchedule(context.Background(), u.ID, sched.ID)
	if err != nil || conv.ID == 0 {
		t.Fatalf("no conversation stored: %+v, %v", conv, err)
	}
	if len(msgs) != 1 || msgs[0].Role != "assistant" || msgs[0].Content != "What does 'hola' mean?" {
		t.Errorf("stored transcript = %+v", msgs)
	}

	// Chat with the conversation ID appends both turns server-side.
	w = app.do("POST", "/study/1/chat", url.Values{
		"scheduleId": {itoa(sched.ID)}, "conversationId": {itoa(conv.ID)}, "message": {"hello"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("chat = %d", w.Code)
	}
	if _, msgs, _ = app.store.GetConversationMessages(context.Background(), u.ID, conv.ID); len(msgs) != 3 {
		t.Errorf("transcript after chat = %d messages, want 3", len(msgs))
	}
}

// A conversationId belonging to a different card (or user) is a 404, and the
// transcript it names is never replayed to Claude.
func TestStudyChatRejectsForeignConversation(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()
	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")

	// A conversation for a *different* schedule of the same user.
	other, err := app.store.CreateCard(ctx, u.ID, store.CardParams{
		DeckID: card.DeckID, Front: "adios", Back: "goodbye", Priority: sm2.PriorityA,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherSched := other.ForwardSchedule().ID
	foreignConv, err := app.store.CreateConversation(ctx, u.ID, 1, &otherSched, nil)
	if err != nil {
		t.Fatal(err)
	}

	w := app.do("POST", "/study/1/chat", url.Values{
		"scheduleId": {itoa(sched.ID)}, "conversationId": {itoa(foreignConv)}, "message": {"hi"},
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("mismatched conversation = %d, want 404", w.Code)
	}

	// Another user's conversationId is equally invisible.
	app.login("u2@example.com")
	topic2, err := app.store.CreateTopic(ctx, 2, store.TopicParams{Name: "Spanish"})
	if err != nil {
		t.Fatal(err)
	}
	w = app.do("POST", fmt.Sprintf("/chat/%d/message", topic2.ID), url.Values{
		"conversationId": {itoa(foreignConv)}, "message": {"hi"},
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user conversation = %d, want 404", w.Code)
	}
}

// A scheduleId in the session URL re-serves the same card with its stored
// transcript (no pending stub, no new AI call), and /next pushes the session
// URL — including the served card and every repeated deckIds value — via the
// HX-Push-Url header.
func TestStudyNextResumesCurrentCard(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()
	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")

	// Build a transcript for the card.
	if w := app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}}); w.Code != http.StatusOK {
		t.Fatal("question failed")
	}
	conv, _, _ := app.store.LatestConversationForSchedule(ctx, u.ID, sched.ID)
	if err := app.store.AppendChatMessages(ctx, u.ID, conv.ID, []store.ChatMessage{
		{Role: "user", Content: "my answer was hello"},
	}); err != nil {
		t.Fatal(err)
	}

	resumeURL := fmt.Sprintf("/study/1/next?deckIds=1&deckIds=9&total=1&completed=0&scheduleId=%d", sched.ID)
	w := app.do("GET", resumeURL, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("resume = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "hola") || !strings.Contains(body, "my answer was hello") {
		t.Errorf("resumed card missing card or transcript: %q", body)
	}
	if strings.Contains(body, "chat-pending") {
		t.Error("resumed card should not re-request a question")
	}
	if !strings.Contains(body, fmt.Sprintf(`value="%d"`, conv.ID)) {
		t.Error("resumed composer missing the conversation ID")
	}
	push := w.Header().Get("HX-Push-Url")
	for _, want := range []string{fmt.Sprintf("scheduleId=%d", sched.ID), "deckIds=1", "deckIds=9"} {
		if !strings.Contains(push, want) {
			t.Errorf("HX-Push-Url %q missing %q", push, want)
		}
	}
}

// A stale scheduleId (card graded, so no longer due) falls through to a fresh
// pick — here, with nothing else due, session completion.
func TestStudyNextStaleScheduleFallsBack(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()

	w := app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"CORRECT_PERFECT_RECALL"}, "version": {"0"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("grade = %d", w.Code)
	}

	w = app.do("GET", fmt.Sprintf("/study/1/next?deckIds=1&total=1&completed=1&scheduleId=%d", sched.ID), nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Study Session Complete") {
		t.Errorf("stale resume = %d, want session complete, body %q", w.Code, w.Body.String())
	}
	if push := w.Header().Get("HX-Push-Url"); strings.Contains(push, "scheduleId") {
		t.Errorf("completion push %q should not carry a scheduleId", push)
	}
}
