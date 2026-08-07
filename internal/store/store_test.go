package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"cadence-cards/internal/sm2"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seed creates a user, topic, and deck; returns their IDs.
func seed(t *testing.T, s *Store, bidirectional bool) (userID, topicID, deckID int64) {
	t.Helper()
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	topic, err := s.CreateTopic(ctx, u.ID, TopicParams{Name: "Spanish"})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	deck, err := s.CreateDeck(ctx, u.ID, DeckParams{Name: "Vocabulary", TopicID: topic.ID, IsBidirectional: bidirectional})
	if err != nil {
		t.Fatalf("CreateDeck: %v", err)
	}
	return u.ID, topic.ID, deck.ID
}

func mkCard(t *testing.T, s *Store, userID, deckID int64, front string, priority sm2.Priority, tags ...string) Card {
	t.Helper()
	c, err := s.CreateCard(ctx, userID, CardParams{
		DeckID: deckID, Front: front, Back: "back of " + front, Priority: priority, Tags: tags,
	})
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	return c
}

func TestUserCRUDAndDuplicates(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Sean", "sean@example.com", "h1")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.CreateUser(ctx, "Dup", "sean@example.com", "h2"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate email: got %v, want ErrDuplicate", err)
	}
	got, hash, err := s.GetUserByEmail(ctx, "sean@example.com")
	if err != nil || got.ID != u.ID || hash != "h1" {
		t.Errorf("GetUserByEmail = (%+v, %q, %v)", got, hash, err)
	}
	if _, _, err := s.GetUserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing email: got %v, want ErrNotFound", err)
	}
}

