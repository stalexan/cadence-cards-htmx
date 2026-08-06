package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"time"

	"cadence-cards/internal/sm2"
)

// StudyFilter holds the study-session parameters from the setup page. Unlike
// the source app, all of these are honored: every repeated deckIds value,
// the priority filter, and includeNew.
type StudyFilter struct {
	DeckIDs    []int64
	Priority   string // "" = all priorities
	IncludeNew bool   // false excludes never-seen schedules
}

// dueSchedules fetches every candidate schedule for the topic/filter and
// returns the due ones, applying the reverse-schedule bidirectional gate and
// the includeNew filter (port of getDueSchedulesForPriority, generalized).
func (s *Store) dueSchedules(ctx context.Context, userID, topicID int64, f StudyFilter, priority sm2.Priority, now time.Time) ([]StudyItem, error) {
	q := `
		SELECT s.id, s.is_reversed, s.easiness, s.interval, s.rep_count, s.grade, s.last_seen, s.version,
		       c.id, c.front, c.back, c.note, c.priority, c.tags,
		       d.id, d.name, d.field1_label, d.field2_label, d.is_bidirectional
		FROM schedules s
		JOIN cards c ON c.id = s.card_id
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE t.user_id = ? AND t.id = ? AND c.priority = ?`
	args := []any{userID, topicID, string(priority)}
	if len(f.DeckIDs) > 0 {
		q += ` AND d.id IN (`
		for i, id := range f.DeckIDs {
			if i > 0 {
				q += `,`
			}
			q += `?`
			args = append(args, id)
		}
		q += `)`
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var due []StudyItem
	for rows.Next() {
		var (
			scID, cardID, deckID        int64
			rev, bidir                  int
			easiness                    float64
			interval, repCount, version int
			grade, lastSeen, note       sql.NullString
			front, back, priorityS      string
			tagsJSON, deckName          string
			f1, f2                      sql.NullString
		)
		if err := rows.Scan(&scID, &rev, &easiness, &interval, &repCount, &grade, &lastSeen, &version,
			&cardID, &front, &back, &note, &priorityS, &tagsJSON,
			&deckID, &deckName, &f1, &f2, &bidir); err != nil {
			return nil, err
		}

		isReversed := rev == 1
		// Skip reverse schedules for non-bidirectional decks.
		if isReversed && bidir != 1 {
			continue
		}

		st := sm2.State{Easiness: easiness, Interval: interval, RepCount: repCount}
		if grade.Valid {
			g := sm2.Grade(grade.String)
			st.Grade = &g
		}
		if st.LastSeen, err = nullTime(lastSeen); err != nil {
			return nil, err
		}

		// includeNew=false: skip never-seen schedules.
		if !f.IncludeNew && st.LastSeen == nil {
			continue
		}
		if !st.IsDue(now) {
			continue
		}

		// Resolve prompt/answer by direction (port of formatStudyCard).
		deck := Deck{Field1Label: nullStr(f1), Field2Label: nullStr(f2)}
		field1, field2 := deck.FieldLabels()
		item := StudyItem{
			CardID:      cardID,
			ScheduleID:  scID,
			Prompt:      front,
			Answer:      back,
			PromptLabel: field1,
			AnswerLabel: field2,
			Note:        nullStr(note),
			Priority:    sm2.Priority(priorityS),
			IsReversed:  isReversed,
			DeckID:      deckID,
			DeckName:    deckName,
			Tags:        decodeTags(tagsJSON),
			State:       st,
			Version:     version,
		}
		if isReversed {
			item.Prompt, item.Answer = back, front
			item.PromptLabel, item.AnswerLabel = field2, field1
		}
		due = append(due, item)
	}
	return due, rows.Err()
}

// GetStudyItem loads one schedule as a direction-resolved StudyItem
// (ownership-checked). Used by the study fragments, which reference the
// current card by scheduleId so the browser never supplies card content.
func (s *Store) GetStudyItem(ctx context.Context, userID, scheduleID int64) (StudyItem, int64, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.is_reversed, s.easiness, s.interval, s.rep_count, s.grade, s.last_seen, s.version,
		       c.id, c.front, c.back, c.note, c.priority, c.tags,
		       d.id, d.name, d.field1_label, d.field2_label, t.id
		FROM schedules s
		JOIN cards c ON c.id = s.card_id
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE s.id = ? AND t.user_id = ?`, scheduleID, userID)

	var (
		scID, cardID, deckID, topicID int64
		rev                           int
		easiness                      float64
		interval, repCount, version   int
		grade, lastSeen, note         sql.NullString
		front, back, priorityS        string
		tagsJSON, deckName            string
		f1, f2                        sql.NullString
	)
	err := row.Scan(&scID, &rev, &easiness, &interval, &repCount, &grade, &lastSeen, &version,
		&cardID, &front, &back, &note, &priorityS, &tagsJSON,
		&deckID, &deckName, &f1, &f2, &topicID)
	if errors.Is(err, sql.ErrNoRows) {
		return StudyItem{}, 0, ErrNotFound
	}
	if err != nil {
		return StudyItem{}, 0, err
	}

	st := sm2.State{Easiness: easiness, Interval: interval, RepCount: repCount}
	if grade.Valid {
		g := sm2.Grade(grade.String)
		st.Grade = &g
	}
	if st.LastSeen, err = nullTime(lastSeen); err != nil {
		return StudyItem{}, 0, err
	}

	deck := Deck{Field1Label: nullStr(f1), Field2Label: nullStr(f2)}
	field1, field2 := deck.FieldLabels()
	item := StudyItem{
		CardID:      cardID,
		ScheduleID:  scID,
		Prompt:      front,
		Answer:      back,
		PromptLabel: field1,
		AnswerLabel: field2,
		Note:        nullStr(note),
		Priority:    sm2.Priority(priorityS),
		IsReversed:  rev == 1,
		DeckID:      deckID,
		DeckName:    deckName,
		Tags:        decodeTags(tagsJSON),
		State:       st,
		Version:     version,
	}
	if item.IsReversed {
		item.Prompt, item.Answer = back, front
		item.PromptLabel, item.AnswerLabel = field2, field1
	}
	return item, topicID, nil
}

// verifyTopicOwnership returns ErrNotFound unless the topic belongs to userID.
func (s *Store) verifyTopicOwnership(ctx context.Context, userID, topicID int64) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM topics WHERE id = ? AND user_id = ?`, topicID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// NextDue picks the next schedule to study: iterate priorities A→B→C (or only
// the filtered priority), and within the first non-empty band pick one due
// schedule at random (port of study-service getNextCard). Returns
// (nil, nil) when nothing is due.
func (s *Store) NextDue(ctx context.Context, userID, topicID int64, f StudyFilter, now time.Time) (*StudyItem, error) {
	if err := s.verifyTopicOwnership(ctx, userID, topicID); err != nil {
		return nil, err
	}

	priorities := sm2.Priorities
	if f.Priority != "" {
		priorities = []sm2.Priority{sm2.Priority(f.Priority)}
	}
	for _, priority := range priorities {
		due, err := s.dueSchedules(ctx, userID, topicID, f, priority, now)
		if err != nil {
			return nil, err
		}
		if len(due) > 0 {
			item := due[rand.Intn(len(due))]
			return &item, nil
		}
	}
	return nil, nil
}

// StudyStats returns total card and due-by-priority counts for a topic/deck
// selection (port of study-service getStudyStats; due counts are study items,
// i.e. schedules, not cards). The priority/includeNew filters are ignored here
// on purpose — the setup page shows the full topic/deck picture.
func (s *Store) StudyStats(ctx context.Context, userID, topicID int64, deckIDs []int64, now time.Time) (StudyStats, error) {
	if err := s.verifyTopicOwnership(ctx, userID, topicID); err != nil {
		return StudyStats{}, err
	}

	countQ := `
		SELECT COUNT(*) FROM cards c
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE t.user_id = ? AND t.id = ?`
	args := []any{userID, topicID}
	if len(deckIDs) > 0 {
		countQ += ` AND d.id IN (`
		for i, id := range deckIDs {
			if i > 0 {
				countQ += `,`
			}
			countQ += `?`
			args = append(args, id)
		}
		countQ += `)`
	}
	var stats StudyStats
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&stats.TotalCards); err != nil {
		return StudyStats{}, err
	}

	all := StudyFilter{DeckIDs: deckIDs, IncludeNew: true}
	for _, priority := range sm2.Priorities {
		due, err := s.dueSchedules(ctx, userID, topicID, all, priority, now)
		if err != nil {
			return StudyStats{}, err
		}
		stats.DueTotal += len(due)
		switch priority {
		case sm2.PriorityA:
			stats.DueA = len(due)
		case sm2.PriorityB:
			stats.DueB = len(due)
		case sm2.PriorityC:
			stats.DueC = len(due)
		}
	}
	return stats, nil
}

// CountDue returns the number of due study items matching a session filter —
// used for the session progress bar total and the session-complete check.
func (s *Store) CountDue(ctx context.Context, userID, topicID int64, f StudyFilter, now time.Time) (int, error) {
	if err := s.verifyTopicOwnership(ctx, userID, topicID); err != nil {
		return 0, err
	}
	priorities := sm2.Priorities
	if f.Priority != "" {
		priorities = []sm2.Priority{sm2.Priority(f.Priority)}
	}
	total := 0
	for _, priority := range priorities {
		due, err := s.dueSchedules(ctx, userID, topicID, f, priority, now)
		if err != nil {
			return 0, err
		}
		total += len(due)
	}
	return total, nil
}

// DueByPriority is a topic's due counts split by card priority, plus the total.
type DueByPriority struct {
	A     int
	B     int
	C     int
	Total int
}

// TopicDueCounts returns due study-item counts per topic for the study index
// page, keyed by topic ID. The split is per priority because the index shows
// a chip per non-empty priority alongside the total.
func (s *Store) TopicDueCounts(ctx context.Context, userID int64, now time.Time) (map[int64]DueByPriority, error) {
	topics, err := s.ListTopics(ctx, userID)
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]DueByPriority, len(topics))
	for _, t := range topics {
		var d DueByPriority
		for _, p := range sm2.Priorities {
			n, err := s.CountDue(ctx, userID, t.ID, StudyFilter{Priority: string(p), IncludeNew: true}, now)
			if err != nil {
				return nil, err
			}
			switch p {
			case sm2.PriorityA:
				d.A = n
			case sm2.PriorityB:
				d.B = n
			case sm2.PriorityC:
				d.C = n
			}
			d.Total += n
		}
		counts[t.ID] = d
	}
	return counts, nil
}
