package store

import (
	"strings"
	"testing"

	"cadence-cards/internal/sm2"
)

// drifted is an easiness the unrounded SM-2 formula produces for "2.8": three
// perfect recalls from 2.5, in float64.
const drifted = 2.8000000000000003

// TestImportRoundsEasiness pins the import boundary: a YAML file written by the
// Svelte app carries raw float64 easiness values, and they must land in the
// database rounded like everything the Go app computes itself.
func TestImportRoundsEasiness(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, true)

	if _, err := s.ImportCards(ctx, userID, deckID, []ImportCardParams{{
		Front: "drifted", Back: "b", Priority: sm2.PriorityA,
		Forward: sm2.State{RepCount: 3, Easiness: drifted, Interval: 12},
		Reverse: &sm2.State{RepCount: 3, Easiness: drifted, Interval: 12},
	}}); err != nil {
		t.Fatalf("ImportCards: %v", err)
	}

	cards, _, err := s.ListCards(ctx, userID, CardListParams{})
	if err != nil || len(cards) != 1 {
		t.Fatalf("ListCards: %d cards (err %v)", len(cards), err)
	}
	for _, sc := range cards[0].Schedules {
		if sc.Easiness != 2.8 {
			t.Errorf("imported easiness (reversed=%v) = %v, want 2.8", sc.IsReversed, sc.Easiness)
		}
	}
}

// TestRoundEasinessMigration checks the SQL of 0005 against rows written before
// the rounding existed. The migration has already run on this store, so this
// dirties a row the way an old build would have and re-applies it — which also
// pins that the statements are idempotent.
func TestRoundEasinessMigration(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "legacy", sm2.PriorityA)

	if _, err := s.db.Exec(
		`UPDATE schedules SET easiness = ?, prev_easiness = ? WHERE card_id = ?`,
		drifted, drifted, card.ID); err != nil {
		t.Fatalf("dirty the row: %v", err)
	}

	src, err := migrationsFS.ReadFile("migrations/0005_round_easiness.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for range 2 {
		if _, err := s.db.Exec(string(src)); err != nil {
			t.Fatalf("apply migration: %v", err)
		}
		var easiness, prev float64
		if err := s.db.QueryRow(
			`SELECT easiness, prev_easiness FROM schedules WHERE card_id = ?`,
			card.ID).Scan(&easiness, &prev); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if easiness != 2.8 || prev != 2.8 {
			t.Errorf("after migration: easiness = %v, prev_easiness = %v, want 2.8 and 2.8", easiness, prev)
		}
	}
}

// TestRoundEasinessMigrationLeavesNullPrevAlone guards the NULL branch: a
// schedule with no regrade baseline must keep prev_easiness NULL, since
// "prev_easiness IS NOT NULL" is what marks a baseline as existing.
func TestRoundEasinessMigrationLeavesNullPrevAlone(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "never regraded", sm2.PriorityA)

	src, err := migrationsFS.ReadFile("migrations/0005_round_easiness.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := s.db.Exec(string(src)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	var prev any
	if err := s.db.QueryRow(
		`SELECT prev_easiness FROM schedules WHERE card_id = ?`, card.ID).Scan(&prev); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if prev != nil {
		t.Errorf("prev_easiness = %v, want NULL", prev)
	}
}

// TestGradedEasinessIsStoredRounded walks the write path a real review takes.
func TestGradedEasinessIsStoredRounded(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "studied", sm2.PriorityA)
	sc := card.ForwardSchedule()

	// Three perfect recalls: 2.5 -> 2.6 -> 2.7 -> 2.8.
	for range 3 {
		graded, err := s.RecordReview(ctx, userID, sc.ID, sm2.GradeCorrectPerfectRecall, sc.Version, now)
		if err != nil {
			t.Fatalf("RecordReview: %v", err)
		}
		sc = &graded
	}
	if sc.Easiness != 2.8 {
		t.Errorf("stored easiness = %v, want 2.8", sc.Easiness)
	}
	// The stored text form is what an export or a manual query shows.
	var raw string
	if err := s.db.QueryRow(
		`SELECT CAST(easiness AS TEXT) FROM schedules WHERE id = ?`, sc.ID).Scan(&raw); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(raw, "00000") {
		t.Errorf("stored easiness renders as %q", raw)
	}
}
