package claude

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cadence-cards/internal/config"
)

func strp(s string) *string { return &s }

func TestParseCardRevision(t *testing.T) {
	reply := `<front>¿Cómo se dice "to run"?</front>
<back>correr</back>
<note>
- "Corro todas las mañanas." — I run every morning.
</note>`

	got := ParseCardRevision(reply)
	if got.Front == nil || *got.Front != `¿Cómo se dice "to run"?` {
		t.Errorf("Front = %v, want the parsed front", got.Front)
	}
	if got.Back == nil || *got.Back != "correr" {
		t.Errorf("Back = %v, want %q", got.Back, "correr")
	}
	if got.Note == nil || *got.Note != `- "Corro todas las mañanas." — I run every morning.` {
		t.Errorf("Note = %v, want the parsed note", got.Note)
	}
}

// The changed-fields-only contract: an absent tag means "leave that field as
// the user typed it", so it must stay nil rather than becoming empty.
func TestParseCardRevisionChangedOnly(t *testing.T) {
	got := ParseCardRevision("<back>| es | en |\n|---|---|\n| correr | to run |</back>")
	if got.Front != nil {
		t.Errorf("Front = %q, want nil for an absent tag", *got.Front)
	}
	if got.Note != nil {
		t.Errorf("Note = %q, want nil for an absent tag", *got.Note)
	}
	if got.Back == nil {
		t.Fatal("Back = nil, want the revised value")
	}
	if got.Empty() {
		t.Error("Empty() = true for a revision that changes the back")
	}
}

// An empty <note></note> clears the note; an empty <front></front> or
// <back></back> must be ignored — they are required fields, so a revision may
// never blank them.
func TestParseCardRevisionEmptyNoteClears(t *testing.T) {
	got := ParseCardRevision("<front></front>\n<note></note>")
	if got.Front != nil {
		t.Errorf("Front = %q, want nil for a present-but-empty required field", *got.Front)
	}
	if got.Note == nil {
		t.Fatal("Note = nil, want a pointer to the empty string (clear)")
	}
	if *got.Note != "" {
		t.Errorf("Note = %q, want empty", *got.Note)
	}
	if got.Empty() {
		t.Error("Empty() = true for a revision that clears the note")
	}
}

// Prose that happens to open with something like "<note 1>" must not be
// mistaken for the real tag, or the field truncates mid-sentence.
func TestParseCardRevisionIgnoresLookalikeTags(t *testing.T) {
	got := ParseCardRevision("<note>Compare with <note 1> in the appendix. Keep reading.</note>")
	if want := "Compare with <note 1> in the appendix. Keep reading."; got.Note == nil || *got.Note != want {
		t.Errorf("Note = %v, want %q", got.Note, want)
	}
}

func TestCardRevisionEmpty(t *testing.T) {
	if !(CardRevision{}).Empty() {
		t.Error("zero CardRevision should be Empty")
	}
	if (CardRevision{Note: strp("")}).Empty() {
		t.Error("a note-clearing revision should not be Empty")
	}
}

// Every tag the instructions ask for must be one the parser reads back, and
// the two rules the handler depends on — omit means unchanged, empty note
// means clear — must actually be stated.
func TestAssistInstructionsAskForParsedTags(t *testing.T) {
	for tag := range assistTagRe {
		if !strings.Contains(assistInstructions, "<"+tag+">") {
			t.Errorf("assistInstructions never asks for <%s>", tag)
		}
	}
	for _, phrase := range []string{
		"omit the tag",
		"<note></note>",
		"empty <front> or <back>",
	} {
		if !strings.Contains(assistInstructions, phrase) {
			t.Errorf("assistInstructions no longer states %q", phrase)
		}
	}
}

func TestAssistPreamble(t *testing.T) {
	if got := assistPreamble(CardAssistContext{}); got != "" {
		t.Errorf("empty context should produce no preamble, got %q", got)
	}
	got := assistPreamble(CardAssistContext{
		TopicName: "Spanish", TopicDesc: "Mexican Spanish",
		FrontLabel: "Spanish", BackLabel: "English",
	})
	for _, want := range []string{`"Spanish"`, "(Mexican Spanish)", `the front "Spanish"`, `the back "English"`} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble %q missing %q", got, want)
		}
	}
	// A description that just repeats the name would read as stuttering.
	if got := assistPreamble(CardAssistContext{TopicName: "Chess", TopicDesc: "Chess"}); strings.Contains(got, "(") {
		t.Errorf("preamble should skip a description equal to the name, got %q", got)
	}
}

// Same contract as the other operations: no API key means ErrNotConfigured
// before any request is made.
func TestUnconfiguredAssistFailsWithoutARequest(t *testing.T) {
	c := New(config.Config{ClaudeModel: "claude-opus-5", ClaudeMaxTokens: 100})
	_, err := c.AssistCard(context.Background(), CardAssistContext{}, CardDraft{}, "a card about correr")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("AssistCard error = %v, want ErrNotConfigured", err)
	}
	if reqs, _, _, _, _ := c.Tracker.Totals(); reqs != 0 {
		t.Errorf("tracker recorded %d requests for a client that never called the API", reqs)
	}
}
