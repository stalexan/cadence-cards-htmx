// Package yamlio ports web/src/lib/yaml-utils.ts: the YAML card import/export
// format shared with cadence-cards-svelte. Key set, key order, defaults, and
// validation rules mirror the source so exports round-trip between the apps.
//
// Byte compatibility note: output matches the npm `yaml` stringify settings
// (indent 2, null literals, date-only LastSeen, `[]` for empty tag lists) for
// typical content. Very long lines may be folded differently by the two
// emitters; folding is semantically lossless, and both importers parse the
// other side's output.
package yamlio

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"cadence-cards/internal/sm2"
)

// Card is one card in YAML form (the YamlCard shape from yaml-utils.ts).
// Pointer fields distinguish absent from null where defaults apply.
type Card struct {
	ID       *int     `yaml:"ID"`
	Front    string   `yaml:"Front"`
	Back     string   `yaml:"Back"`
	Note     *string  `yaml:"Note"`
	LastSeen *string  `yaml:"LastSeen"`
	Priority string   `yaml:"Priority"`
	Grade    *string  `yaml:"Grade"`
	RepCount int      `yaml:"RepCount"`
	Easiness float64  `yaml:"Easiness"`
	Interval int      `yaml:"Interval"`
	Tags     []string `yaml:"Tags"`

	// Reverse schedule parameters (for bidirectional decks).
	ReverseLastSeen *string `yaml:"ReverseLastSeen"`
	ReverseGrade    *string `yaml:"ReverseGrade"`
	ReverseRepCount int     `yaml:"ReverseRepCount"`
	ReverseEasiness float64 `yaml:"ReverseEasiness"`
	ReverseInterval int     `yaml:"ReverseInterval"`

	// HasReverse reports whether any reverse key was present in the source
	// YAML (including explicit nulls), mirroring the `!== undefined` checks in
	// convertYamlCardsToDatabaseFormat.
	HasReverse bool `yaml:"-"`
}

// InvalidCard is a card that failed validation, with its index-bearing error.
type InvalidCard struct {
	Card  any
	Error string
}

// ExportCard is the input to Export: card content plus optional SM-2 state.
type ExportCard struct {
	Front    string
	Back     string
	Note     *string
	Priority sm2.Priority
	Tags     []string
	Forward  sm2.State
	// Reverse is non-nil when the card has a reverse schedule; its state is
	// then always exported alongside the forward one.
	Reverse *sm2.State
}

// Metadata is the export header block.
type Metadata struct {
	FormatVersion string
	DeckName      string
	CreatorName   *string
	ExportDate    string // YYYY-MM-DD
	CardCount     int
}

// dateOnly formats a timestamp the way the source does: UTC calendar date
// (toISOString().split('T')[0]).
func dateOnly(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02")
	return &s
}

func gradeStr(g *sm2.Grade) *string {
	if g == nil {
		return nil
	}
	s := string(*g)
	return &s
}

// Ordered card shapes for export. Field order matches yaml-utils.ts exactly:
// ID, Front, Back, Note, Priority, Tags [, LastSeen, Grade, RepCount,
// Easiness, Interval [, Reverse*]].
type baseCard struct {
	ID       int      `yaml:"ID"`
	Front    string   `yaml:"Front"`
	Back     string   `yaml:"Back"`
	Note     *string  `yaml:"Note"`
	Priority string   `yaml:"Priority"`
	Tags     []string `yaml:"Tags"`
}

type sm2Card struct {
	baseCard `yaml:",inline"`
	LastSeen *string `yaml:"LastSeen"`
	Grade    *string `yaml:"Grade"`
	RepCount int     `yaml:"RepCount"`
	Easiness float64 `yaml:"Easiness"`
	Interval int     `yaml:"Interval"`
}

type sm2ReverseCard struct {
	sm2Card         `yaml:",inline"`
	ReverseLastSeen *string `yaml:"ReverseLastSeen"`
	ReverseGrade    *string `yaml:"ReverseGrade"`
	ReverseRepCount int     `yaml:"ReverseRepCount"`
	ReverseEasiness float64 `yaml:"ReverseEasiness"`
	ReverseInterval int     `yaml:"ReverseInterval"`
}

