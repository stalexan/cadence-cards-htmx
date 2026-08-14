package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ConversationTTL is how long an untouched conversation is kept before the
// hourly maintenance ticker prunes it.
const ConversationTTL = 7 * 24 * time.Hour

// Conversation is one server-owned chat transcript. There is no user_id
// column: ownership always resolves through topic_id → topics.user_id, per the
// authorization-in-the-transaction rule.
type Conversation struct {
	ID         int64
	TopicID    int64
	ScheduleID *int64 // nil = free-form topic chat
	UpdatedAt  time.Time
}

// ChatMessage is one stored chat turn.
type ChatMessage struct {
	ID             int64
	ConversationID int64
	Role           string // "user" | "assistant"
	Content        string
}

// CreateConversation opens a transcript for a topic (scheduleID nil) or a
// study card (scheduleID set), optionally seeding initial messages, all in one
// transaction. The insert itself carries the ownership scope — and, when a
// schedule is given, the schedule-belongs-to-topic check — so a non-owned or
// mismatched reference inserts zero rows and returns ErrNotFound.
func (s *Store) CreateConversation(ctx context.Context, userID, topicID int64, scheduleID *int64, initial []ChatMessage) (int64, error) {
	var convID int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if scheduleID == nil {
			res, err = tx.ExecContext(ctx, `
				INSERT INTO conversations (topic_id, schedule_id)
				SELECT t.id, NULL FROM topics t WHERE t.id = ? AND t.user_id = ?`,
				topicID, userID)
		} else {
			res, err = tx.ExecContext(ctx, `
				INSERT INTO conversations (topic_id, schedule_id)
				SELECT t.id, s.id FROM schedules s
				JOIN cards c ON c.id = s.card_id
				JOIN decks d ON d.id = c.deck_id
				JOIN topics t ON t.id = d.topic_id
				WHERE t.id = ? AND t.user_id = ? AND s.id = ?`,
				topicID, userID, *scheduleID)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if convID, err = res.LastInsertId(); err != nil {
			return err
		}
		return appendMessagesTx(ctx, tx, convID, initial)
	})
	if err != nil {
		return 0, err
	}
	return convID, nil
}

// AppendChatMessages appends turns to an owned conversation and bumps its
// updated_at (which drives the TTL cleanup).
func (s *Store) AppendChatMessages(ctx context.Context, userID, convID int64, msgs []ChatMessage) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE conversations SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND topic_id IN (SELECT id FROM topics WHERE user_id = ?)`,
			convID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return appendMessagesTx(ctx, tx, convID, msgs)
	})
}

func appendMessagesTx(ctx context.Context, tx *sql.Tx, convID int64, msgs []ChatMessage) error {
	for _, m := range msgs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_messages (conversation_id, role, content) VALUES (?, ?, ?)`,
			convID, m.Role, m.Content); err != nil {
			return err
		}
	}
	return nil
}

// GetConversationMessages loads an owned conversation and its transcript in
// message order. Missing-or-not-owned is ErrNotFound.
func (s *Store) GetConversationMessages(ctx context.Context, userID, convID int64) (Conversation, []ChatMessage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT cv.id, cv.topic_id, cv.schedule_id, cv.updated_at
		FROM conversations cv
		JOIN topics t ON t.id = cv.topic_id
		WHERE cv.id = ? AND t.user_id = ?`, convID, userID)
	conv, err := scanConversation(row)
	if err != nil {
		return Conversation{}, nil, err
	}
	msgs, err := s.conversationMessages(ctx, convID)
	return conv, msgs, err
}

// LatestConversationForSchedule returns the most recently touched conversation
// for a schedule with its transcript, or (Conversation{}, nil, nil) when the
// card has never been chatted about. Used to restore the chat on a mid-card
// refresh.
func (s *Store) LatestConversationForSchedule(ctx context.Context, userID, scheduleID int64) (Conversation, []ChatMessage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT cv.id, cv.topic_id, cv.schedule_id, cv.updated_at
		FROM conversations cv
		JOIN topics t ON t.id = cv.topic_id
		WHERE cv.schedule_id = ? AND t.user_id = ?
		ORDER BY cv.updated_at DESC, cv.id DESC LIMIT 1`, scheduleID, userID)
	conv, err := scanConversation(row)
	if errors.Is(err, ErrNotFound) {
		return Conversation{}, nil, nil
	}
	if err != nil {
		return Conversation{}, nil, err
	}
	msgs, err := s.conversationMessages(ctx, conv.ID)
	return conv, msgs, err
}

func scanConversation(row *sql.Row) (Conversation, error) {
	var conv Conversation
	var schedID sql.NullInt64
	var updated string
	err := row.Scan(&conv.ID, &conv.TopicID, &schedID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if schedID.Valid {
		conv.ScheduleID = &schedID.Int64
	}
	if conv.UpdatedAt, err = parseTime(updated); err != nil {
		return Conversation{}, err
	}
	return conv, nil
}

func (s *Store) conversationMessages(ctx context.Context, convID int64) ([]ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, role, content FROM chat_messages
		WHERE conversation_id = ? ORDER BY id`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// DeleteStaleConversations prunes conversations untouched since cutoff
// (messages cascade). Called from the hourly maintenance ticker.
func (s *Store) DeleteStaleConversations(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM conversations WHERE updated_at < ?`, fmtTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
