package store

import (
	"context"
	"database/sql"
	"errors"
)

// TopicParams are the writable topic fields (name + the 7 Claude prompt-config
// columns from the source schema).
type TopicParams struct {
	Name             string
	TopicDescription *string
	Expertise        *string
	Focus            *string
	ContextType      *string
	Example          *string
	Question         *string
}

const topicCols = `id, user_id, name, topic_description, expertise, focus, context_type, example, question, created_at, updated_at`

func scanTopic(scan func(dest ...any) error) (Topic, error) {
	var t Topic
	var desc, exp, focus, ctxType, example, question sql.NullString
	var created, updated string
	if err := scan(&t.ID, &t.UserID, &t.Name, &desc, &exp, &focus, &ctxType, &example, &question, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Topic{}, ErrNotFound
		}
		return Topic{}, err
	}
	t.TopicDescription, t.Expertise, t.Focus = nullStr(desc), nullStr(exp), nullStr(focus)
	t.ContextType, t.Example, t.Question = nullStr(ctxType), nullStr(example), nullStr(question)
	var err error
	if t.CreatedAt, err = parseTime(created); err != nil {
		return Topic{}, err
	}
	if t.UpdatedAt, err = parseTime(updated); err != nil {
		return Topic{}, err
	}
	return t, nil
}

// ListTopics returns the user's topics with deck and card counts, newest
// first (port of topic-service getTopics).
func (s *Store) ListTopics(ctx context.Context, userID int64) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.user_id, t.name, t.topic_description, t.expertise, t.focus,
		       t.context_type, t.example, t.question, t.created_at, t.updated_at,
		       COUNT(DISTINCT d.id) AS deck_count,
		       COUNT(DISTINCT c.id) AS card_count
		FROM topics t
		LEFT JOIN decks d ON d.topic_id = t.id
		LEFT JOIN cards c ON c.deck_id = d.id
		WHERE t.user_id = ?
		GROUP BY t.id
		ORDER BY t.created_at DESC, t.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var t Topic
		var desc, exp, focus, ctxType, example, question sql.NullString
		var created, updated string
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &desc, &exp, &focus, &ctxType, &example, &question,
			&created, &updated, &t.DeckCount, &t.CardCount); err != nil {
			return nil, err
		}
		t.TopicDescription, t.Expertise, t.Focus = nullStr(desc), nullStr(exp), nullStr(focus)
		t.ContextType, t.Example, t.Question = nullStr(ctxType), nullStr(example), nullStr(question)
		if t.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if t.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		topics = append(topics, t)
	}
	return topics, rows.Err()
}

// GetTopic fetches one topic owned by the user.
func (s *Store) GetTopic(ctx context.Context, userID, topicID int64) (Topic, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+topicCols+` FROM topics WHERE id = ? AND user_id = ?`, topicID, userID)
	return scanTopic(row.Scan)
}

// CreateTopic inserts a topic. Returns ErrDuplicate on a name collision
// within the user's topics.
func (s *Store) CreateTopic(ctx context.Context, userID int64, p TopicParams) (Topic, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO topics (user_id, name, topic_description, expertise, focus, context_type, example, question)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, p.Name, strOrNil(p.TopicDescription), strOrNil(p.Expertise), strOrNil(p.Focus),
		strOrNil(p.ContextType), strOrNil(p.Example), strOrNil(p.Question))
	if err != nil {
		if isUniqueViolation(err) {
			return Topic{}, ErrDuplicate
		}
		return Topic{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Topic{}, err
	}
	return s.GetTopic(ctx, userID, id)
}

// UpdateTopic updates a topic owned by the user.
func (s *Store) UpdateTopic(ctx context.Context, userID, topicID int64, p TopicParams) (Topic, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE topics SET name = ?, topic_description = ?, expertise = ?, focus = ?,
		       context_type = ?, example = ?, question = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND user_id = ?`,
		p.Name, strOrNil(p.TopicDescription), strOrNil(p.Expertise), strOrNil(p.Focus),
		strOrNil(p.ContextType), strOrNil(p.Example), strOrNil(p.Question), topicID, userID)
	if err != nil {
		if isUniqueViolation(err) {
			return Topic{}, ErrDuplicate
		}
		return Topic{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Topic{}, ErrNotFound
	}
	return s.GetTopic(ctx, userID, topicID)
}

// DeleteTopic removes a topic owned by the user (cascades to decks/cards).
func (s *Store) DeleteTopic(ctx context.Context, userID, topicID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM topics WHERE id = ? AND user_id = ?`, topicID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
