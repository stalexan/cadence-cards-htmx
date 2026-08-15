package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TopicImportParams is a parsed topic file ready to insert.
type TopicImportParams struct {
	Topic TopicParams
	Decks []DeckImportParams
}

// DeckImportParams is one deck from a topic file, with its cards.
type DeckImportParams struct {
	Name            string
	Field1Label     *string
	Field2Label     *string
	IsBidirectional bool
	Cards           []ImportCardParams
}

// TopicImportResult reports what was actually created, including any names
// that had to be suffixed.
type TopicImportResult struct {
	TopicID   int64
	TopicName string // final name, suffixed if it collided
	Renamed   bool
	DeckCount int
	CardCount int
	// DeckRenames lists "wanted -> final" for decks the file named twice.
	DeckRenames []string
}

// maxNameSuffix bounds the auto-suffix probe. Reaching it means the user has
// a thousand same-named topics; ErrDuplicate is a saner answer than spinning.
const maxNameSuffix = 1000

// ImportTopic creates a topic with its decks and cards in one transaction.
//
// The topic name is auto-suffixed ("Spanish (2)") rather than failing on a
// collision, so importing the same file twice always succeeds. Deck names are
// suffixed the same way, but only against decks created earlier in this same
// file — the topic is brand new, so no pre-existing deck can collide.
func (s *Store) ImportTopic(ctx context.Context, userID int64, p TopicImportParams) (TopicImportResult, error) {
	var result TopicImportResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		// Probing inside the transaction is safe because store.Open pins
		// SetMaxOpenConns(1): no concurrent writer can claim the name between
		// the probe and the insert. It also avoids leaning on
		// isUniqueViolation, which is an error-string match.
		name, renamed, err := availableTopicName(ctx, tx, userID, p.Topic.Name)
		if err != nil {
			return err
		}
		result.TopicName, result.Renamed = name, renamed

		res, err := tx.ExecContext(ctx, `
			INSERT INTO topics (user_id, name, topic_description, expertise, focus, context_type, example, question)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, name, strOrNil(p.Topic.TopicDescription), strOrNil(p.Topic.Expertise),
			strOrNil(p.Topic.Focus), strOrNil(p.Topic.ContextType), strOrNil(p.Topic.Example),
			strOrNil(p.Topic.Question))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		topicID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		result.TopicID = topicID

		seen := map[string]bool{}
		for _, d := range p.Decks {
			deckName, deckRenamed := uniqueInSet(seen, d.Name)
			seen[deckName] = true
			if deckRenamed {
				result.DeckRenames = append(result.DeckRenames,
					fmt.Sprintf("%q was imported as %q", d.Name, deckName))
			}

			// Reverse params in the file mean the deck is studied both ways,
			// same rule ImportCards applies. The deck is new, so the flag can
			// just be set at insert time — nothing to back-fill.
			bidir := d.IsBidirectional || anyReverse(d.Cards)

			dres, err := tx.ExecContext(ctx, `
				INSERT INTO decks (topic_id, name, field1_label, field2_label, is_bidirectional)
				VALUES (?, ?, ?, ?, ?)`,
				topicID, deckName, strOrNil(d.Field1Label), strOrNil(d.Field2Label), boolInt(bidir))
			if err != nil {
				if isUniqueViolation(err) {
					return ErrDuplicate
				}
				return err
			}
			deckID, err := dres.LastInsertId()
			if err != nil {
				return err
			}

			for _, c := range d.Cards {
				if err := insertCard(ctx, tx, deckID, bidir, c); err != nil {
					return err
				}
			}
			result.DeckCount++
			result.CardCount += len(d.Cards)
		}
		return nil
	})
	if err != nil {
		return TopicImportResult{}, err
	}
	return result, nil
}

// availableTopicName returns the first free name for the user, appending
// " (2)", " (3)", … to the wanted name until no topic holds it.
func availableTopicName(ctx context.Context, tx *sql.Tx, userID int64, want string) (string, bool, error) {
	for n := 1; n <= maxNameSuffix; n++ {
		candidate := want
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)", want, n)
		}
		var one int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM topics WHERE name = ? AND user_id = ?`, candidate, userID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, n > 1, nil
		}
		if err != nil {
			return "", false, err
		}
	}
	return "", false, ErrDuplicate
}

// uniqueInSet returns a name not already in seen, suffixing as needed. Used
// for deck names within a single file.
func uniqueInSet(seen map[string]bool, want string) (string, bool) {
	for n := 1; n <= maxNameSuffix; n++ {
		candidate := want
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)", want, n)
		}
		if !seen[candidate] {
			return candidate, n > 1
		}
	}
	return want, false
}
