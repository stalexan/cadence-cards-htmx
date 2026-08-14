package pregen

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

type fakeBatchAI struct {
	calls    int
	requests []claude.BatchQuestionRequest
}

func (f *fakeBatchAI) GenerateQuestionBatch(_ context.Context, reqs []claude.BatchQuestionRequest) (map[int64]string, error) {
	f.calls++
	f.requests = reqs
	out := make(map[int64]string, len(reqs))
	for _, r := range reqs {
		out[r.ScheduleID] = "Q: " + r.Card.Front
	}
	return out, nil
}

func (f *fakeBatchAI) QuestionModel() string { return "fake-model" }

func newStore(t *testing.T) (*store.Store, int64, int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "Test", "t@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	topic, err := s.CreateTopic(ctx, u.ID, store.TopicParams{Name: "Spanish"})
	if err != nil {
		t.Fatal(err)
	}
	deck, err := s.CreateDeck(ctx, u.ID, store.DeckParams{Name: "Vocab", TopicID: topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, u.ID, deck.ID
}

func mkCard(t *testing.T, s *store.Store, userID, deckID int64, front string) store.Card {
	t.Helper()
	c, err := s.CreateCard(context.Background(), userID, store.CardParams{
		DeckID: deckID, Front: front, Back: "back", Priority: sm2.PriorityA,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunnerFiresOncePerDayAtConfiguredHour(t *testing.T) {
	s, userID, deckID := newStore(t)
	card := mkCard(t, s, userID, deckID, "hola")
	ai := &fakeBatchAI{}
	r := New(s, ai, 3)
	ctx := context.Background()

	day := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)

	// Before the configured hour: nothing happens.
	r.MaybeRun(ctx, day.Add(2*time.Hour))
	if ai.calls != 0 {
		t.Fatalf("fired before the configured hour (%d calls)", ai.calls)
	}

	// At the hour: one batch, question stored.
	r.MaybeRun(ctx, day.Add(3*time.Hour))
	if ai.calls != 1 {
		t.Fatalf("calls = %d, want 1", ai.calls)
	}
	schedID := card.ForwardSchedule().ID
	q, ok, err := s.TakeGeneratedQuestion(ctx, userID, schedID)
	if err != nil || !ok || q != "Q: hola" {
		t.Fatalf("stored question = %q, %v, %v", q, ok, err)
	}

	// Later the same day: no second run.
	r.MaybeRun(ctx, day.Add(9*time.Hour))
	if ai.calls != 1 {
		t.Errorf("re-ran within the same day (%d calls)", ai.calls)
	}
	// Next day: runs again.
	r.MaybeRun(ctx, day.Add(27*time.Hour))
	if ai.calls != 2 {
		t.Errorf("did not run the next day (%d calls)", ai.calls)
	}
}

func TestRunnerRespectsCap(t *testing.T) {
	s, userID, deckID := newStore(t)
	mkCard(t, s, userID, deckID, "uno")
	mkCard(t, s, userID, deckID, "dos")
	ai := &fakeBatchAI{}
	r := New(s, ai, 0)
	r.max = 1

	r.MaybeRun(context.Background(), time.Date(2026, 8, 14, 1, 0, 0, 0, time.Local))
	if ai.calls != 1 || len(ai.requests) != 1 {
		t.Fatalf("calls = %d, requests = %d, want one capped batch", ai.calls, len(ai.requests))
	}
}
