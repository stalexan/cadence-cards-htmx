package store

import (
	"context"
	"database/sql"
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

		updated := sm2.CalculateNextInterval(sched.State(), grade, now)
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
// schedule-service resetProgress; version still increments).
func (s *Store) ResetProgress(ctx context.Context, userID, scheduleID int64) (Schedule, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE schedules SET easiness = 2.5, interval = 1, rep_count = 0, grade = NULL, last_seen = NULL,
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
	var grade any
	if st.Grade != nil {
		grade = string(*st.Grade)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE schedules SET easiness = ?, interval = ?, rep_count = ?, grade = ?, last_seen = ?
		WHERE card_id = ? AND is_reversed = ?`,
		st.Easiness, st.Interval, st.RepCount, grade, fmtNullTime(st.LastSeen),
		cardID, boolInt(isReversed))
	return err
}
