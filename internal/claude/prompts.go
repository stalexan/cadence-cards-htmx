package claude

// Port of web/src/lib/server/claude/prompts/study-assistance.ts. Prompt text
// is reproduced verbatim, including the source's whitespace.

import (
	"fmt"
	"regexp"
	"strings"
)

// TopicConfig is the prompt configuration resolved from a Topic row, with the
// source's fallbacks applied.
type TopicConfig struct {
	Topic       string
	TopicDesc   string
	Expertise   string
	Focus       string
	ContextType string
	Example     string
	Question    string
}

// orDefault returns the pointed-to string or fallback when nil/empty.
func orDefault(p *string, fallback string) string {
	if p != nil && *p != "" {
		return *p
	}
	return fallback
}

// NewTopicConfig applies the getTopicConfig fallbacks to raw topic fields.
func NewTopicConfig(name string, description, expertise, focus, contextType, example, question *string) TopicConfig {
	return TopicConfig{
		Topic:       name,
		TopicDesc:   orDefault(description, name),
		Expertise:   orDefault(expertise, "tutor"),
		Focus:       orDefault(focus, "concepts and principles"),
		ContextType: orDefault(contextType, "additional context"),
		Example:     orDefault(example, ""),
		Question:    orDefault(question, ""),
	}
}

// Prompts are the generated prompt strings for a topic.
type Prompts struct {
	Identity             string
	StaticContext        string
	Examples             string
	AdditionalGuardrails string
	GeneralInstructions  string
	PracticeInstructions string
}

// GeneratePrompts ports generateTopicPrompts.
func GeneratePrompts(c TopicConfig) Prompts {
	identity := fmt.Sprintf(`You are a knowledgeable %s %s, specializing in %s.
Your role is to help users practice and learn %s through flashcard exercises,
provide explanations about %s, and offer %s when relevant.`,
		c.Topic, c.Expertise, c.TopicDesc, c.Topic, c.Focus, c.ContextType)

	staticContext := fmt.Sprintf(`
<static_context>
%s Learning Assistant

Role:
- Help users practice %s %s through flashcards
- Provide clear explanations of %s concepts
- Offer %s for better understanding
- Guide users through their learning journey with patience and encouragement

Key Features:
- Focus on %s concepts and principles
- Contextual examples for better understanding
- Detailed explanations when needed
- Additional notes for deeper comprehension
</static_context>
`, c.Topic, c.Topic, c.Focus, c.Topic, c.ContextType, c.Topic)

	examples := `
Here are examples of how you should interact with learners:
`
	if strings.TrimSpace(c.Example) != "" {
		examples += fmt.Sprintf(`
<example 1>
%s
</example 1>
`, c.Example)
	} else {
		examples += `
<example 1>
H: What's the difference between "concept A" and "concept B"?

A: Let me explain the difference between "concept A" and "concept B" in this topic:

[Explanation of the difference]

For example:
- [Example 1] - [Explanation]
- [Example 2] - [Explanation]
- [Example 3] - [Explanation]
- [Example 4] - [Explanation]
</example 1>
`
	}
	examples += `
<example 2>
H: [Practice mode - shown flashcard about a concept]

A: [Question about the concept shown on the flashcard]
</example 2>
`

	guardrails := fmt.Sprintf(`Please adhere to the following guidelines:
1. Always provide accurate information about %s
2. When relevant, point out different perspectives or approaches within %s
3. Provide contextual information when it adds value to understanding
4. Be encouraging and patient with learners
5. Use clear explanations appropriate to the learner's level
`, c.Topic, c.Topic)

	practice := fmt.Sprintf(`
We are now in practice mode. Help the user practice their %s knowledge using flashcards.

Important guidelines:
- Do not offer a "next card" option since there is a separate button for that
- Do not use images or say anything about showing images
- Don't say you are waiting for them
- Do not repeat back to the user any instructions you've been given
- If they ask for the answer, provide it along with a brief explanation of any important points
- For additional notes, focus specifically on how the concept is understood or used in the context of %s
`, c.Topic, c.Topic)

	return Prompts{
		Identity:             identity,
		StaticContext:        staticContext,
		Examples:             examples,
		AdditionalGuardrails: guardrails,
		GeneralInstructions:  staticContext + examples + guardrails,
		PracticeInstructions: practice,
	}
}

var questionRe = regexp.MustCompile(`(?s)<question>(.*?)</question>`)

// ExtractQuestion pulls the question tag from a response, falling back to the
// whole text (port of extractQuestionFromXML).
func ExtractQuestion(text string) string {
	if m := questionRe.FindStringSubmatch(text); m != nil {
		if q := strings.TrimSpace(m[1]); q != "" {
			return q
		}
	}
	return text
}

// FormatCardXML ports formatCardAsXML.
func FormatCardXML(front, back string, note *string) string {
	xml := fmt.Sprintf("<front>%s</front>\n<back>%s</back>", front, back)
	if note != nil && *note != "" {
		xml += fmt.Sprintf("\n<notes>%s</notes>", *note)
	}
	return xml
}
