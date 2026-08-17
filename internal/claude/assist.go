package claude

// Card drafting and revision. Like suggest.go this has no counterpart in the
// Svelte app. It differs from topic suggestion in one deliberate way: topic
// suggestion only fills blank fields, but card assistance is iterative — the
// user refines an existing draft ("make the second example about food"), so
// the reply may overwrite fields that already have content. The contract that
// makes iteration safe is changed-fields-only: a field whose tag is absent
// from the reply is left exactly as the user typed it.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// CardDraft is the card's three markdown fields as they currently sit in the
// form — possibly all empty (a brand-new card).
type CardDraft struct {
	Front string
	Back  string
	Note  string
}

// CardAssistContext is optional prompt context resolved server-side from the
// form's deck/topic selection. The zero value means "no context known" and
// produces a generic drafting prompt.
type CardAssistContext struct {
	TopicName  string
	TopicDesc  string
	FrontLabel string
	BackLabel  string
}

func (a CardAssistContext) empty() bool {
	return a == CardAssistContext{}
}

// CardRevision is Claude's proposed changes. nil means "leave the field as it
// is". For Note a pointer to "" means "clear it"; Front and Back are required
// fields, so ParseCardRevision never produces an empty value for them.
type CardRevision struct {
	Front *string
	Back  *string
	Note  *string
}

// Empty reports whether the revision changes nothing.
func (r CardRevision) Empty() bool {
	return r.Front == nil && r.Back == nil && r.Note == nil
}

const assistSystem = `You draft and revise flashcards for a spaced-repetition study app.

Given a card's current fields and an instruction from its author, you either
write a new card from scratch or make exactly the changes the instruction asks
for, and nothing more.`

// assistInstructions is identical for every request, so it carries the cache
// breakpoint: only the card, its context, and the instruction below it vary.
const assistInstructions = `A card has three fields:

<front>
The prompt side, shown first during study. Usually a term, question, or cue.
</front>

<back>
The answer side, revealed on demand. Keep it focused on the answer itself.
</back>

<note>
Optional extra context shown alongside the back: examples, mnemonics, usage
notes. Leave it out when the back already says everything worth saying.
</note>

All three fields are GitHub-Flavored Markdown — lists, tables, code fences,
and emphasis all render. The renderer drops raw HTML entirely, so never emit
HTML tags in field content.

Reply with a tag for each field you are changing, and omit the tag for any
field you are leaving as it is. When all three current fields are empty you
are drafting a new card: write the front and the back, and add a note only if
it earns its place. An empty <note></note> means: clear the note. Never reply
with an empty <front> or <back> — they are required fields, so to leave one
alone, omit its tag.

Follow the instruction precisely. If it targets one field ("reformat the
back"), change only that field. If it asks for a change inside a field ("swap
the second example"), keep the rest of that field intact.

Reply with only the tags, nothing before or after them:

<front>...</front>
<back>...</back>
<note>...</note>`

// assistSeed carries everything that varies per request, kept below the
// cached instructions. The current fields use current_* tag names so card
// content that quotes the reply tags can never collide with them.
const assistSeed = `%sHere is the card as it currently stands (an empty tag is an empty field):

<current_front>
%s
</current_front>
<current_back>
%s
</current_back>
<current_note>
%s
</current_note>

Here is the instruction:

<instruction>
%s
</instruction>`

// maxAssistInstructionLen caps the instruction box, and maxAssistFieldLen
// each card field — generous next to any real card, but keeps a paste
// accident from carrying an unbounded prompt.
const (
	maxAssistInstructionLen = 2000
	maxAssistFieldLen       = 20000
)

// assistTagRe holds one compiled matcher per output tag. As in suggestTagRe,
// the tag name is matched with its closing angle bracket attached, so prose
// like "<note 1>" inside a field cannot be mistaken for the real tag.
var assistTagRe = func() map[string]*regexp.Regexp {
	tags := []string{"front", "back", "note"}
	m := make(map[string]*regexp.Regexp, len(tags))
	for _, t := range tags {
		m[t] = regexp.MustCompile(`(?s)<` + t + `>(.*?)</` + t + `>`)
	}
	return m
}()

// assistTag pulls one tag's trimmed contents and whether the tag was present
// at all — unlike suggest's tagValue, absent and present-but-empty must be
// told apart here, because absent means "unchanged" and empty means "empty".
func assistTag(text, tag string) (string, bool) {
	re, ok := assistTagRe[tag]
	if !ok {
		return "", false
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// ParseCardRevision reads the tagged reply. An absent tag stays nil (leave
// the field alone). A present-but-empty front or back is treated as absent —
// they are required fields, so a revision must never blank them — while a
// present-but-empty note means "clear the note".
func ParseCardRevision(text string) CardRevision {
	var r CardRevision
	if v, ok := assistTag(text, "front"); ok && v != "" {
		r.Front = &v
	}
	if v, ok := assistTag(text, "back"); ok && v != "" {
		r.Back = &v
	}
	if v, ok := assistTag(text, "note"); ok {
		r.Note = &v
	}
	return r
}

// capRunes truncates by rune, not byte: a byte cut can land mid-character and
// hand the API a broken sequence.
func capRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// assistPreamble renders the optional context line ahead of the card fields.
func assistPreamble(actx CardAssistContext) string {
	if actx.empty() {
		return ""
	}
	var b strings.Builder
	if actx.TopicName != "" {
		fmt.Fprintf(&b, "This card belongs to the topic %q", actx.TopicName)
		if actx.TopicDesc != "" && actx.TopicDesc != actx.TopicName {
			fmt.Fprintf(&b, " (%s)", actx.TopicDesc)
		}
		b.WriteString(".")
	}
	if actx.FrontLabel != "" && actx.BackLabel != "" {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Its deck labels the front %q and the back %q.", actx.FrontLabel, actx.BackLabel)
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("\n\n")
	return b.String()
}

// AssistCard drafts or revises a card's front, back, and note from the user's
// instruction. Fields absent from the returned revision are unchanged.
func (c *Client) AssistCard(ctx context.Context, actx CardAssistContext, draft CardDraft, instruction string) (CardRevision, error) {
	instruction = capRunes(strings.TrimSpace(instruction), maxAssistInstructionLen)

	seed := fmt.Sprintf(assistSeed,
		assistPreamble(actx),
		capRunes(draft.Front, maxAssistFieldLen),
		capRunes(draft.Back, maxAssistFieldLen),
		capRunes(draft.Note, maxAssistFieldLen),
		instruction,
	)
	turns := []turn{{role: "user", parts: []part{
		// The instructions are byte-identical for every user and every card,
		// so this breakpoint is shared process-wide, not per-card.
		{text: assistInstructions, cache: true},
		{text: seed},
	}}}

	resp, err := c.generateText(ctx, c.chatOpts, assistSystem, turns)
	if err != nil {
		return CardRevision{}, err
	}
	r := ParseCardRevision(resp)
	if r.Empty() {
		return CardRevision{}, fmt.Errorf("claude returned no card changes")
	}
	return r, nil
}
