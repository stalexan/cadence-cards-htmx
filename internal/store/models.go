package store

import (
	"time"

	"cadence-cards/internal/sm2"
)

// User is an application account.
type User struct {
	ID        int64
	Name      *string
	Email     *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Topic groups decks and carries the Claude prompt configuration.
type Topic struct {
	ID               int64
	UserID           int64
	Name             string
	TopicDescription *string
	Expertise        *string
	Focus            *string
	ContextType      *string
	Example          *string
	Question         *string
	// Provenance: where this topic came from. Free text, unused by
	// internal/claude, carried through YAML import/export.
	Author    *string
	License   *string
	Source    *string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Denormalized counts (topic list page).
	DeckCount int
	CardCount int
}

// Deck groups cards within a topic.
type Deck struct {
	ID              int64
	TopicID         int64
	TopicName       string
	Name            string
	Field1Label     *string
	Field2Label     *string
	IsBidirectional bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CardCount       int
}

// FieldLabels returns the deck's field labels with the "Front"/"Back" fallbacks.
func (d Deck) FieldLabels() (string, string) {
	f1, f2 := "Front", "Back"
	if d.Field1Label != nil && *d.Field1Label != "" {
		f1 = *d.Field1Label
	}
	if d.Field2Label != nil && *d.Field2Label != "" {
		f2 = *d.Field2Label
	}
	return f1, f2
}

// Schedule is the SM-2 state of one study direction of a card.
type Schedule struct {
	ID         int64
	CardID     int64
	IsReversed bool
	Easiness   float64
	Interval   int
	RepCount   int
	Grade      *sm2.Grade
	LastSeen   *time.Time
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// State extracts the pure SM-2 state.
func (sc Schedule) State() sm2.State {
	return sm2.State{
		LastSeen: sc.LastSeen,
		Grade:    sc.Grade,
		RepCount: sc.RepCount,
		Easiness: sc.Easiness,
		Interval: sc.Interval,
	}
}

// Card is a flashcard with denormalized deck/topic info.
type Card struct {
	ID        int64
	DeckID    int64
	DeckName  string
	TopicID   int64
	TopicName string
	Front     string
	Back      string
	Note      *string
	Priority  sm2.Priority
	Tags      []string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time

	IsBidirectional bool
	// Schedules: forward first, then reverse if present.
	Schedules []Schedule
}

// ForwardSchedule returns the is_reversed=false schedule, or nil.
func (c Card) ForwardSchedule() *Schedule {
	for i := range c.Schedules {
		if !c.Schedules[i].IsReversed {
			return &c.Schedules[i]
		}
	}
	return nil
}

// ReverseSchedule returns the is_reversed=true schedule, or nil.
func (c Card) ReverseSchedule() *Schedule {
	for i := range c.Schedules {
		if c.Schedules[i].IsReversed {
			return &c.Schedules[i]
		}
	}
	return nil
}

// StudyItem is a schedule joined with its card and deck, with prompt/answer
// resolved by direction (port of formatStudyCard in study-service.ts).
type StudyItem struct {
	CardID      int64
	ScheduleID  int64
	Prompt      string
	Answer      string
	PromptLabel string
	AnswerLabel string
	Note        *string
	Priority    sm2.Priority
	IsReversed  bool
	DeckID      int64
	DeckName    string
	Tags        []string
	State       sm2.State
	Version     int
}

// StudyStats summarizes a topic/deck selection for the study setup page.
type StudyStats struct {
	TotalCards int
	DueTotal   int
	DueA       int
	DueB       int
	DueC       int
}

// DashboardStats is the aggregate view for the dashboard page.
type DashboardStats struct {
	TotalTopics    int
	TotalDecks     int
	TotalCards     int
	CardsCorrect   int
	CardsIncorrect int
	DueA           int
	DueB           int
	DueC           int
	RecentActivity []ActivityItem
}

// ActivityItem is one recent-review entry on the dashboard.
type ActivityItem struct {
	CardID int64
	Action string
	// ItemName is the card's front, in full and still as markdown. The
	// template strips and truncates it.
	ItemName  string
	DeckName  string
	Timestamp time.Time
}
