package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"cadence-cards/internal/sm2"
)

// GetSchedule fetches one schedule owned by the user.
func (s *Store) GetSchedule(ctx context.Context, userID, scheduleID int64) (Schedule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT s.`+scheduleColsPrefixed+` FROM schedules s
		JOIN cards c ON c.id = s.card_id
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE s.id = ? AND t.user_id = ?`, scheduleID, userID)
	return scanSchedule(row)
}

const scheduleColsPrefixed = `id, s.card_id, s.is_reversed, s.easiness, s.interval, s.rep_count, s.grade, s.last_seen, s.version, s.created_at, s.updated_at`

// RecordReview applies a graded review to a schedule with optimistic locking
// (port of schedule-service recordReview): ownership check, version compare,
// SM-2 calculation, and update happen in one transaction. A version mismatch
// returns ErrVersionConflict.
func (s *Store) RecordReview(ctx context.Context, userID, scheduleID int64, grade sm2.Grade, currentVersion int, now time.Time) (Schedule, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT s.`+scheduleColsPrefixed+` FROM schedules s
			JOIN cards c ON c.id = s.card_id
			JOIN decks d ON d.id = c.deck_id
			JOIN topics t ON t.id = d.topic_id
			WHERE s.id = ? AND t.user_id = ?`, scheduleID, userID)
		sched, err := scanSchedule(row)
		if err != nil {
			return err
		}
		if sched.Version != currentVersion {
			return ErrVersionConflict
		}

		// The state this review starts from is also stashed in prev_*, so the
		// grade can be changed afterwards without compounding (RegradeReview).
		prev := sched.State()
		updated := sm2.CalculateNextInterval(prev, grade, now)
		res, err := tx.ExecContext(ctx, `
			UPDATE schedules SET easiness = ?, interval = ?, rep_count = ?, grade = ?, last_seen = ?,
			       prev_easiness = ?, prev_interval = ?, prev_rep_count = ?, prev_grade = ?, prev_last_seen = ?,
			       version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND version = ?`,
			updated.Easiness, updated.Interval, updated.RepCount, string(grade), fmtTime(now),
			prev.Easiness, prev.Interval, prev.RepCount, gradeOrNil(prev.Grade), fmtNullTime(prev.LastSeen),
			scheduleID, currentVersion)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, userID, scheduleID)
}

// RegradeReview changes the grade of a schedule's most recent review: the new
// grade is applied to the state that review *started* from (the prev_* snapshot
// RecordReview left behind), so changing an answer is an undo followed by a
// fresh grade rather than a second review compounded on the first. The snapshot
// is deliberately left untouched, so successive changes all re-derive from the
// same baseline. Optimistic locking and the ownership check work exactly as in
// RecordReview; a schedule with no snapshot returns ErrNoPreviousGrade.
func (s *Store) RegradeReview(ctx context.Context, userID, scheduleID int64, grade sm2.Grade, currentVersion int, now time.Time) (Schedule, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT s.version, s.prev_easiness, s.prev_interval, s.prev_rep_count, s.prev_grade, s.prev_last_seen
			FROM schedules s
			JOIN cards c ON c.id = s.card_id
			JOIN decks d ON d.id = c.deck_id
			JOIN topics t ON t.id = d.topic_id
			WHERE s.id = ? AND t.user_id = ?`, scheduleID, userID)

		var (
			version             int
			easiness            sql.NullFloat64
			interval, repCount  sql.NullInt64
			prevGrade, prevSeen sql.NullString
		)
		if err := row.Scan(&version, &easiness, &interval, &repCount, &prevGrade, &prevSeen); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if version != currentVersion {
			return ErrVersionConflict
		}
		if !easiness.Valid {
			return ErrNoPreviousGrade
		}

		prev := sm2.State{
			RepCount: int(repCount.Int64),
			Easiness: easiness.Float64,
			Interval: int(interval.Int64),
		}
		if prevGrade.Valid {
			g := sm2.Grade(prevGrade.String)
			prev.Grade = &g
		}
		var err error
		if prev.LastSeen, err = nullTime(prevSeen); err != nil {
			return err
		}

		updated := sm2.CalculateNextInterval(prev, grade, now)
		res, err := tx.ExecContext(ctx, `
			UPDATE schedules SET easiness = ?, interval = ?, rep_count = ?, grade = ?, last_seen = ?,
			       version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND version = ?`,
			updated.Easiness, updated.Interval, updated.RepCount, string(grade), fmtTime(now),
			scheduleID, currentVersion)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, userID, scheduleID)
}

// ResetProgress restores a schedule to the initial SM-2 state (port of
// schedule-service resetProgress; version still increments). The regrade
// snapshot is cleared too — after a reset there is no review to rewind.
func (s *Store) ResetProgress(ctx context.Context, userID, scheduleID int64) (Schedule, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE schedules SET easiness = 2.5, interval = 1, rep_count = 0, grade = NULL, last_seen = NULL,
		       prev_easiness = NULL, prev_interval = NULL, prev_rep_count = NULL,
		       prev_grade = NULL, prev_last_seen = NULL,
		       version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND card_id IN (
			SELECT c.id FROM cards c
			JOIN decks d ON d.id = c.deck_id
			JOIN topics t ON t.id = d.topic_id
			WHERE t.user_id = ?)`, scheduleID, userID)
	if err != nil {
		return Schedule{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Schedule{}, ErrNotFound
	}
	return s.GetSchedule(ctx, userID, scheduleID)
}

// SetScheduleState overwrites a schedule's SM-2 fields (YAML import with SM-2
// params). Not version-checked: import creates the card and schedules in the
// same transaction.
func setScheduleState(ctx context.Context, tx *sql.Tx, cardID int64, isReversed bool, st sm2.State) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE schedules SET easiness = ?, interval = ?, rep_count = ?, grade = ?, last_seen = ?,
		       prev_easiness = NULL, prev_interval = NULL, prev_rep_count = NULL,
		       prev_grade = NULL, prev_last_seen = NULL
		WHERE card_id = ? AND is_reversed = ?`,
		st.Easiness, st.Interval, st.RepCount, gradeOrNil(st.Grade), fmtNullTime(st.LastSeen),
		cardID, boolInt(isReversed))
	return err
}

// gradeOrNil converts an optional grade to a nullable column value.
func gradeOrNil(g *sm2.Grade) any {
	if g == nil {
		return nil
	}
	return string(*g)
}