func TestTopicUniquePerUser(t *testing.T) {
	s := newTestStore(t)
	userID, _, _ := seed(t, s, false)
	if _, err := s.CreateTopic(ctx, userID, TopicParams{Name: "Spanish"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate topic name: got %v, want ErrDuplicate", err)
	}
	// Same name under a different user is fine.
	u2, err := s.CreateUser(ctx, "Other", "other@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTopic(ctx, u2.ID, TopicParams{Name: "Spanish"}); err != nil {
		t.Errorf("same name other user: %v", err)
	}
}

func TestDeckOwnershipAndDuplicate(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, _ := seed(t, s, false)
	if _, err := s.CreateDeck(ctx, userID, DeckParams{Name: "Vocabulary", TopicID: topicID}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate deck name: got %v, want ErrDuplicate", err)
	}
	// Creating a deck under someone else's topic fails ownership.
	u2, _ := s.CreateUser(ctx, "Other", "other2@example.com", "h")
	if _, err := s.CreateDeck(ctx, u2.ID, DeckParams{Name: "X", TopicID: topicID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user deck create: got %v, want ErrNotFound", err)
	}
}

func TestCardCreateSchedules(t *testing.T) {
	s := newTestStore(t)

	t.Run("unidirectional deck creates one forward schedule", func(t *testing.T) {
		userID, _, deckID := seed(t, s, false)
		c := mkCard(t, s, userID, deckID, "hola", sm2.PriorityA)
		if len(c.Schedules) != 1 || c.Schedules[0].IsReversed {
			t.Errorf("schedules = %+v, want one forward", c.Schedules)
		}
		fwd := c.ForwardSchedule()
		if fwd.Easiness != 2.5 || fwd.Interval != 1 || fwd.RepCount != 0 || fwd.Grade != nil || fwd.LastSeen != nil {
			t.Errorf("initial state wrong: %+v", fwd)
		}
	})

	t.Run("bidirectional deck creates forward and reverse", func(t *testing.T) {
		st := newTestStore(t)
		userID, _, deckID := seed(t, st, true)
		c := mkCard(t, st, userID, deckID, "hola", sm2.PriorityA)
		if len(c.Schedules) != 2 || c.ForwardSchedule() == nil || c.ReverseSchedule() == nil {
			t.Errorf("schedules = %+v, want forward+reverse", c.Schedules)
		}
	})
}

func TestBidirectionalBackfill(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	c1 := mkCard(t, s, userID, deckID, "uno", sm2.PriorityA)
	c2 := mkCard(t, s, userID, deckID, "dos", sm2.PriorityB)

	// Flip bidirectional on -> reverse schedules back-filled exactly once.
	p := DeckParams{Name: "Vocabulary", TopicID: topicID, IsBidirectional: true}
	if _, err := s.UpdateDeck(ctx, userID, deckID, p); err != nil {
		t.Fatalf("UpdateDeck: %v", err)
	}
	for _, id := range []int64{c1.ID, c2.ID} {
		c, err := s.GetCard(ctx, userID, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Schedules) != 2 {
			t.Errorf("card %d schedules = %d, want 2", id, len(c.Schedules))
		}
	}

	// Flip off and on again: no duplicate reverse schedules.
	p.IsBidirectional = false
	if _, err := s.UpdateDeck(ctx, userID, deckID, p); err != nil {
		t.Fatal(err)
	}
	p.IsBidirectional = true
	if _, err := s.UpdateDeck(ctx, userID, deckID, p); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetCard(ctx, userID, c1.ID)
	if len(c.Schedules) != 2 {
		t.Errorf("after re-flip schedules = %d, want 2 (no dupes)", len(c.Schedules))
	}
}

func TestCardOptimisticLocking(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	c := mkCard(t, s, userID, deckID, "hola", sm2.PriorityA, "greeting")

	upd := CardParams{DeckID: deckID, Front: "hola!", Back: "hello", Priority: sm2.PriorityB, Tags: []string{"greeting", "common"}}
	c2, err := s.UpdateCard(ctx, userID, c.ID, c.Version, upd)
	if err != nil {
		t.Fatalf("UpdateCard: %v", err)
	}
	if c2.Version != c.Version+1 || c2.Front != "hola!" || c2.Priority != sm2.PriorityB {
		t.Errorf("updated card = %+v", c2)
	}
	if len(c2.Tags) != 2 {
		t.Errorf("tags = %v", c2.Tags)
	}

	// Stale version -> conflict.
	if _, err := s.UpdateCard(ctx, userID, c.ID, c.Version, upd); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale update: got %v, want ErrVersionConflict", err)
	}
}

func TestScheduleReviewAndConflict(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	c := mkCard(t, s, userID, deckID, "hola", sm2.PriorityA)
	sched := *c.ForwardSchedule()

	got, err := s.RecordReview(ctx, userID, sched.ID, sm2.GradeCorrectPerfectRecall, sched.Version, now)
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}
	if got.Version != sched.Version+1 || got.RepCount != 1 || got.Interval != 1 || *got.Grade != sm2.GradeCorrectPerfectRecall {
		t.Errorf("reviewed schedule = %+v", got)
	}
	if got.Easiness < 2.59 || got.Easiness > 2.61 {
		t.Errorf("easiness = %v, want ~2.6", got.Easiness)
	}
	if got.LastSeen == nil {
		t.Error("lastSeen not stamped")
	}

	// Replay with the stale version -> conflict.
	if _, err := s.RecordReview(ctx, userID, sched.ID, sm2.GradeIncorrect, sched.Version, now); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale review: got %v, want ErrVersionConflict", err)
	}

	// Another user can't grade it.
	u2, _ := s.CreateUser(ctx, "Other", "other3@example.com", "h")
	if _, err := s.RecordReview(ctx, u2.ID, sched.ID, sm2.GradeIncorrect, got.Version, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user review: got %v, want ErrNotFound", err)
	}

	// Reset restores initial state and bumps version.
	reset, err := s.ResetProgress(ctx, userID, sched.ID)
	if err != nil {
		t.Fatalf("ResetProgress: %v", err)
	}
	if reset.Easiness != 2.5 || reset.Interval != 1 || reset.RepCount != 0 || reset.Grade != nil || reset.LastSeen != nil {
		t.Errorf("reset state = %+v", reset)
	}
	if reset.Version != got.Version+1 {
		t.Errorf("reset version = %d, want %d", reset.Version, got.Version+1)
	}
}

