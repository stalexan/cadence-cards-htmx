package claude

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cadence-cards/internal/config"
)

func TestParseTopicSuggestion(t *testing.T) {
	reply := `<name>Spanish</name>
<description>Mexican Spanish</description>
<expertise>language tutor</expertise>
<focus>vocabulary and grammar</focus>
<context_type>cultural context</context_type>
<example>
H: What's the difference between "pelo" and "cabello"?

A: "Cabello" is the hair on your head and is the more formal word.
</example>
<question>Ask about the back of the card without revealing it.</question>`

	got := ParseTopicSuggestion(reply)
	want := TopicSuggestion{
		Name:        "Spanish",
		TopicDesc:   "Mexican Spanish",
		Expertise:   "language tutor",
		Focus:       "vocabulary and grammar",
		ContextType: "cultural context",
		Example: "H: What's the difference between \"pelo\" and \"cabello\"?\n\n" +
			`A: "Cabello" is the hair on your head and is the more formal word.`,
		Question: "Ask about the back of the card without revealing it.",
	}
	if got != want {
		t.Errorf("ParseTopicSuggestion() =\n%#v\nwant\n%#v", got, want)
	}
}

// A partial reply must still fill what it does carry: the caller only rejects a
// wholly empty suggestion.
func TestParseTopicSuggestionTolerentOfMissingTags(t *testing.T) {
	got := ParseTopicSuggestion("<name>Chess</name>\n<focus>openings and endgames</focus>")
	if got.Name != "Chess" || got.Focus != "openings and endgames" {
		t.Errorf("present tags not parsed: %#v", got)
	}
	if got.Expertise != "" || got.Example != "" {
		t.Errorf("absent tags should stay empty: %#v", got)
	}
	if got.Empty() {
		t.Error("Empty() = true for a partially filled suggestion")
	}
}

// Prose inside <example> that opens with something like "<example 1>" must not
// be mistaken for the real closing tag, or the example truncates mid-sentence.
func TestParseTopicSuggestionIgnoresLookalikeTags(t *testing.T) {
	got := ParseTopicSuggestion("<example>The app writes <example 1> around this. Keep reading.</example>")
	if want := "The app writes <example 1> around this. Keep reading."; got.Example != want {
		t.Errorf("Example = %q, want %q", got.Example, want)
	}
}

func TestTopicSuggestionEmpty(t *testing.T) {
	if !(TopicSuggestion{}).Empty() {
		t.Error("zero TopicSuggestion should be Empty")
	}
	if (TopicSuggestion{Question: "x"}).Empty() {
		t.Error("suggestion with a question should not be Empty")
	}
}

// The instructions quote the identity sentence GeneratePrompts builds. If that
// template changes and the quote does not, suggestions get written to fit a
// sentence the app no longer produces — so pin the shared phrases.
func TestSuggestInstructionsQuoteTheRealTemplate(t *testing.T) {
	identity := GeneratePrompts(TopicConfig{}).Identity
	for _, phrase := range []string{
		"You are a knowledgeable",
		"specializing in",
		"through flashcard",
		"provide explanations about",
		"when relevant",
	} {
		if !strings.Contains(identity, phrase) {
			t.Errorf("identity prompt no longer contains %q — update suggestInstructions", phrase)
		}
		if !strings.Contains(suggestInstructions, phrase) {
			t.Errorf("suggestInstructions no longer quotes %q", phrase)
		}
	}
}

// Every tag the instructions ask for must be one the parser reads back.
func TestSuggestInstructionsAskForParsedTags(t *testing.T) {
	for tag := range suggestTagRe {
		if !strings.Contains(suggestInstructions, "<"+tag+">") {
			t.Errorf("suggestInstructions never asks for <%s>", tag)
		}
	}
}

// A server started without CLAUDE_API_KEY must fail before the request, not
// after: the SDK's own credential-discovery error carries no HTTP status, so
// classifyAPIError cannot recognise it and every caller would report a generic
// "try again" for a condition retrying can never fix.
func TestUnconfiguredClientFailsWithoutARequest(t *testing.T) {
	c := New(config.Config{ClaudeModel: "claude-opus-5", ClaudeMaxTokens: 100})
	if c.configured {
		t.Fatal("client with no API key reports itself configured")
	}

	_, err := c.SuggestTopicConfig(context.Background(), "Mexican Spanish")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("SuggestTopicConfig error = %v, want ErrNotConfigured", err)
	}
	_, err = c.GenerateQuestion(context.Background(), TopicConfig{Topic: "Spanish"}, CardContent{Front: "hola", Back: "hello"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("GenerateQuestion error = %v, want ErrNotConfigured", err)
	}
	// Nothing was sent, so nothing should have been counted.
	if reqs, _, _, _, _ := c.Tracker.Totals(); reqs != 0 {
		t.Errorf("tracker recorded %d requests for a client that never called the API", reqs)
	}

	if !New(config.Config{ClaudeAPIKey: "sk-test"}).configured {
		t.Error("client with an API key reports itself unconfigured")
	}
}
