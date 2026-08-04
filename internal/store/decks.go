package store

import (
	"context"
	"database/sql"
	"errors"
)

// DeckParams are the writable deck fields.
type DeckParams struct {
	Name            string
	TopicID         int64
	Field1Label     *string
	Field2Label     *string
	IsBidirectional bool
}

const deckSelect = `
	SELECT d.id, d.topic_id, t.name, d.name, d.field1_label, d.field2_label,
	       d.is_bidirectional, d.created_at, d.updated_at,
	       (SELECT COUNT(*) FROM cards c WHERE c.deck_id = d.id) AS card_count
	FROM decks d
	JOIN topics t ON t.id = d.topic_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanDeck(row rowScanner) (Deck, error) {
	var d Deck
	var f1, f2 sql.NullString
	var bidir int
	var created, updated string
	if err := row.Scan(&d.ID, &d.TopicID, &d.TopicName, &d.Name, &f1, &f2, &bidir, &created, &updated, &d.CardCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Deck{}, ErrNotFound
		}
		return Deck{}, err
	}
	d.Field1Label, d.Field2Label = nullStr(f1), nullStr(f2)
	d.IsBidirectional = bidir == 1
	var err error
	if d.CreatedAt, err = parseTime(created); err != nil {
		return Deck{}, err
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return Deck{}, err
	}
	return d, nil
}

// ListDecks returns the user's decks (optionally topic-filtered) with card
// counts, newest first (port of deck-service getDecks).
func (s *Store) ListDecks(ctx context.Context, userID int64, topicID *int64) ([]Deck, error) {
	q := deckSelect + ` WHERE t.user_id = ?`
	args := []any{userID}
	if topicID != nil {
		q += ` AND d.topic_id = ?`
		args = append(args, *topicID)
	}
	q += ` ORDER BY d.created_at DESC, d.id DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decks []Deck
	for rows.Next() {
		d, err := scanDeck(rows)
		if err != nil {
			return nil, err
		}
		decks = append(decks, d)
	}
	return decks, rows.Err()
}

// GetDeck fetches one deck owned by the user.
func (s *Store) GetDeck(ctx context.Context, userID, deckID int64) (Deck, error) {
	row := s.db.QueryRowContext(ctx, deckSelect+` WHERE d.id = ? AND t.user_id = ?`, deckID, userID)
	return scanDeck(row)
}

// CreateDeck inserts a deck after verifying topic ownership in the same
// transaction. Returns ErrDuplicate on a name collision within the topic.
func (s *Store) CreateDeck(ctx context.Context, userID int64, p DeckParams) (Deck, error) {
	var deckID int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM topics WHERE id = ? AND user_id = ?`, p.TopicID, userID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO decks (topic_id, name, field1_label, field2_label, is_bidirectional)
			VALUES (?, ?, ?, ?, ?)`,
			p.TopicID, p.Name, strOrNil(p.Field1Label), strOrNil(p.Field2Label), boolInt(p.IsBidirectional))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		deckID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Deck{}, err
	}
	return s.GetDeck(ctx, userID, deckID)
}

// UpdateDeck updates a deck owned by the user. When flipping isBidirectional
// on, reverse schedules are back-filled for cards lacking one, in the same
// transaction (port of deck-service updateDeck). Disabling leaves reverse
// schedules dormant.
func (s *Store) UpdateDeck(ctx context.Context, userID, deckID int64, p DeckParams) (Deck, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var wasBidir int
		err := tx.QueryRowContext(ctx, `
			SELECT d.is_bidirectional FROM decks d
			JOIN topics t ON t.id = d.topic_id
			WHERE d.id = ? AND t.user_id = ?`, deckID, userID).Scan(&wasBidir)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		if p.IsBidirectional && wasBidir == 0 {
			// Back-fill reverse schedules for cards that don't have one.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO schedules (card_id, is_reversed)
				SELECT c.id, 1 FROM cards c
				WHERE c.deck_id = ?
				  AND NOT EXISTS (SELECT 1 FROM schedules s WHERE s.card_id = c.id AND s.is_reversed = 1)`,
				deckID); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE decks SET name = ?, field1_label = ?, field2_label = ?, is_bidirectional = ?,
			       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ?`,
			p.Name, strOrNil(p.Field1Label), strOrNil(p.Field2Label), boolInt(p.IsBidirectional), deckID); err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Deck{}, err
	}
	return s.GetDeck(ctx, userID, deckID)
}

// DeleteDeck removes a deck owned by the user (cascades to cards/schedules).
func (s *Store) DeleteDeck(ctx context.Context, userID, deckID int64) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM decks WHERE id = ?
		  AND topic_id IN (SELECT id FROM topics WHERE user_id = ?)`, deckID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
