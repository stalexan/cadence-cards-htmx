package sm2

// Port of web/src/lib/sm2.test.ts from cadence-cards-svelte.

import (
	"math"
	"sort"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)

// daysAgo builds a time exactly `days` whole days before now.
func daysAgo(days int) *time.Time {
	t := now.AddDate(0, 0, -days)
	return &t
}

func closeTo(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("got %v, want %v (±%v)", got, want, tol)
	}
}

func TestCalculateNextInterval(t *testing.T) {
	base := State{RepCount: 0, Easiness: 2.5, Interval: 1}

	t.Run("resets interval and repCount and drops easiness on INCORRECT", func(t *testing.T) {
		s := base
		s.RepCount, s.Interval = 4, 30
		result := CalculateNextInterval(s, GradeIncorrect, now)
		if result.Interval != 1 {
			t.Errorf("interval = %d, want 1", result.Interval)
		}
		if result.RepCount != 0 {
			t.Errorf("repCount = %d, want 0", result.RepCount)
		}
		if *result.Grade != GradeIncorrect {
			t.Errorf("grade = %v, want INCORRECT", *result.Grade)
		}
		// quality 0: EF delta is -0.8 -> 2.5 - 0.8 = 1.7
		closeTo(t, result.Easiness, 1.7, 1e-5)
	})

	t.Run("never lets easiness fall below the 1.3 floor", func(t *testing.T) {
		// Two consecutive INCORRECT grades would push EF to 0.9 without the clamp.
		s := CalculateNextInterval(base, GradeIncorrect, now) // 1.7
		s = CalculateNextInterval(s, GradeIncorrect, now)     // 0.9 -> clamp
		if s.Easiness != 1.3 {
			t.Errorf("easiness = %v, want 1.3", s.Easiness)
		}
	})

	t.Run("treats CORRECT_WITH_HESITATION (quality 4) as the neutral easiness point", func(t *testing.T) {
		result := CalculateNextInterval(base, GradeCorrectWithHesitation, now)
		closeTo(t, result.Easiness, 2.5, 1e-5)
		if result.RepCount != 1 {
			t.Errorf("repCount = %d, want 1", result.RepCount)
		}
	})

	t.Run("raises easiness on CORRECT_PERFECT_RECALL (quality 5)", func(t *testing.T) {
		result := CalculateNextInterval(base, GradeCorrectPerfectRecall, now)
		closeTo(t, result.Easiness, 2.6, 1e-5)
	})

	t.Run("follows the SM-2 interval progression for correct answers", func(t *testing.T) {
		// First correct rep -> interval 1
		first := CalculateNextInterval(base, GradeCorrectPerfectRecall, now)
		if first.RepCount != 1 || first.Interval != 1 {
			t.Errorf("first rep = (%d, %d), want (1, 1)", first.RepCount, first.Interval)
		}

		// Second correct rep -> interval 6
		s := base
		s.RepCount, s.Interval = 1, 1
		second := CalculateNextInterval(s, GradeCorrectPerfectRecall, now)
		if second.RepCount != 2 || second.Interval != 6 {
			t.Errorf("second rep = (%d, %d), want (2, 6)", second.RepCount, second.Interval)
		}

		// Third+ correct rep -> round(interval * newEasiness); EF 2.5 -> 2.6, 6 * 2.6 = 15.6 -> 16
		s = base
		s.RepCount, s.Interval = 2, 6
		third := CalculateNextInterval(s, GradeCorrectPerfectRecall, now)
		if third.RepCount != 3 || third.Interval != 16 {
			t.Errorf("third rep = (%d, %d), want (3, 16)", third.RepCount, third.Interval)
		}
	})

	t.Run("stamps lastSeen with now", func(t *testing.T) {
		result := CalculateNextInterval(base, GradeCorrectPerfectRecall, now)
		if result.LastSeen == nil || !result.LastSeen.Equal(now) {
			t.Errorf("lastSeen = %v, want %v", result.LastSeen, now)
		}
	})
}

func TestDaysBetween(t *testing.T) {
	t.Run("returns 0 for the same calendar day regardless of time", func(t *testing.T) {
		morning := time.Date(2024, 1, 15, 8, 0, 0, 0, time.Local)
		evening := time.Date(2024, 1, 15, 23, 0, 0, 0, time.Local)
		if d := DaysBetween(morning, evening); d != 0 {
			t.Errorf("DaysBetween = %d, want 0", d)
		}
	})

	t.Run("returns 1 for consecutive days", func(t *testing.T) {
		a := time.Date(2024, 1, 15, 0, 0, 0, 0, time.Local)
		b := time.Date(2024, 1, 16, 0, 0, 0, 0, time.Local)
		if d := DaysBetween(a, b); d != 1 {
			t.Errorf("DaysBetween = %d, want 1", d)
		}
	})

	t.Run("handles month and year boundaries", func(t *testing.T) {
		if d := DaysBetween(time.Date(2024, 1, 31, 0, 0, 0, 0, time.Local), time.Date(2024, 2, 1, 0, 0, 0, 0, time.Local)); d != 1 {
			t.Errorf("month boundary = %d, want 1", d)
		}
		if d := DaysBetween(time.Date(2023, 12, 31, 0, 0, 0, 0, time.Local), time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local)); d != 1 {
			t.Errorf("year boundary = %d, want 1", d)
		}
	})

	t.Run("returns the day difference from now", func(t *testing.T) {
		if d := DaysBetween(*daysAgo(3), now); d != 3 {
			t.Errorf("DaysBetween = %d, want 3", d)
		}
	})
}

