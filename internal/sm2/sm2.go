// Package sm2 implements the spaced repetition algorithm. This algorithm is
// based on the SuperMemo 2 (SM-2) algorithm developed by Piotr Woźniak.
// Source: https://www.supermemo.com/en/blog/application-of-a-computer-to-improve-the-results-obtained-in-working-with-the-supermemo-method
//
// It is a direct port of web/src/lib/sm2.ts from cadence-cards-svelte. Unlike
// the TypeScript version, "now" is always passed in explicitly so the day-based
// due logic is deterministic and testable. Day boundaries are computed at local
// midnight (time.Local), matching the source's use of the JS local timezone.
package sm2

import (
	"math"
	"time"
)

// Grade values aligned with the database schema.
type Grade string

const (
	GradeIncorrect             Grade = "INCORRECT"
	GradeCorrectWithHesitation Grade = "CORRECT_WITH_HESITATION"
	GradeCorrectPerfectRecall  Grade = "CORRECT_PERFECT_RECALL"
)

// ValidGrade reports whether s is one of the three grade constants.
func ValidGrade(s string) bool {
	switch Grade(s) {
	case GradeIncorrect, GradeCorrectWithHesitation, GradeCorrectPerfectRecall:
		return true
	}
	return false
}

// Priority levels for cards.
type Priority string

const (
	PriorityA Priority = "A" // High priority
	PriorityB Priority = "B" // Medium priority
	PriorityC Priority = "C" // Low priority
)

// Priorities in study order (A → B → C).
var Priorities = []Priority{PriorityA, PriorityB, PriorityC}

// ValidPriority reports whether s is one of the three priority constants.
func ValidPriority(s string) bool {
	switch Priority(s) {
	case PriorityA, PriorityB, PriorityC:
		return true
	}
	return false
}

// State holds the SM-2 scheduling parameters of one schedule (one study
// direction of one card).
type State struct {
	LastSeen *time.Time
	Grade    *Grade
	RepCount int
	Easiness float64
	Interval int
}

// InitialState returns the SM-2 parameters for a new, never-studied schedule.
func InitialState() State {
	return State{RepCount: 0, Easiness: 2.5, Interval: 1}
}

// EasinessDecimals is how many decimal places an easiness factor is kept to.
const EasinessDecimals = 2

// RoundEasiness rounds an easiness factor to EasinessDecimals places.
//
// A second deliberate deviation from the JS source (alongside DaysBetween):
// the SM-2 increments are values like 0.1 and -0.14 that binary floating point
// cannot represent exactly, so repeated reviews accumulate visible drift and
// store/export "2.8" as 2.8000000000000003. Two decimals is finer than the
// smallest increment the formula can produce (0.02), so rounding here costs no
// scheduling fidelity: intervals are themselves rounded to whole days.
func RoundEasiness(ef float64) float64 {
	scale := math.Pow(10, EasinessDecimals)
	return math.Round(ef*scale) / scale
}

// DaysBetween calculates whole days between two instants, ignoring time of
// day, using local-midnight boundaries (port of getDaysBetweenDates).
//
// Rounded, not floored: across a DST transition adjacent local midnights are
// 23 or 25 hours apart, and flooring 23h/24 to 0 made a 1-day interval skip
// the day after spring-forward (a bug the JS source shares). DST shifts are
// at most one hour, so rounding always lands on the calendar-day count.
func DaysBetween(a, b time.Time) int {
	al, bl := a.In(time.Local), b.In(time.Local)
	d1 := time.Date(al.Year(), al.Month(), al.Day(), 0, 0, 0, 0, time.Local)
	d2 := time.Date(bl.Year(), bl.Month(), bl.Day(), 0, 0, 0, 0, time.Local)
	return int(math.Round(d2.Sub(d1).Hours() / 24))
}

// CalculateNextInterval applies one review with the given grade to the
// original state (the state when the card was first shown in this repetition)
// and returns the updated state. LastSeen is stamped with now.
func CalculateNextInterval(original State, newGrade Grade, now time.Time) State {
	updated := original
	g := newGrade
	updated.Grade = &g
	seen := now
	updated.LastSeen = &seen

	// Quality mapping: 0 for complete failure to recall.
	var quality float64
	switch newGrade {
	case GradeCorrectPerfectRecall:
		quality = 5
	case GradeCorrectWithHesitation:
		quality = 4
	case GradeIncorrect:
		quality = 0
	}

	// Standard SM-2 formula: EF' = EF + (0.1 - (5 - q) * (0.08 + (5 - q) * 0.02))
	newEasiness := original.Easiness + (0.1 - (5-quality)*(0.08+(5-quality)*0.02))
	// 1.3 floor, no upper bound.
	updated.Easiness = RoundEasiness(math.Max(1.3, newEasiness))

	var newInterval, newRepCount int
	if newGrade == GradeIncorrect {
		// Incorrect answers reset the interval and repetition count.
		newInterval = 1
		newRepCount = 0
	} else {
		newRepCount = original.RepCount + 1
		switch newRepCount {
		case 1:
			newInterval = 1
		case 2:
			newInterval = 6
		default:
			// For repetitions > 2, use the SM-2 formula.
			newInterval = int(math.Round(float64(original.Interval) * updated.Easiness))
		}
	}

	updated.RepCount = newRepCount
	updated.Interval = max(1, newInterval)
	return updated
}

// IsDue reports whether a schedule is due for review at now. A never-seen
// schedule is always due; otherwise it is due when whole local days since
// LastSeen >= Interval.
func (s State) IsDue(now time.Time) bool {
	if s.LastSeen == nil {
		return true
	}
	return DaysBetween(*s.LastSeen, now) >= s.Interval
}

// NextDueDate returns the date a schedule becomes due (LastSeen + Interval
// days; now when never seen). Used for priority-then-due-date sorting.
func (s State) NextDueDate(now time.Time) time.Time {
	base := now
	if s.LastSeen != nil {
		base = *s.LastSeen
	}
	return base.AddDate(0, 0, s.Interval)
}
