package claude

// Port of web/src/lib/server/claude/services/study-assistance.ts. Card
// content is always resolved server-side by the caller — the browser never
// supplies prompt text.

import (
	"context"
	"fmt"
	"strings"
)

// CardContent is the card data used to build practice prompts.
type CardContent struct {
	Front string
	Back  string
	Note  *string
}

// buildQuestionTurns assembles the question-generation request. Shared by the
// live path (GenerateQuestion) and, later, batch pre-generation, so both send
// byte-identical prompts and share cache entries.
func buildQuestionTurns(cfg TopicConfig, card CardContent) (system string, turns []turn) {
	prompts := GeneratePrompts(cfg)
	cardXML := FormatCardXML(card.Front, card.Back, card.Note)

	prompt := fmt.Sprintf(`Here's the card to create a question for:

<card>
%s
</card>

Place the question in an XML tag called question.`, cardXML)
	if strings.TrimSpace(cfg.Question) != "" {
		prompt += "\n\n" + cfg.Question
	}

	return prompts.Identity, []turn{
		{role: "user", parts: []part{{text: prompts.StaticContext}}},
		// Breakpoint after the ack: system + static context + ack is the
		// topic-stable prefix shared by every card's question, so back-to-back
		// cards in a session read it from cache instead of re-paying for it.
		{role: "assistant", parts: []part{{text: "I will help generate a practice question.", cache: true}}},
		{role: "user", parts: []part{{text: prompt}}},
	}
}

// GenerateQuestion asks Claude to write a practice question for a card and
// extracts it from the <question> tag.
func (c *Client) GenerateQuestion(ctx context.Context, cfg TopicConfig, card CardContent) (string, error) {
	system, turns := buildQuestionTurns(cfg, card)
	resp, err := c.generateText(ctx, c.questionOpts, system, turns)
	if err != nil {
		return "", err
	}
	return ExtractQuestion(resp), nil
}

// ChatAboutQuestion continues the practice conversation about a card.
func (c *Client) ChatAboutQuestion(ctx context.Context, cfg TopicConfig, card CardContent, userAnswer string, previous []Message) (string, error) {
	prompts := GeneratePrompts(cfg)
	cardXML := FormatCardXML(card.Front, card.Back, card.Note)

	msgs := []turn{
		{role: "user", parts: []part{
			// Breakpoint 1: topic-stable instructions, shared across cards.
			{text: prompts.GeneralInstructions + "\n\n" + prompts.PracticeInstructions, cache: true},
			{text: "Here's the card to practice:\n<card>" + cardXML + "</card>"},
		}},
		// Breakpoint 2: extends the cached prefix through the card block —
		// stable across every turn of this card's chat.
		{role: "assistant", parts: []part{{text: "Understood.", cache: true}}},
	}
	// History comes from the server-owned transcript (never the browser), so
	// no defensive filtering is needed. historyTurns marks the last history
	// message (breakpoint 4, with the system block as the fourth — the API
	// maximum) so long chats re-read the growing conversation from cache.
	msgs = append(msgs, historyTurns(previous)...)
	msgs = append(msgs, turn{role: "user", parts: []part{{text: userAnswer}}})

	return c.generateText(ctx, c.chatOpts, prompts.Identity, msgs)
}

// ChatAboutTopic handles the free-form topic chat. The first message primes
// the conversation with the general instructions.
func (c *Client) ChatAboutTopic(ctx context.Context, cfg TopicConfig, message string, previous []Message, isFirst bool) (string, error) {
	prompts := GeneratePrompts(cfg)

	var msgs []turn
	if isFirst {
		msgs = []turn{
			{role: "user", parts: []part{{text: prompts.GeneralInstructions}}},
			// Breakpoint: instructions + ack are stable for the topic. Later
			// turns deliberately don't re-send the instructions (ported
			// behavior), so their only cacheable prefix is the history itself,
			// marked by historyTurns.
			{role: "assistant", parts: []part{{text: "Understood.", cache: true}}},
			{role: "user", parts: []part{{text: message}}},
		}
	} else {
		msgs = append(historyTurns(previous), turn{role: "user", parts: []part{{text: message}}})
	}
	return c.generateText(ctx, c.chatOpts, prompts.Identity, msgs)
}