func TestScheduleRegrade(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	c := mkCard(t, s, userID, deckID, "hola", sm2.PriorityA)
	sched := *c.ForwardSchedule()

	// Nothing reviewed yet -> nothing to change.
	if _, err := s.RegradeReview(ctx, userID, sched.ID, sm2.GradeIncorrect, sched.Version, now); !errors.Is(err, ErrNoPreviousGrade) {
		t.Errorf("regrade before any review: got %v, want ErrNoPreviousGrade", err)
	}

	// Two reviews of history, so the state the third one starts from is
	// visibly different from the state it produces.
	cur := sched
	for i := 0; i < 2; i++ {
		var err error
		if cur, err = s.RecordReview(ctx, userID, sched.ID, sm2.GradeCorrectPerfectRecall, cur.Version, now); err != nil {
			t.Fatalf("RecordReview %d: %v", i, err)
		}
	}
	baseline := cur.State()

	graded, err := s.RecordReview(ctx, userID, sched.ID, sm2.GradeCorrectPerfectRecall, cur.Version, now)
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}

	sameState := func(t *testing.T, label string, got Schedule, want sm2.State) {
		t.Helper()
		if got.RepCount != want.RepCount || got.Interval != want.Interval ||
			math.Abs(got.Easiness-want.Easiness) > 1e-9 ||
			got.Grade == nil || *got.Grade != *want.Grade {
			t.Errorf("%s = {easiness %v interval %d reps %d grade %v}, want {easiness %v interval %d reps %d grade %v}",
				label, got.Easiness, got.Interval, got.RepCount, got.Grade,
				want.Easiness, want.Interval, want.RepCount, *want.Grade)
		}
	}

	// Changing the grade applies it to the *pre-grade* state, not to the state
	// the first grade produced.
	changed, err := s.RegradeReview(ctx, userID, sched.ID, sm2.GradeCorrectWithHesitation, graded.Version, now)
	if err != nil {
		t.Fatalf("RegradeReview: %v", err)
	}
	sameState(t, "regraded", changed, sm2.CalculateNextInterval(baseline, sm2.GradeCorrectWithHesitation, now))
	if changed.Version != graded.Version+1 {
		t.Errorf("regrade version = %d, want %d", changed.Version, graded.Version+1)
	}
	if compounded := sm2.CalculateNextInterval(graded.State(), sm2.GradeCorrectWithHesitation, now); changed.Interval == compounded.Interval && changed.Easiness == compounded.Easiness {
		t.Errorf("regrade compounded on the graded state instead of rewinding: %+v", changed)
	}

	// Every further change re-derives from the same baseline, so cycling
	// through the grades and back lands on the original values.
	for _, g := range []sm2.Grade{sm2.GradeIncorrect, sm2.GradeCorrectPerfectRecall, sm2.GradeCorrectWithHesitation} {
		next, err := s.RegradeReview(ctx, userID, sched.ID, g, changed.Version, now)
		if err != nil {
			t.Fatalf("RegradeReview(%s): %v", g, err)
		}
		sameState(t, "regraded "+string(g), next, sm2.CalculateNextInterval(baseline, g, now))
		changed = next
	}

	// Stale version -> conflict; another user can't touch it.
	if _, err := s.RegradeReview(ctx, userID, sched.ID, sm2.GradeIncorrect, graded.Version, now); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale regrade: got %v, want ErrVersionConflict", err)
	}
	u2, _ := s.CreateUser(ctx, "Other", "other4@example.com", "h")
	if _, err := s.RegradeReview(ctx, u2.ID, sched.ID, sm2.GradeIncorrect, changed.Version, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user regrade: got %v, want ErrNotFound", err)
	}

	// A reset drops the snapshot: there is no longer a review to change.
	reset, err := s.ResetProgress(ctx, userID, sched.ID)
	if err != nil {
		t.Fatalf("ResetProgress: %v", err)
	}
	if _, err := s.RegradeReview(ctx, userID, sched.ID, sm2.GradeIncorrect, reset.Version, now); !errors.Is(err, ErrNoPreviousGrade) {
		t.Errorf("regrade after reset: got %v, want ErrNoPreviousGrade", err)
	}
}

