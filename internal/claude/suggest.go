package claude

// Topic-configuration suggestion. This has no counterpart in the Svelte app —
// it exists because GeneratePrompts is a fill-in-the-blanks template, and
// filling those blanks well requires knowing the sentences they land in. So the
// instructions below quote the template rather than describing it, which is
// what keeps a suggestion grammatical ("language tutor", not "a language tutor")
// and free of the article/duplication mistakes a blind guess makes.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// TopicSuggestion is a proposed prompt configuration for a topic. Fields map
// one-to-one onto the topic form's inputs; any of them may come back empty,
// which the caller treats as "nothing proposed for this one".
type TopicSuggestion struct {
	Name        string
	TopicDesc   string
	Expertise   string
	Focus       string
	ContextType string
	Example     string
	Question    string
}

// Empty reports whether nothing usable was proposed.
func (s TopicSuggestion) Empty() bool {
	return s.Name == "" && s.TopicDesc == "" && s.Expertise == "" && s.Focus == "" &&
		s.ContextType == "" && s.Example == "" && s.Question == ""
}

const suggestSystem = `You configure study topics for a spaced-repetition flashcard app.

The app builds its AI tutor's system prompt by substituting a topic's settings
into a fixed template. Given a short description of what someone is studying,
you propose settings that read naturally in that template and make the tutor
genuinely useful for that subject.`

// suggestInstructions is identical for every request, so it carries the cache
// breakpoint: only the description below it varies.
//
// The quoted template must match GeneratePrompts in prompts.go — if the
// identity sentence changes there, change it here too, or suggestions will be
// written to fit a sentence the app no longer builds.
const suggestInstructions = `The tutor's identity is assembled like this, with the settings substituted in:

    You are a knowledgeable {name} {expertise}, specializing in {description}.
    Your role is to help users practice and learn {name} through flashcard
    exercises, provide explanations about {focus}, and offer {context_type}
    when relevant.

Because of that sentence, {expertise}, {focus} and {context_type} must be bare
lowercase noun phrases: no leading article, no trailing period, and no repeat of
the topic name (it is already next to them).

Propose one value for each setting, following these rules:

<name>
The topic's short display name, as it will appear in menus. One to three words,
capitalized normally. Prefer the broad field ("Spanish", "Organic Chemistry",
"Music Theory") and leave the specifics to the description.
</name>

<description>
A more specific description of the subject area, as a noun phrase that completes
"specializing in ...". This is where the specifics from the person's own words
belong: "Mexican Spanish", "first-year medical pharmacology", "the Baroque
period".
</description>

<expertise>
The role the tutor should take, completing "You are a knowledgeable {name} ...".
Two or three words, ending in a teaching role: "language tutor", "chemistry
tutor", "clinical instructor".
</expertise>

<focus>
What the tutor should emphasize, completing "provide explanations about ...".
Name the two or three things a learner in this subject actually struggles with:
"vocabulary and grammar", "reaction mechanisms and stereochemistry".
</focus>

<context_type>
The kind of extra context worth offering, completing "offer ... when relevant".
This is the setting that makes a topic feel expert rather than generic, so pick
what a real teacher of this subject would volunteer: "cultural context" for a
language, "clinical relevance" for pharmacology, "historical context" for music
theory.
</context_type>

<example>
One worked exchange showing how the tutor should answer a learner's question
about this subject, in this shape:

    H: [a question a learner of this subject would really ask]

    A: [the tutor's answer: the direct explanation first, then concrete
    examples, each with a short gloss]

Write the whole thing out with real subject matter — a genuine question and a
genuine answer, not placeholders. Keep it under about 200 words. The app wraps
this in <example> tags for you and supplies its own practice-mode example
separately, so do not add tags, numbering, or a practice-mode exchange.
</example>

<question>
Instructions for generating a practice question from a flashcard, addressed to
the tutor. Always tell it to write a question testing whether the learner knows
the back of the card given the front, without revealing or hinting at the back.
Add whatever else this specific subject needs — a language topic might ask for
the question in English, a maths topic might ask that the question restate the
given values.
</question>

Reply with only the seven tags, in this order, and nothing before or after them:

<name>...</name>
<description>...</description>
<expertise>...</expertise>
<focus>...</focus>
<context_type>...</context_type>
<example>...</example>
<question>...</question>`

// suggestSeed carries the one part of the request that varies. Kept in its own
// block after the instructions so the cached prefix ends cleanly above it.
const suggestSeed = `Here is what the person is studying:

<studying>
%s
</studying>`

// maxSuggestSeedLen caps the description we forward. It is a UI-scale field
// ("what are you studying?"), so anything longer is a paste accident, and the
// cap keeps one request from carrying an unbounded prompt.
const maxSuggestSeedLen = 2000

// suggestTagRe holds one compiled matcher per output tag. The tag name is
// matched with its closing angle bracket attached, so prose inside <example>
// that happens to start "<example 1>" cannot be mistaken for the real tag.
var suggestTagRe = func() map[string]*regexp.Regexp {
	tags := []string{"name", "description", "expertise", "focus", "context_type", "example", "question"}
	m := make(map[string]*regexp.Regexp, len(tags))
	for _, t := range tags {
		m[t] = regexp.MustCompile(`(?s)<` + t + `>(.*?)</` + t + `>`)
	}
	return m
}()

// tagValue pulls one tag's trimmed contents, or "" when it is absent.
func tagValue(text, tag string) string {
	re, ok := suggestTagRe[tag]
	if !ok {
		return ""
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// ParseTopicSuggestion reads the tagged reply. Missing tags are left empty
// rather than failing the whole suggestion: a partial proposal still fills
// several fields usefully, and the caller only rejects a wholly empty one.
func ParseTopicSuggestion(text string) TopicSuggestion {
	return TopicSuggestion{
		Name:        tagValue(text, "name"),
		TopicDesc:   tagValue(text, "description"),
		Expertise:   tagValue(text, "expertise"),
		Focus:       tagValue(text, "focus"),
		ContextType: tagValue(text, "context_type"),
		Example:     tagValue(text, "example"),
		Question:    tagValue(text, "question"),
	}
}

// SuggestTopicConfig proposes a topic's prompt configuration from a one-line
// description of what the user is studying.
func (c *Client) SuggestTopicConfig(ctx context.Context, description string) (TopicSuggestion, error) {
	description = strings.TrimSpace(description)
	// Sliced by rune, not byte: a byte cut can land mid-character and hand the
	// API a broken sequence.
	if r := []rune(description); len(r) > maxSuggestSeedLen {
		description = string(r[:maxSuggestSeedLen])
	}

	turns := []turn{{role: "user", parts: []part{
		// The instructions are byte-identical for every user and every topic,
		// so this breakpoint is shared process-wide, not per-topic.
		{text: suggestInstructions, cache: true},
		{text: fmt.Sprintf(suggestSeed, description)},
	}}}

	resp, err := c.generateText(ctx, c.chatOpts, suggestSystem, turns)
	if err != nil {
		return TopicSuggestion{}, err
	}
	s := ParseTopicSuggestion(resp)
	if s.Empty() {
		return TopicSuggestion{}, fmt.Errorf("claude returned no usable topic settings")
	}
	return s, nil
}
