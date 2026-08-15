package store

import (
	"context"
	"database/sql"
	"errors"

	"cadence-cards/internal/sm2"
)

// ImportCardParams is one card to import, with optional SM-2 state carried
// over from the YAML (Reverse is nil when no reverse params were present).
type ImportCardParams struct {
	Front    string
	Back     string
	Note     *string
	Priority sm2.Priority
	Tags     []string
	Forward  sm2.State
	Reverse  *sm2.State
}

// ImportCards creates all cards (with schedules) in one transaction after
// verifying deck ownership (port of import-service importCards). A reverse
// schedule is created when the deck is bidirectional or the YAML carried
// reverse SM-2 params.
//
// Reverse params in the file are a statement that the deck is studied both
// ways, so a unidirectional deck is switched to bidirectional (back-filling
// reverse schedules for its existing cards, as UpdateDeck does) — otherwise
// dueSchedules would silently skip everything just imported. The returned bool
// reports whether that switch happened.
func (s *Store) ImportCards(ctx context.Context, userID, deckID int64, cards []ImportCardParams) (bool, error) {
	var madeBidirectional bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var bidir int
		err := tx.QueryRowContext(ctx, `
			SELECT d.is_bidirectional FROM decks d
			JOIN topics t ON t.id = d.topic_id
			WHERE d.id = ? AND t.user_id = ?`, deckID, userID).Scan(&bidir)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		if bidir == 0 && anyReverse(cards) {
			// Back-fill reverse schedules for cards already in the deck; the
			// imported ones get theirs in the loop below.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO schedules (card_id, is_reversed)
				SELECT c.id, 1 FROM cards c
				WHERE c.deck_id = ?
				  AND NOT EXISTS (SELECT 1 FROM schedules s WHERE s.card_id = c.id AND s.is_reversed = 1)`,
				deckID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE decks SET is_bidirectional = 1,
				       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
				WHERE id = ?`, deckID); err != nil {
				return err
			}
			bidir = 1
			madeBidirectional = true
		}

		for _, p := range cards {
			if err := insertCard(ctx, tx, deckID, bidir == 1, p); err != nil {
				return err
			}
		}
		return nil
	})
	return madeBidirectional, err
}

// insertCard inserts one imported card and its schedules inside an open
// transaction. A reverse schedule is created when the deck is bidirectional or
// the card carried reverse SM-2 params. Shared by ImportCards and ImportTopic.
func insertCard(ctx context.Context, tx *sql.Tx, deckID int64, bidir bool, p ImportCardParams) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO cards (deck_id, front, back, note, priority, tags)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deckID, p.Front, p.Back, strOrNil(p.Note), string(p.Priority), encodeTags(p.Tags))
	if err != nil {
		return err
	}
	cardID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schedules (card_id, is_reversed) VALUES (?, 0)`, cardID); err != nil {
		return err
	}
	if err := setScheduleState(ctx, tx, cardID, false, p.Forward); err != nil {
		return err
	}

	if bidir || p.Reverse != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schedules (card_id, is_reversed) VALUES (?, 1)`, cardID); err != nil {
			return err
		}
		rev := sm2.InitialState()
		if p.Reverse != nil {
			rev = *p.Reverse
		}
		if err := setScheduleState(ctx, tx, cardID, true, rev); err != nil {
			return err
		}
	}
	return nil
}

// anyReverse reports whether any card carried reverse SM-2 params.
func anyReverse(cards []ImportCardParams) bool {
	for _, p := range cards {
		if p.Reverse != nil {
			return true
		}
	}
	return false
}
