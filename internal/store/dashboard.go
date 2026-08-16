package store

import (
	"context"
	"database/sql"
	"time"

	"cadence-cards/internal/sm2"
)

// DashboardStats aggregates the dashboard page numbers (port of
// dashboard-service getDashboardStats). Correct/incorrect counts use forward
// schedules only; due counts respect the bidirectional gate.
func (s *Store) DashboardStats(ctx context.Context, userID int64, now time.Time) (DashboardStats, error) {
	var stats DashboardStats

	counts := []struct {
		q    string
		dest *int
	}{
		{`SELECT COUNT(*) FROM topics WHERE user_id = ?`, &stats.TotalTopics},
		{`SELECT COUNT(*) FROM decks d JOIN topics t ON t.id = d.topic_id WHERE t.user_id = ?`, &stats.TotalDecks},
		{`SELECT COUNT(*) FROM cards c JOIN decks d ON d.id = c.deck_id JOIN topics t ON t.id = d.topic_id WHERE t.user_id = ?`, &stats.TotalCards},
		{`SELECT COUNT(*) FROM schedules s JOIN cards c ON c.id = s.card_id JOIN decks d ON d.id = c.deck_id JOIN topics t ON t.id = d.topic_id
		  WHERE t.user_id = ? AND s.is_reversed = 0 AND s.grade IN ('CORRECT_PERFECT_RECALL','CORRECT_WITH_HESITATION')`, &stats.CardsCorrect},
		{`SELECT COUNT(*) FROM schedules s JOIN cards c ON c.id = s.card_id JOIN decks d ON d.id = c.deck_id JOIN topics t ON t.id = d.topic_id
		  WHERE t.user_id = ? AND s.is_reversed = 0 AND s.grade = 'INCORRECT'`, &stats.CardsIncorrect},
	}
	for _, c := range counts {
		if err := s.db.QueryRowContext(ctx, c.q, userID).Scan(c.dest); err != nil {
			return DashboardStats{}, err
		}
	}

	// Due counts by priority: fetch all schedules and evaluate due-ness in Go
	// (local-midnight day math), skipping reverse schedules of unidirectional decks.
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.is_reversed, s.easiness, s.interval, s.rep_count, s.last_seen, c.priority, d.is_bidirectional
		FROM schedules s
		JOIN cards c ON c.id = s.card_id
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE t.user_id = ?`, userID)
	if err != nil {
		return DashboardStats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var rev, bidir, interval, repCount int
		var easiness float64
		var lastSeen sql.NullString
		var priority string
		if err := rows.Scan(&rev, &easiness, &interval, &repCount, &lastSeen, &priority, &bidir); err != nil {
			return DashboardStats{}, err
		}
		if rev == 1 && bidir != 1 {
			continue
		}
		st := sm2.State{Easiness: easiness, Interval: interval, RepCount: repCount}
		if st.LastSeen, err = nullTime(lastSeen); err != nil {
			return DashboardStats{}, err
		}
		if !st.IsDue(now) {
			continue
		}
		switch sm2.Priority(priority) {
		case sm2.PriorityA:
			stats.DueA++
		case sm2.PriorityB:
			stats.DueB++
		case sm2.PriorityC:
			stats.DueC++
		}
	}
	if err := rows.Err(); err != nil {
		return DashboardStats{}, err
	}

	// Recent activity: 5 most recently reviewed schedules.
	actRows, err := s.db.QueryContext(ctx, `
		SELECT c.id, s.is_reversed, c.front, d.name, s.last_seen
		FROM schedules s
		JOIN cards c ON c.id = s.card_id
		JOIN decks d ON d.id = c.deck_id
		JOIN topics t ON t.id = d.topic_id
		WHERE t.user_id = ? AND s.last_seen IS NOT NULL
		ORDER BY s.last_seen DESC
		LIMIT 5`, userID)
	if err != nil {
		return DashboardStats{}, err
	}
	defer actRows.Close()
	for actRows.Next() {
		var item ActivityItem
		var rev int
		var front, seen string
		if err := actRows.Scan(&item.CardID, &rev, &front, &item.DeckName, &seen); err != nil {
			return DashboardStats{}, err
		}
		item.Action = "Reviewed card"
		if rev == 1 {
			item.Action = "Reviewed card (reverse)"
		}
		// The full front, markdown and all. The dashboard strips it to plain
		// text before truncating (30 runes + ellipsis, like the source) —
		// truncating here would spend the budget on markdown syntax and could
		// cut a "**bold**" in half.
		item.ItemName = front
		if item.Timestamp, err = parseTime(seen); err != nil {
			return DashboardStats{}, err
		}
		stats.RecentActivity = append(stats.RecentActivity, item)
	}
	return stats, actRows.Err()
}
