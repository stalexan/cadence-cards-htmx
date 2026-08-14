package claude

// Nightly question pre-generation via the Message Batches API: ~50% cheaper
// than live requests, with its own request pool, and most batches finish
// within the hour — a good fit for questions whose cards SM-2 already knows
// will be due tomorrow.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// BatchQuestionRequest is one card whose question should be pre-generated.
type BatchQuestionRequest struct {
	ScheduleID int64
	Cfg        TopicConfig
	Card       CardContent
}

// QuestionModel reports the model question generation runs on (stored next to
// each pre-generated question).
func (c *Client) QuestionModel() string { return c.questionOpts.model }

const batchPollInterval = time.Minute

// GenerateQuestionBatch submits one batch of question-generation requests,
// waits for it to end (polling; cancel via ctx), and returns the extracted
// questions keyed by schedule ID. Individual failures are logged and skipped —
// a missing key just means that card falls back to live generation. Prompts
// are built by the same buildQuestionTurns as the live path, so both send
// byte-identical prompts and share cache entries.
func (c *Client) GenerateQuestionBatch(ctx context.Context, reqs []BatchQuestionRequest) (map[int64]string, error) {
	params := anthropic.MessageBatchNewParams{}
	for _, r := range reqs {
		system, turns := buildQuestionTurns(r.Cfg, r.Card)
		p := buildParams(c.questionOpts, system, turns)
		params.Requests = append(params.Requests, anthropic.MessageBatchNewParamsRequest{
			CustomID: "sched-" + strconv.FormatInt(r.ScheduleID, 10),
			Params: anthropic.MessageBatchNewParamsRequestParams{
				Model:        p.Model,
				MaxTokens:    p.MaxTokens,
				System:       p.System,
				Messages:     p.Messages,
				Thinking:     p.Thinking,
				OutputConfig: p.OutputConfig,
			},
		})
	}

	batch, err := c.api.Messages.Batches.New(ctx, params)
	if err != nil {
		return nil, classifyAPIError(err)
	}
	slog.Info("question batch submitted", "batchId", batch.ID, "requests", len(reqs))

	for batch.ProcessingStatus != anthropic.MessageBatchProcessingStatusEnded {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(batchPollInterval):
		}
		if batch, err = c.api.Messages.Batches.Get(ctx, batch.ID); err != nil {
			return nil, classifyAPIError(err)
		}
	}

	out := make(map[int64]string, len(reqs))
	stream := c.api.Messages.Batches.ResultsStreaming(ctx, batch.ID)
	for stream.Next() {
		res := stream.Current()
		// Results arrive in arbitrary order — always key by custom_id.
		idStr, found := strings.CutPrefix(res.CustomID, "sched-")
		schedID, perr := strconv.ParseInt(idStr, 10, 64)
		if !found || perr != nil {
			slog.Warn("batch result with unexpected custom_id", "customId", res.CustomID)
			continue
		}
		succeeded, ok := res.Result.AsAny().(anthropic.MessageBatchSucceededResult)
		if !ok {
			slog.Warn("batch question request did not succeed",
				"scheduleId", schedID, "resultType", fmt.Sprintf("%T", res.Result.AsAny()))
			continue
		}
		msg := succeeded.Message
		c.Tracker.record(msg.Usage.InputTokens, msg.Usage.OutputTokens,
			msg.Usage.CacheCreationInputTokens, msg.Usage.CacheReadInputTokens)
		text, terr := messageText(&msg)
		if terr != nil {
			slog.Warn("batch question unusable", "scheduleId", schedID, "error", terr)
			continue
		}
		out[schedID] = ExtractQuestion(text)
	}
	if err := stream.Err(); err != nil {
		return nil, classifyAPIError(err)
	}
	return out, nil
}