func TestCascadeDelete(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, true)
	c := mkCard(t, s, userID, deckID, "hola", sm2.PriorityA)

	if err := s.DeleteTopic(ctx, userID, topicID); err != nil {
		t.Fatalf("DeleteTopic: %v", err)
	}
	if _, err := s.GetCard(ctx, userID, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("card after cascade: got %v, want ErrNotFound", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schedules`).Scan(&n); err != nil || n != 0 {
		t.Errorf("schedules after cascade = %d, want 0 (err %v)", n, err)
	}
}

func TestListCardsFilters(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	deck2, err := s.CreateDeck(ctx, userID, DeckParams{Name: "Phrases", TopicID: topicID})
	if err != nil {
		t.Fatal(err)
	}
	mkCard(t, s, userID, deckID, "zebra", sm2.PriorityA, "animal")
	mkCard(t, s, userID, deckID, "apple", sm2.PriorityB, "food")
	mkCard(t, s, userID, deck2.ID, "banana split", sm2.PriorityB, "food", "dessert")

	// Default sort: front asc.
	cards, total, err := s.ListCards(ctx, userID, CardListParams{})
	if err != nil || total != 3 {
		t.Fatalf("ListCards = (%d, %v)", total, err)
	}
	if cards[0].Front != "apple" || cards[2].Front != "zebra" {
		t.Errorf("sort order: %v", []string{cards[0].Front, cards[1].Front, cards[2].Front})
	}

	// Tag filter via json_each.
	_, total, _ = s.ListCards(ctx, userID, CardListParams{Tag: "food"})
	if total != 2 {
		t.Errorf("tag filter total = %d, want 2", total)
	}

	// Deck filter.
	_, total, _ = s.ListCards(ctx, userID, CardListParams{DeckID: &deck2.ID})
	if total != 1 {
		t.Errorf("deck filter total = %d, want 1", total)
	}

	// Priority filter.
	_, total, _ = s.ListCards(ctx, userID, CardListParams{Priority: "B"})
	if total != 2 {
		t.Errorf("priority filter total = %d, want 2", total)
	}

	// Search hits front/back, with LIKE-escaping.
	_, total, _ = s.ListCards(ctx, userID, CardListParams{Search: "banana"})
	if total != 1 {
		t.Errorf("search total = %d, want 1", total)
	}
	_, total, _ = s.ListCards(ctx, userID, CardListParams{Search: "100%"})
	if total != 0 {
		t.Errorf("wildcard search total = %d, want 0", total)
	}

	// Pagination.
	page, total, _ := s.ListCards(ctx, userID, CardListParams{Page: 2, PerPage: 2})
	if total != 3 || len(page) != 1 {
		t.Errorf("pagination = (%d items, %d total), want (1, 3)", len(page), total)
	}

	// Distinct tags.
	tags, err := s.DistinctTags(ctx, userID)
	if err != nil || len(tags) != 3 {
		t.Errorf("DistinctTags = (%v, %v), want 3 tags", tags, err)
	}
}

func TestStudyFilters(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	deck2, err := s.CreateDeck(ctx, userID, DeckParams{Name: "Phrases", TopicID: topicID})
	if err != nil {
		t.Fatal(err)
	}
	cA := mkCard(t, s, userID, deckID, "cardA", sm2.PriorityA)
	cB := mkCard(t, s, userID, deck2.ID, "cardB", sm2.PriorityB)
	cC := mkCard(t, s, userID, deckID, "cardC", sm2.PriorityC)

	all := StudyFilter{IncludeNew: true}

	t.Run("A-priority band picked before B and C", func(t *testing.T) {
		item, err := s.NextDue(ctx, userID, topicID, all, now)
		if err != nil || item == nil {
			t.Fatalf("NextDue = (%v, %v)", item, err)
		}
		if item.Priority != sm2.PriorityA {
			t.Errorf("priority = %v, want A first", item.Priority)
		}
	})

	t.Run("multiple deckIds are all honored", func(t *testing.T) {
		// Both decks selected: still finds the A card in deck 1.
		f := StudyFilter{DeckIDs: []int64{deckID, deck2.ID}, IncludeNew: true}
		n, err := s.CountDue(ctx, userID, topicID, f, now)
		if err != nil || n != 3 {
			t.Errorf("CountDue both decks = (%d, %v), want 3", n, err)
		}
		// Only deck2: the B card alone.
		f = StudyFilter{DeckIDs: []int64{deck2.ID}, IncludeNew: true}
		item, err := s.NextDue(ctx, userID, topicID, f, now)
		if err != nil || item == nil || item.CardID != cB.ID {
			t.Errorf("NextDue deck2 = (%+v, %v), want card %d", item, err, cB.ID)
		}
	})

	t.Run("priority filter restricts the band", func(t *testing.T) {
		f := StudyFilter{Priority: "C", IncludeNew: true}
		item, err := s.NextDue(ctx, userID, topicID, f, now)
		if err != nil || item == nil || item.CardID != cC.ID {
			t.Errorf("NextDue priority C = (%+v, %v), want card %d", item, err, cC.ID)
		}
	})

	t.Run("includeNew=false excludes never-seen schedules", func(t *testing.T) {
		f := StudyFilter{IncludeNew: false}
		n, err := s.CountDue(ctx, userID, topicID, f, now)
		if err != nil || n != 0 {
			t.Errorf("CountDue seen-only = (%d, %v), want 0", n, err)
		}
		// Review card A long ago -> it becomes a seen due card.
		sched := *cA.ForwardSchedule()
		past := now.AddDate(0, 0, -10)
		if _, err := s.RecordReview(ctx, userID, sched.ID, sm2.GradeIncorrect, sched.Version, past); err != nil {
			t.Fatal(err)
		}
		n, err = s.CountDue(ctx, userID, topicID, f, now)
		if err != nil || n != 1 {
			t.Errorf("CountDue after review = (%d, %v), want 1", n, err)
		}
	})

	t.Run("reverse schedules gated by bidirectional flag", func(t *testing.T) {
		// cA/cC in deck1 (unidirectional): flipping deck2 to bidirectional
		// back-fills a reverse schedule for cB and makes it studyable.
		before, _ := s.CountDue(ctx, userID, topicID, all, now)
		if _, err := s.UpdateDeck(ctx, userID, deck2.ID, DeckParams{Name: "Phrases", TopicID: topicID, IsBidirectional: true}); err != nil {
			t.Fatal(err)
		}
		after, _ := s.CountDue(ctx, userID, topicID, all, now)
		if after != before+1 {
			t.Errorf("due after bidir flip = %d, want %d", after, before+1)
		}
		// Flip back off: reverse schedule stays but is no longer studyable.
		if _, err := s.UpdateDeck(ctx, userID, deck2.ID, DeckParams{Name: "Phrases", TopicID: topicID, IsBidirectional: false}); err != nil {
			t.Fatal(err)
		}
		final, _ := s.CountDue(ctx, userID, topicID, all, now)
		if final != before {
			t.Errorf("due after unflip = %d, want %d", final, before)
		}
	})

	t.Run("cross-user topic access denied", func(t *testing.T) {
		u2, _ := s.CreateUser(ctx, "Other", "other4@example.com", "h")
		if _, err := s.NextDue(ctx, u2.ID, topicID, all, now); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user NextDue: got %v, want ErrNotFound", err)
		}
	})
}

func TestImportCards(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	existing := mkCard(t, s, userID, deckID, "already here", sm2.PriorityA)

	lastSeen := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)
	grade := sm2.GradeCorrectPerfectRecall
	revGrade := sm2.GradeCorrectWithHesitation
	cards := []ImportCardParams{
		{Front: "plain", Back: "b", Priority: sm2.PriorityA, Forward: sm2.InitialState()},
		{
			Front: "practiced", Back: "b", Priority: sm2.PriorityB, Tags: []string{"x"},
			Forward: sm2.State{LastSeen: &lastSeen, Grade: &grade, RepCount: 3, Easiness: 2.8, Interval: 12},
			Reverse: &sm2.State{LastSeen: &lastSeen, Grade: &revGrade, RepCount: 1, Easiness: 2.5, Interval: 1},
		},
	}
	madeBidir, err := s.ImportCards(ctx, userID, deckID, cards)
	if err != nil {
		t.Fatalf("ImportCards: %v", err)
	}
	if !madeBidir {
		t.Error("reverse params imported into a unidirectional deck: madeBidirectional = false")
	}

	got, total, err := s.ListCards(ctx, userID, CardListParams{})
	if err != nil || total != 3 {
		t.Fatalf("after import: (%d, %v)", total, err)
	}
	var practiced Card
	for _, c := range got {
		if c.Front == "practiced" {
			practiced = c
		}
	}
	fwd := practiced.ForwardSchedule()
	if fwd == nil || fwd.Easiness != 2.8 || fwd.Interval != 12 || fwd.RepCount != 3 || *fwd.Grade != grade {
		t.Errorf("imported forward state = %+v", fwd)
	}
	if fwd.LastSeen == nil || !fwd.LastSeen.Equal(lastSeen) {
		t.Errorf("imported lastSeen = %v, want %v", fwd.LastSeen, lastSeen)
	}
	// Reverse params present -> reverse schedule carries the imported state.
	rev := practiced.ReverseSchedule()
	if rev == nil || rev.RepCount != 1 || *rev.Grade != revGrade {
		t.Errorf("imported reverse state = %+v", rev)
	}

	// The deck was flipped bidirectional, so cards that predate the import are
	// back-filled with a reverse schedule at the initial state.
	deck, err := s.GetDeck(ctx, userID, deckID)
	if err != nil || !deck.IsBidirectional {
		t.Fatalf("deck after import: bidirectional = %v (err %v)", deck.IsBidirectional, err)
	}
	var backfilled Card
	for _, c := range got {
		if c.ID == existing.ID {
			backfilled = c
		}
	}
	exRev := backfilled.ReverseSchedule()
	init := sm2.InitialState()
	if exRev == nil || exRev.Grade != nil || exRev.RepCount != init.RepCount ||
		exRev.Easiness != init.Easiness || exRev.Interval != init.Interval {
		t.Errorf("back-filled reverse state = %+v, want initial", exRev)
	}

	// Import into someone else's deck fails.
	u2, _ := s.CreateUser(ctx, "Other", "other5@example.com", "h")
	if _, err := s.ImportCards(ctx, u2.ID, deckID, cards[:1]); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user import: got %v, want ErrNotFound", err)
	}
}

// TestImportCardsNoReverseKeepsDeckUnidirectional pins the other half of the
// rule: only reverse params flip the deck.
func TestImportCardsNoReverseKeepsDeckUnidirectional(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)

	madeBidir, err := s.ImportCards(ctx, userID, deckID, []ImportCardParams{
		{Front: "plain", Back: "b", Priority: sm2.PriorityA, Forward: sm2.InitialState()},
	})
	if err != nil {
		t.Fatalf("ImportCards: %v", err)
	}
	if madeBidir {
		t.Error("madeBidirectional = true without any reverse params")
	}
	deck, err := s.GetDeck(ctx, userID, deckID)
	if err != nil || deck.IsBidirectional {
		t.Fatalf("deck bidirectional = %v (err %v), want false", deck.IsBidirectional, err)
	}
	got, _, err := s.ListCards(ctx, userID, CardListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if rev := got[0].ReverseSchedule(); rev != nil {
		t.Errorf("unexpected reverse schedule = %+v", rev)
	}
}

func TestDashboardStats(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, false)
	c1 := mkCard(t, s, userID, deckID, "one", sm2.PriorityA)
	c2 := mkCard(t, s, userID, deckID, "two", sm2.PriorityB)
	mkCard(t, s, userID, deckID, "three", sm2.PriorityC)

	// Grade c1 correct (long ago -> due again), c2 incorrect just now (not due).
	past := now.AddDate(0, 0, -30)
	if _, err := s.RecordReview(ctx, userID, c1.ForwardSchedule().ID, sm2.GradeCorrectPerfectRecall, 0, past); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordReview(ctx, userID, c2.ForwardSchedule().ID, sm2.GradeIncorrect, 0, now); err != nil {
		t.Fatal(err)
	}

	stats, err := s.DashboardStats(ctx, userID, now)
	if err != nil {
		t.Fatalf("DashboardStats: %v", err)
	}
	if stats.TotalTopics != 1 || stats.TotalDecks != 1 || stats.TotalCards != 3 {
		t.Errorf("totals = %+v", stats)
	}
	if stats.CardsCorrect != 1 || stats.CardsIncorrect != 1 {
		t.Errorf("correct/incorrect = %d/%d, want 1/1", stats.CardsCorrect, stats.CardsIncorrect)
	}
	// Due: c1 (A, reviewed 30d ago, interval 1) and c3 (C, never seen); c2 seen today.
	if stats.DueA != 1 || stats.DueB != 0 || stats.DueC != 1 {
		t.Errorf("due = A%d B%d C%d, want A1 B0 C1", stats.DueA, stats.DueB, stats.DueC)
	}
	if len(stats.RecentActivity) != 2 {
		t.Errorf("recent activity = %d entries, want 2", len(stats.RecentActivity))
	}
	// Most recent first.
	if len(stats.RecentActivity) == 2 && !stats.RecentActivity[0].Timestamp.After(stats.RecentActivity[1].Timestamp) {
		t.Error("recent activity not sorted desc")
	}
}

func TestSessions(t *testing.T) {
	s := newTestStore(t)
	userID, _, _ := seed(t, s, false)

	token, err := s.CreateSession(ctx, userID, now)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	u, err := s.GetSessionUser(ctx, token, now)
	if err != nil || u.ID != userID {
		t.Errorf("GetSessionUser = (%+v, %v)", u, err)
	}

	// Expired session is invalid.
	if _, err := s.GetSessionUser(ctx, token, now.Add(SessionDuration+time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session: got %v, want ErrNotFound", err)
	}

	// Password change keeps only the current session.
	token2, _ := s.CreateSession(ctx, userID, now)
	if err := s.DeleteOtherSessions(ctx, userID, token2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionUser(ctx, token, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("old session after DeleteOtherSessions: got %v, want ErrNotFound", err)
	}
	if _, err := s.GetSessionUser(ctx, token2, now); err != nil {
		t.Errorf("kept session: %v", err)
	}

	// Logout.
	if err := s.DeleteSession(ctx, token2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionUser(ctx, token2, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("session after logout: got %v, want ErrNotFound", err)
	}
}
