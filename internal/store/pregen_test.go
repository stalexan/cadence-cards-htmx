package store

import (
	"errors"
	"testing"
	"time"

	"cadence-cards/internal/sm2"
)

func TestGeneratedQuestionLifecycle(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "hola", "A")
	schedID := card.ForwardSchedule().ID

	if err := s.UpsertGeneratedQuestion(ctx, userID, schedID, "What does hola mean?", "claude-haiku-4-5"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Refresh overwrites.
	if err := s.UpsertGeneratedQuestion(ctx, userID, schedID, "¿Qué significa hola?", "claude-haiku-4-5"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	q, ok, err := s.TakeGeneratedQuestion(ctx, userID, schedID)
	if err != nil || !ok || q != "¿Qué significa hola?" {
		t.Fatalf("take = %q, %v, %v", q, ok, err)
	}
	// Consumed on serve: the second take misses.
	if _, ok, err := s.TakeGeneratedQuestion(ctx, userID, schedID); err != nil || ok {
		t.Errorf("second take = %v, %v — want a miss", ok, err)
	}

	// Foreign user can neither store nor take.
	other, err := s.CreateUser(ctx, "Other", "other@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGeneratedQuestion(ctx, other.ID, schedID, "q", "m"); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign upsert = %v, want ErrNotFound", err)
	}
	if err := s.UpsertGeneratedQuestion(ctx, userID, schedID, "q", "m"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.TakeGeneratedQuestion(ctx, other.ID, schedID); err != nil || ok {
		t.Errorf("foreign take = %v, %v — want a miss", ok, err)
	}
}

func TestGeneratedQuestionInvalidation(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "hola", "A")
	schedID := card.ForwardSchedule().ID

	store := func() {
		t.Helper()
		if err := s.UpsertGeneratedQuestion(ctx, userID, schedID, "q", "m"); err != nil {
			t.Fatal(err)
		}
	}
	gone := func(context string) {
		t.Helper()
		if _, ok, err := s.TakeGeneratedQuestion(ctx, userID, schedID); err != nil || ok {
			t.Errorf("%s: question survived (ok=%v, err=%v)", context, ok, err)
		}
	}

	// Editing the card invalidates (the question was built from old content).
	store()
	card2, err := s.UpdateCard(ctx, userID, card.ID, card.Version, CardParams{
		DeckID: deckID, Front: "hola!", Back: card.Back, Priority: sm2.PriorityA,
	})
	if err != nil {
		t.Fatal(err)
	}
	gone("card edit")

	// Reset Progress invalidates.
	store()
	if _, err := s.ResetProgress(ctx, userID, schedID); err != nil {
		t.Fatal(err)
	}
	gone("reset progress")

	// Deleting the card cascades.
	store()
	if err := s.DeleteCard(ctx, userID, card2.ID); err != nil {
		t.Fatal(err)
	}
	gone("card delete")
}

func TestSchedulesDueWithin(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	fresh := mkCard(t, s, userID, deckID, "hola", "A")
	graded := mkCard(t, s, userID, deckID, "adios", "A")

	// Grade one card now: interval 1 -> due tomorrow, i.e. inside a 24h
	// horizon but not due right now.
	gradedSched := graded.ForwardSchedule().ID
	if _, err := s.RecordReview(ctx, userID, gradedSched, sm2.GradeCorrectPerfectRecall, 0, now); err != nil {
		t.Fatal(err)
	}

	items, err := s.SchedulesDueWithin(ctx, userID, topicID, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, it := range items {
		ids[it.ScheduleID] = true
	}
	if !ids[fresh.ForwardSchedule().ID] || !ids[gradedSched] {
		t.Errorf("due-within-24h = %v, want both the never-studied and the due-tomorrow schedule", ids)
	}

	// A schedule that already holds a question is excluded from the next run.
	if err := s.UpsertGeneratedQuestion(ctx, userID, gradedSched, "q", "m"); err != nil {
		t.Fatal(err)
	}
	items, err = s.SchedulesDueWithin(ctx, userID, topicID, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ScheduleID == gradedSched {
			t.Error("schedule with a stored question was offered again")
		}
	}
}
