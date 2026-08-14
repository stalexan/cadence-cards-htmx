package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"cadence-cards/internal/sm2"
)

// ListUserIDs returns every user ID (the nightly pre-generation job iterates
// all users).
func (s *Store) ListUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SchedulesDueWithin lists a topic's study items that will be due within
// horizon of now and don't already hold a pre-generated question. Due-ness is
// monotonic, so "due at now+horizon" is exactly "due within horizon".
func (s *Store) SchedulesDueWithin(ctx context.Context, userID, topicID int64, now time.Time, horizon time.Duration) ([]StudyItem, error) {
	if err := s.verifyTopicOwnership(ctx, userID, topicID); err != nil {
		return nil, err
	}
	deadline := now.Add(horizon)

	// Schedules that already hold an unused question for this topic.
	have := map[int64]bool{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT gq.schedule_id FROM generated_questions gq
		JOIN schedules sc ON sc.id = gq.schedule_id
		JOIN cards c ON c.id = sc.card_id
		JOIN decks d ON d.id = c.deck_id
		WHERE d.topic_id = ?`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		have[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []StudyItem
	for _, priority := range sm2.Priorities {
		due, err := s.dueSchedules(ctx, userID, topicID, StudyFilter{IncludeNew: true}, priority, deadline)
		if err != nil {
			return nil, err
		}
		for _, item := range due {
			if !have[item.ScheduleID] {
				out = append(out, item)
			}
		}
	}
	return out, nil
}

// UpsertGeneratedQuestion stores (or refreshes) the pre-generated question for
// an owned schedule. The INSERT ... SELECT carries the ownership scope, so a
// non-owned schedule inserts zero rows and returns ErrNotFound.
func (s *Store) UpsertGeneratedQuestion(ctx context.Context, userID, scheduleID int64, question, model string) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO generated_questions (schedule_id, question, model)
		SELECT sc.id, ?, ? FROM schedules sc
		JOIN cards c ON c.id = sc.card_id
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE sc.id = ? AND t.user_id = ?
		ON CONFLICT(schedule_id) DO UPDATE SET
		    question = excluded.question,
		    model = excluded.model,
		    generated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		question, model, scheduleID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TakeGeneratedQuestion consumes the stored question for an owned schedule:
// read + delete in one transaction, so a question is served at most once.
// ok=false (no error) when there is nothing stored — the caller falls back to
// live generation.
func (s *Store) TakeGeneratedQuestion(ctx context.Context, userID, scheduleID int64) (question string, ok bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT gq.question FROM generated_questions gq
			JOIN schedules sc ON sc.id = gq.schedule_id
			JOIN cards c ON c.id = sc.card_id
			JOIN decks d ON d.id = c.deck_id
			JOIN topics t ON t.id = d.topic_id
			WHERE gq.schedule_id = ? AND t.user_id = ?`, scheduleID, userID).Scan(&question)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM generated_questions WHERE schedule_id = ?`, scheduleID)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return question, true, nil
}

// deleteGeneratedQuestionsForCard invalidates stored questions inside a card
// mutation's own transaction (a question built from the old front/back/note
// must never be served).
func deleteGeneratedQuestionsForCard(ctx context.Context, tx *sql.Tx, cardID int64) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM generated_questions
		WHERE schedule_id IN (SELECT id FROM schedules WHERE card_id = ?)`, cardID)
	return err
}
