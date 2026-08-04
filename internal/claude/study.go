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

// GenerateQuestion asks Claude to write a practice question for a card and
// extracts it from the <question> tag.
func (c *Client) GenerateQuestion(ctx context.Context, cfg TopicConfig, card CardContent) (string, error) {
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

	resp, err := c.generateText(ctx, prompts.Identity, []Message{
		{Role: "user", Content: prompts.StaticContext},
		{Role: "assistant", Content: "I will help generate a practice question."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return "", err
	}
	return ExtractQuestion(resp), nil
}

// ChatAboutQuestion continues the practice conversation about a card.
func (c *Client) ChatAboutQuestion(ctx context.Context, cfg TopicConfig, card CardContent, userAnswer string, previous []Message) (string, error) {
	prompts := GeneratePrompts(cfg)
	cardXML := FormatCardXML(card.Front, card.Back, card.Note)

	// Filter out context/instruction messages if they leaked into history.
	visible := make([]Message, 0, len(previous))
	for _, m := range previous {
		if m.Role == "user" && strings.Contains(m.Content, prompts.GeneralInstructions) {
			continue
		}
		if m.Role == "assistant" && m.Content == "Understood." {
			continue
		}
		visible = append(visible, m)
	}

	msgs := []Message{
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s\n\nHere's the card to practice:\n<card>%s</card>",
			prompts.GeneralInstructions, prompts.PracticeInstructions, cardXML)},
		{Role: "assistant", Content: "Understood."},
	}
	msgs = append(msgs, visible...)
	msgs = append(msgs, Message{Role: "user", Content: userAnswer})

	return c.generateText(ctx, prompts.Identity, msgs)
}

// ChatAboutTopic handles the free-form topic chat. The first message primes
// the conversation with the general instructions.
func (c *Client) ChatAboutTopic(ctx context.Context, cfg TopicConfig, message string, previous []Message, isFirst bool) (string, error) {
	prompts := GeneratePrompts(cfg)

	var msgs []Message
	if isFirst {
		msgs = []Message{
			{Role: "user", Content: prompts.GeneralInstructions},
			{Role: "assistant", Content: "Understood."},
			{Role: "user", Content: message},
		}
	} else {
		msgs = append(append(msgs, previous...), Message{Role: "user", Content: message})
	}
	return c.generateText(ctx, prompts.Identity, msgs)
}