// Export serializes cards to the shared YAML format (port of
// exportCardsToYaml). SM-2 parameters are included only when includeSM2 is
// set; the metadata comment header only when meta is non-nil.
func Export(cards []ExportCard, meta *Metadata, includeSM2 bool) (string, error) {
	out := make([]any, len(cards))
	for i, c := range cards {
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		base := baseCard{
			ID:       i + 1, // sequential IDs for export
			Front:    c.Front,
			Back:     c.Back,
			Note:     c.Note,
			Priority: string(c.Priority),
			Tags:     tags,
		}
		if !includeSM2 {
			out[i] = base
			continue
		}
		withSM2 := sm2Card{
			baseCard: base,
			LastSeen: dateOnly(c.Forward.LastSeen),
			Grade:    gradeStr(c.Forward.Grade),
			RepCount: c.Forward.RepCount,
			Easiness: c.Forward.Easiness,
			Interval: c.Forward.Interval,
		}
		if c.Reverse == nil {
			out[i] = withSM2
			continue
		}
		out[i] = sm2ReverseCard{
			sm2Card:         withSM2,
			ReverseLastSeen: dateOnly(c.Reverse.LastSeen),
			ReverseGrade:    gradeStr(c.Reverse.Grade),
			ReverseRepCount: c.Reverse.RepCount,
			ReverseEasiness: c.Reverse.Easiness,
			ReverseInterval: c.Reverse.Interval,
		}
	}

	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	content := sb.String()

	if meta != nil {
		creator := "Anonymous"
		if meta.CreatorName != nil && *meta.CreatorName != "" {
			creator = *meta.CreatorName
		}
		header := fmt.Sprintf(`# ============================================
# Flashcard Deck Export
# ============================================
# Format Version: %s
# Deck: %s
# Creator: %s
# Exported: %s
# Cards: %d
# ============================================

`, meta.FormatVersion, headerValue(meta.DeckName), headerValue(creator), meta.ExportDate, meta.CardCount)
		return header + content, nil
	}
	return content, nil
}

