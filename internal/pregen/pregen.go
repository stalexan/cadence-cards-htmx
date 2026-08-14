// Package pregen runs the nightly question pre-generation job: spaced
// repetition knows tonight exactly which cards are due tomorrow, so their
// study questions are generated ahead of time via the Message Batches API
// (half price, own request pool) and served instantly during the day.
//
// Deliberately not part of the server.AI interface: handlers never touch
// batching, so handler tests keep running offline against stubAI. The runner
// is wired only in cmd/cadence, with the concrete *claude.Client.
package pregen

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/store"
)

// BatchAI is the batch capability of *claude.Client, an interface so the
// runner's tests can fake it.
type BatchAI interface {
	GenerateQuestionBatch(ctx context.Context, reqs []claude.BatchQuestionRequest) (map[int64]string, error)
	QuestionModel() string
}

// Horizon is how far ahead the nightly job looks: everything due within the
// next day gets a question tonight.
const Horizon = 24 * time.Hour

// DefaultMaxBatch caps one nightly run. Skipped schedules are logged (never
// silently dropped) and picked up the next night — or served live.
const DefaultMaxBatch = 200

// Runner fires the nightly job from the hourly maintenance ticker.
type Runner struct {
	store *store.Store
	ai    BatchAI
	hour  int // local hour (0-23) at or after which the job runs
	max   int

	mu      sync.Mutex
	lastDay string // last local day the job ran (YYYY-MM-DD)
}

// New builds a Runner that fires once per local day, at the first ticker tick
// at or after the given hour.
func New(st *store.Store, ai BatchAI, hour int) *Runner {
	return &Runner{store: st, ai: ai, hour: hour, max: DefaultMaxBatch}
}

// MaybeRun runs the job if now has reached the configured hour and it hasn't
// run today. Blocks for the batch's duration — call it in a goroutine off the
// ticker; ctx cancellation (shutdown) abandons the poll loop.
func (r *Runner) MaybeRun(ctx context.Context, now time.Time) {
	if !r.claim(now) {
		return
	}
	r.run(ctx, now)
}

func (r *Runner) claim(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	day := now.Format("2006-01-02")
	if now.Hour() < r.hour || r.lastDay == day {
		return false
	}
	r.lastDay = day
	return true
}

func (r *Runner) run(ctx context.Context, now time.Time) {
	start := time.Now()

	userIDs, err := r.store.ListUserIDs(ctx)
	if err != nil {
		slog.Error("question pre-generation: listing users failed", "error", err)
		return
	}

	var reqs []claude.BatchQuestionRequest
	owner := map[int64]int64{} // scheduleID -> userID, for the upsert
	skipped := 0
	for _, userID := range userIDs {
		topics, err := r.store.ListTopics(ctx, userID)
		if err != nil {
			slog.Error("question pre-generation: listing topics failed", "userId", userID, "error", err)
			return
		}
		for _, topic := range topics {
			cfg := claude.NewTopicConfig(topic.Name, topic.TopicDescription, topic.Expertise,
				topic.Focus, topic.ContextType, topic.Example, topic.Question)
			items, err := r.store.SchedulesDueWithin(ctx, userID, topic.ID, now, Horizon)
			if err != nil {
				slog.Error("question pre-generation: due query failed", "topicId", topic.ID, "error", err)
				return
			}
			for _, item := range items {
				if len(reqs) >= r.max {
					skipped++
					continue
				}
				reqs = append(reqs, claude.BatchQuestionRequest{
					ScheduleID: item.ScheduleID,
					Cfg:        cfg,
					// Direction-aware, same as the live path.
					Card: claude.CardContent{Front: item.Prompt, Back: item.Answer, Note: item.Note},
				})
				owner[item.ScheduleID] = userID
			}
		}
	}
	if skipped > 0 {
		slog.Warn("question pre-generation over cap — remainder deferred to the next run",
			"cap", r.max, "skipped", skipped)
	}
	if len(reqs) == 0 {
		slog.Info("question pre-generation: nothing due within the horizon")
		return
	}

	questions, err := r.ai.GenerateQuestionBatch(ctx, reqs)
	if err != nil {
		slog.Error("question pre-generation batch failed", "requested", len(reqs), "error", err)
		return
	}
	stored := 0
	for schedID, q := range questions {
		if err := r.store.UpsertGeneratedQuestion(ctx, owner[schedID], schedID, q, r.ai.QuestionModel()); err != nil {
			slog.Error("storing pre-generated question failed", "scheduleId", schedID, "error", err)
			continue
		}
		stored++
	}
	slog.Info("question pre-generation complete",
		"requested", len(reqs),
		"stored", stored,
		"failed", len(reqs)-stored,
		"durationMs", time.Since(start).Milliseconds(),
	)
}
