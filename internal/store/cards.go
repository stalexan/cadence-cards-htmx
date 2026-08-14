package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"cadence-cards/internal/sm2"
)

// CardParams are the writable card content fields.
type CardParams struct {
	DeckID   int64
	Front    string
	Back     string
	Note     *string
	Priority sm2.Priority
	Tags     []string
}

// CardListParams filter/sort/paginate the cards list (replaces the Svelte
// client-side filtering with SQL).
type CardListParams struct {
	Search   string
	TopicID  *int64
	DeckID   *int64
	Priority string
	Tag      string
	Sort     string // front | priority | deck | lastSeen
	Dir      string // asc | desc
	Page     int    // 1-based
	PerPage  int
}

func encodeTags(tags []string) string {
	if tags == nil {
		tags = []string{}
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func decodeTags(s string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil || tags == nil {
		return []string{}
	}
	return tags
}

func scanSchedule(row rowScanner) (Schedule, error) {
	var sc Schedule
	var rev int
	var grade, lastSeen sql.NullString
	var created, updated string
	if err := row.Scan(&sc.ID, &sc.CardID, &rev, &sc.Easiness, &sc.Interval, &sc.RepCount,
		&grade, &lastSeen, &sc.Version, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, err
	}
	sc.IsReversed = rev == 1
	if grade.Valid {
		g := sm2.Grade(grade.String)
		sc.Grade = &g
	}
	var err error
	if sc.LastSeen, err = nullTime(lastSeen); err != nil {
		return Schedule{}, err
	}
	if sc.CreatedAt, err = parseTime(created); err != nil {
		return Schedule{}, err
	}
	if sc.UpdatedAt, err = parseTime(updated); err != nil {
		return Schedule{}, err
	}
	return sc, nil
}

const scheduleCols = `id, card_id, is_reversed, easiness, interval, rep_count, grade, last_seen, version, created_at, updated_at`

// loadSchedules fetches all schedules for a set of card IDs, forward first.
func (s *Store) loadSchedules(ctx context.Context, cardIDs []int64) (map[int64][]Schedule, error) {
	if len(cardIDs) == 0 {
		return map[int64][]Schedule{}, nil
	}
	placeholders := ""
	args := make([]any, len(cardIDs))
	for i, id := range cardIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE card_id IN (`+placeholders+`) ORDER BY card_id, is_reversed`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64][]Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out[sc.CardID] = append(out[sc.CardID], sc)
	}
	return out, rows.Err()
}

const cardSelect = `
	SELECT c.id, c.deck_id, d.name, d.topic_id, t.name, c.front, c.back, c.note,
	       c.priority, c.tags, c.version, c.created_at, c.updated_at, d.is_bidirectional
	FROM cards c
	JOIN decks d ON d.id = c.deck_id
	JOIN topics t ON t.id = d.topic_id`

func scanCard(row rowScanner) (Card, error) {
	var c Card
	var note sql.NullString
	var priority, tags string
	var bidir int
	var created, updated string
	if err := row.Scan(&c.ID, &c.DeckID, &c.DeckName, &c.TopicID, &c.TopicName, &c.Front, &c.Back,
		&note, &priority, &tags, &c.Version, &created, &updated, &bidir); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Card{}, ErrNotFound
		}
		return Card{}, err
	}
	c.Note = nullStr(note)
	c.Priority = sm2.Priority(priority)
	c.Tags = decodeTags(tags)
	c.IsBidirectional = bidir == 1
	var err error
	if c.CreatedAt, err = parseTime(created); err != nil {
		return Card{}, err
	}
	if c.UpdatedAt, err = parseTime(updated); err != nil {
		return Card{}, err
	}
	return c, nil
}

// ListCards returns a filtered, sorted page of the user's cards plus the total
// match count. Schedules are attached to each returned card.
func (s *Store) ListCards(ctx context.Context, userID int64, p CardListParams) ([]Card, int, error) {
	where := ` WHERE t.user_id = ?`
	args := []any{userID}

	if p.Search != "" {
		where += ` AND (c.front LIKE ? ESCAPE '\' OR c.back LIKE ? ESCAPE '\' OR c.note LIKE ? ESCAPE '\')`
		pat := "%" + escapeLike(p.Search) + "%"
		args = append(args, pat, pat, pat)
	}
	if p.TopicID != nil {
		where += ` AND d.topic_id = ?`
		args = append(args, *p.TopicID)
	}
	if p.DeckID != nil {
		where += ` AND c.deck_id = ?`
		args = append(args, *p.DeckID)
	}
	if p.Priority != "" {
		where += ` AND c.priority = ?`
		args = append(args, p.Priority)
	}
	if p.Tag != "" {
		where += ` AND EXISTS (SELECT 1 FROM json_each(c.tags) je WHERE je.value = ?)`
		args = append(args, p.Tag)
	}

	var total int
	countQ := `SELECT COUNT(*) FROM cards c JOIN decks d ON d.id = c.deck_id JOIN topics t ON t.id = d.topic_id` + where
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dir := "ASC"
	if p.Dir == "desc" {
		dir = "DESC"
	}
	var orderBy string
	switch p.Sort {
	case "priority":
		orderBy = "c.priority " + dir + ", c.front COLLATE NOCASE ASC"
	case "deck":
		orderBy = "d.name COLLATE NOCASE " + dir + ", c.front COLLATE NOCASE ASC"
	case "lastSeen":
		// Forward-schedule last_seen; NULLs (never seen) sort first on asc.
		orderBy = `(SELECT sc.last_seen FROM schedules sc WHERE sc.card_id = c.id AND sc.is_reversed = 0) ` + dir + `, c.front COLLATE NOCASE ASC`
	default: // "front"
		orderBy = "c.front COLLATE NOCASE " + dir
	}

	q := cardSelect + where + ` ORDER BY ` + orderBy
	if p.PerPage > 0 {
		page := max(1, p.Page)
		q += fmt.Sprintf(` LIMIT %d OFFSET %d`, p.PerPage, (page-1)*p.PerPage)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var cards []Card
	var ids []int64
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, 0, err
		}
		cards = append(cards, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	schedules, err := s.loadSchedules(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range cards {
		cards[i].Schedules = schedules[cards[i].ID]
	}
	return cards, total, nil
}

// escapeLike escapes LIKE wildcards in user-supplied search text.
func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '%', '_', '\\':
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// GetCard fetches one card owned by the user, with its schedules.
func (s *Store) GetCard(ctx context.Context, userID, cardID int64) (Card, error) {
	row := s.db.QueryRowContext(ctx, cardSelect+` WHERE c.id = ? AND t.user_id = ?`, cardID, userID)
	c, err := scanCard(row)
	if err != nil {
		return Card{}, err
	}
	schedules, err := s.loadSchedules(ctx, []int64{c.ID})
	if err != nil {
		return Card{}, err
	}
	c.Schedules = schedules[c.ID]
	return c, nil
}

// CreateCard inserts a card after verifying deck ownership in the same
// transaction, creating one forward schedule plus a reverse one when the deck
// is bidirectional (port of card-service createCard).
func (s *Store) CreateCard(ctx context.Context, userID int64, p CardParams) (Card, error) {
	var cardID int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var bidir int
		err := tx.QueryRowContext(ctx, `
			SELECT d.is_bidirectional FROM decks d
			JOIN topics t ON t.id = d.topic_id
			WHERE d.id = ? AND t.user_id = ?`, p.DeckID, userID).Scan(&bidir)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO cards (deck_id, front, back, note, priority, tags)
			VALUES (?, ?, ?, ?, ?, ?)`,
			p.DeckID, p.Front, p.Back, strOrNil(p.Note), string(p.Priority), encodeTags(p.Tags))
		if err != nil {
			return err
		}
		if cardID, err = res.LastInsertId(); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schedules (card_id, is_reversed) VALUES (?, 0)`, cardID); err != nil {
			return err
		}
		if bidir == 1 {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO schedules (card_id, is_reversed) VALUES (?, 1)`, cardID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Card{}, err
	}
	return s.GetCard(ctx, userID, cardID)
}

// UpdateCard updates card content with optimistic locking: the update only
// applies when version matches, else ErrVersionConflict. A deck move is
// ownership-checked in the same transaction.
func (s *Store) UpdateCard(ctx context.Context, userID, cardID int64, version int, p CardParams) (Card, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `
			SELECT 1 FROM cards c
			JOIN decks d ON d.id = c.deck_id
			JOIN topics t ON t.id = d.topic_id
			WHERE c.id = ? AND t.user_id = ?`, cardID, userID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		// Verify the target deck belongs to the user (card may move decks).
		var bidir int
		err = tx.QueryRowContext(ctx, `
			SELECT d.is_bidirectional FROM decks d
			JOIN topics t ON t.id = d.topic_id
			WHERE d.id = ? AND t.user_id = ?`, p.DeckID, userID).Scan(&bidir)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE cards SET deck_id = ?, front = ?, back = ?, note = ?, priority = ?, tags = ?,
			       version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND version = ?`,
			p.DeckID, p.Front, p.Back, strOrNil(p.Note), string(p.Priority), encodeTags(p.Tags),
			cardID, version)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Row exists (checked above) but version didn't match.
			return ErrVersionConflict
		}

		// The edit may have changed front/back/note, so any pre-generated
		// question built from the old content is stale.
		if err := deleteGeneratedQuestionsForCard(ctx, tx, cardID); err != nil {
			return err
		}

		// A move into a bidirectional deck must back-fill the reverse
		// schedule, or the card is silently never offered in reverse (a
		// pre-existing dormant one is left as is, matching UpdateDeck).
		if bidir == 1 {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO schedules (card_id, is_reversed)
				SELECT ?, 1
				WHERE NOT EXISTS (SELECT 1 FROM schedules s WHERE s.card_id = ? AND s.is_reversed = 1)`,
				cardID, cardID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Card{}, err
	}
	return s.GetCard(ctx, userID, cardID)
}

// DeleteCard removes a card owned by the user (cascades to schedules).
func (s *Store) DeleteCard(ctx context.Context, userID, cardID int64) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM cards WHERE id = ? AND deck_id IN (
			SELECT d.id FROM decks d JOIN topics t ON t.id = d.topic_id WHERE t.user_id = ?)`,
		cardID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DistinctTags returns all distinct tags across the user's cards (filter
// dropdown on the cards page).
func (s *Store) DistinctTags(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT je.value FROM cards c
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		JOIN json_each(c.tags) je
		WHERE t.user_id = ?
		ORDER BY je.value COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}