// headerValue keeps a user-supplied metadata value on its own comment line: a
// newline in a deck or creator name would otherwise escape the '# ' header
// and inject content into the export.
func headerValue(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

// Import parses YAML content into valid and invalid cards (port of
// importCardsFromYaml). Comment headers are ignored by the parser; each card
// is validated independently so bad cards are reported, not fatal.
func Import(src string) (valid []Card, invalid []InvalidCard, err error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return nil, nil, fmt.Errorf("Error parsing YAML: %v", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.SequenceNode {
		return nil, nil, fmt.Errorf("Error parsing YAML: YAML content must be an array of cards")
	}

	for i, node := range root.Content[0].Content {
		card, err := decodeCard(node)
		if err != nil {
			var raw any
			_ = node.Decode(&raw)
			invalid = append(invalid, InvalidCard{Card: raw, Error: fmt.Sprintf("Card at index %d: %v", i, err)})
			continue
		}
		valid = append(valid, card)
	}
	return valid, invalid, nil
}

// decodeCard decodes and validates one card node, applying the YamlCardSchema
// defaults (RepCount 0, Easiness 2.5, Interval 1, Tags []).
func decodeCard(node *yaml.Node) (Card, error) {
	// Track key presence (including explicit nulls) for reverse detection and
	// defaults.
	present := map[string]bool{}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			present[node.Content[i].Value] = true
		}
	}

	// Pointer-typed shadow struct to distinguish absent numeric fields.
	var raw struct {
		Front    *string  `yaml:"Front"`
		Back     *string  `yaml:"Back"`
		Note     *string  `yaml:"Note"`
		LastSeen *string  `yaml:"LastSeen"`
		Priority *string  `yaml:"Priority"`
		Grade    *string  `yaml:"Grade"`
		RepCount *int     `yaml:"RepCount"`
		Easiness *float64 `yaml:"Easiness"`
		Interval *int     `yaml:"Interval"`
		Tags     []string `yaml:"Tags"`

		ReverseLastSeen *string  `yaml:"ReverseLastSeen"`
		ReverseGrade    *string  `yaml:"ReverseGrade"`
		ReverseRepCount *int     `yaml:"ReverseRepCount"`
		ReverseEasiness *float64 `yaml:"ReverseEasiness"`
		ReverseInterval *int     `yaml:"ReverseInterval"`
	}
	if err := node.Decode(&raw); err != nil {
		return Card{}, err
	}

	if raw.Front == nil || *raw.Front == "" {
		return Card{}, fmt.Errorf("Front content is required")
	}
	if raw.Back == nil || *raw.Back == "" {
		return Card{}, fmt.Errorf("Back content is required")
	}
	if raw.Priority == nil || !sm2.ValidPriority(*raw.Priority) {
		return Card{}, fmt.Errorf("Priority must be one of A, B, C")
	}
	if raw.Grade != nil && !sm2.ValidGrade(*raw.Grade) {
		return Card{}, fmt.Errorf("invalid Grade")
	}
	if raw.ReverseGrade != nil && !sm2.ValidGrade(*raw.ReverseGrade) {
		return Card{}, fmt.Errorf("invalid ReverseGrade")
	}

	card := Card{
		Front:    *raw.Front,
		Back:     *raw.Back,
		Note:     raw.Note,
		LastSeen: raw.LastSeen,
		Priority: *raw.Priority,
		Grade:    raw.Grade,
		RepCount: 0,
		Easiness: 2.5,
		Interval: 1,
		Tags:     raw.Tags,

		ReverseLastSeen: raw.ReverseLastSeen,
		ReverseGrade:    raw.ReverseGrade,
		ReverseRepCount: 0,
		ReverseEasiness: 2.5,
		ReverseInterval: 1,
	}
	if card.Tags == nil {
		card.Tags = []string{}
	}

	if raw.RepCount != nil {
		if *raw.RepCount < 0 {
			return Card{}, fmt.Errorf("RepCount must be >= 0")
		}
		card.RepCount = *raw.RepCount
	}
	if raw.Easiness != nil {
		// SM-2 easiness has a 1.3 floor but no upper bound — capping here
		// would reject exported well-practiced cards on re-import.
		if *raw.Easiness < 1.3 {
			return Card{}, fmt.Errorf("Easiness must be >= 1.3")
		}
		card.Easiness = *raw.Easiness
	}
	if raw.Interval != nil {
		if *raw.Interval < 1 {
			return Card{}, fmt.Errorf("Interval must be >= 1")
		}
		card.Interval = *raw.Interval
	}
	if raw.ReverseRepCount != nil {
		if *raw.ReverseRepCount < 0 {
			return Card{}, fmt.Errorf("ReverseRepCount must be >= 0")
		}
		card.ReverseRepCount = *raw.ReverseRepCount
	}
	if raw.ReverseEasiness != nil {
		if *raw.ReverseEasiness < 1.3 {
			return Card{}, fmt.Errorf("ReverseEasiness must be >= 1.3")
		}
		card.ReverseEasiness = *raw.ReverseEasiness
	}
	if raw.ReverseInterval != nil {
		if *raw.ReverseInterval < 1 {
			return Card{}, fmt.Errorf("ReverseInterval must be >= 1")
		}
		card.ReverseInterval = *raw.ReverseInterval
	}
	// Any reverse key means the file carries reverse-direction state; the
	// Easiness/Interval keys count too, or values that were just validated
	// above would be silently discarded by ReverseState.
	card.HasReverse = present["ReverseLastSeen"] || present["ReverseGrade"] || present["ReverseRepCount"] ||
		present["ReverseEasiness"] || present["ReverseInterval"]
	return card, nil
}

// parseDate converts a YYYY-MM-DD LastSeen value to UTC midnight, matching
// the source's `new Date('YYYY-MM-DD')` semantics. Full timestamps are also
// accepted.
func parseDate(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	if t, err := time.Parse("2006-01-02", *s); err == nil {
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q", *s)
	}
	return &t, nil
}

// ForwardState converts the card's forward SM-2 fields to an sm2.State.
func (c Card) ForwardState() (sm2.State, error) {
	st := sm2.State{RepCount: c.RepCount, Easiness: c.Easiness, Interval: c.Interval}
	var err error
	if st.LastSeen, err = parseDate(c.LastSeen); err != nil {
		return st, err
	}
	if c.Grade != nil {
		g := sm2.Grade(*c.Grade)
		st.Grade = &g
	}
	return st, nil
}

// ReverseState converts the card's reverse SM-2 fields to an sm2.State, or
// nil when no reverse keys were present.
func (c Card) ReverseState() (*sm2.State, error) {
	if !c.HasReverse {
		return nil, nil
	}
	st := sm2.State{RepCount: c.ReverseRepCount, Easiness: c.ReverseEasiness, Interval: c.ReverseInterval}
	var err error
	if st.LastSeen, err = parseDate(c.ReverseLastSeen); err != nil {
		return nil, err
	}
	if c.ReverseGrade != nil {
		g := sm2.Grade(*c.ReverseGrade)
		st.Grade = &g
	}
	return &st, nil
}