func TestIsDue(t *testing.T) {
	sched := func(lastSeen *time.Time, interval int) State {
		return State{RepCount: 0, Easiness: 2.5, Interval: interval, LastSeen: lastSeen}
	}

	t.Run("treats a never-seen schedule as due", func(t *testing.T) {
		if !sched(nil, 5).IsDue(now) {
			t.Error("never-seen schedule should be due")
		}
	})

	t.Run("is due when days since last seen >= interval (boundary)", func(t *testing.T) {
		if !sched(daysAgo(5), 5).IsDue(now) {
			t.Error("boundary case should be due")
		}
	})

	t.Run("is not due when days since last seen < interval", func(t *testing.T) {
		if sched(daysAgo(0), 1).IsDue(now) {
			t.Error("seen today with interval 1 should not be due")
		}
		if sched(daysAgo(4), 5).IsDue(now) {
			t.Error("4 days ago with interval 5 should not be due")
		}
	})
}

// item pairs a priority with SM-2 state, standing in for the TS
// CardSchedulingData used by sortCardsByPriorityAndDueDate.
type item struct {
	priority Priority
	state    State
}

func TestSortByPriorityAndDueDate(t *testing.T) {
	c := func(p Priority, lastSeen *time.Time, interval int) item {
		return item{priority: p, state: State{Easiness: 2.5, Interval: interval, LastSeen: lastSeen}}
	}

	cLow := c(PriorityC, daysAgo(1), 1)
	bMid := c(PriorityB, daysAgo(1), 1)
	aLater := c(PriorityA, daysAgo(0), 10)
	aSooner := c(PriorityA, daysAgo(10), 1)

	items := []item{cLow, aLater, bMid, aSooner}
	// Sort by priority (A < B < C via string compare), then earliest due date —
	// the ordering contract from sortCardsByPriorityAndDueDate.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].priority != items[j].priority {
			return items[i].priority < items[j].priority
		}
		return items[i].state.NextDueDate(now).Before(items[j].state.NextDueDate(now))
	})

	wantPriorities := []Priority{PriorityA, PriorityA, PriorityB, PriorityC}
	for i, want := range wantPriorities {
		if items[i].priority != want {
			t.Fatalf("position %d priority = %v, want %v", i, items[i].priority, want)
		}
	}
	// Within priority A, the one due sooner comes first.
	if items[0] != aSooner || items[1] != aLater {
		t.Error("within priority A, sooner-due item should come first")
	}
}

func TestCountDueByPriority(t *testing.T) {
	type counted struct {
		priority Priority
		state    State
	}
	card := func(p Priority, lastSeen *time.Time, interval int) counted {
		return counted{priority: p, state: State{Easiness: 2.5, Interval: interval, LastSeen: lastSeen}}
	}

	cards := []counted{
		card(PriorityA, nil, 1),         // due
		card(PriorityA, daysAgo(0), 5),  // not due
		card(PriorityB, daysAgo(10), 1), // due
		card(PriorityC, daysAgo(5), 5),  // due
		card(PriorityC, daysAgo(1), 30), // not due
	}

	var total, a, b, cCount int
	for _, cd := range cards {
		if cd.state.IsDue(now) {
			total++
			switch cd.priority {
			case PriorityA:
				a++
			case PriorityB:
				b++
			case PriorityC:
				cCount++
			}
		}
	}
	if total != 3 || a != 1 || b != 1 || cCount != 1 {
		t.Errorf("counts = (total %d, A %d, B %d, C %d), want (3, 1, 1, 1)", total, a, b, cCount)
	}
}

// Not part of the sm2.test.ts port: the JS source floors hours/24 and skips a
// day after spring-forward; the Go port deliberately rounds instead.
func TestDaysBetweenAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	orig := time.Local
	time.Local = loc
	defer func() { time.Local = orig }()

	// Spring-forward 2026-03-08: the following midnights are 23h apart.
	if got := DaysBetween(
		time.Date(2026, 3, 8, 20, 0, 0, 0, loc),
		time.Date(2026, 3, 9, 9, 0, 0, 0, loc)); got != 1 {
		t.Errorf("spring-forward: DaysBetween = %d, want 1", got)
	}
	// Fall-back 2026-11-01: the following midnights are 25h apart.
	if got := DaysBetween(
		time.Date(2026, 11, 1, 20, 0, 0, 0, loc),
		time.Date(2026, 11, 2, 9, 0, 0, 0, loc)); got != 1 {
		t.Errorf("fall-back: DaysBetween = %d, want 1", got)
	}
}
