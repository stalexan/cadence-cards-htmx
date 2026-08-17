package yamlio

import (
	"strings"
	"testing"

	"cadence-cards/internal/sm2"
)

func sampleTopicConfig() TopicConfig {
	return TopicConfig{
		Name:             "French Basics",
		TopicDescription: str("Conversational French"),
		Expertise:        str("patient tutor"),
		Example:          str("line one\nline two"),
	}
}

func sampleTopicDecks() []ExportDeck {
	return []ExportDeck{
		{
			Name:            "Greetings",
			Field1Label:     str("French"),
			Field2Label:     str("English"),
			IsBidirectional: true,
			Cards:           sampleCards(),
		},
		{Name: "Empty Deck"},
	}
}

func sampleTopicMeta() *TopicMetadata {
	return &TopicMetadata{
		FormatVersion: "1.0", TopicName: "French Basics", CreatorName: str("Sean"),
		ExportDate: "2024-01-20", DeckCount: 2, CardCount: 2,
	}
}

// TestExportTopicGolden pins the exact bytes of the topic exporter so format
// drift is caught, the same way TestExportGolden guards the deck format.
func TestExportTopicGolden(t *testing.T) {
	out, err := ExportTopic(sampleTopicConfig(), nil, sampleTopicDecks(), sampleTopicMeta(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	want := `# ============================================
# Flashcard Topic Export
# ============================================
# Format Version: 1.0
# Topic: French Basics
# Creator: Sean
# Exported: 2024-01-20
# Decks: 2
# Cards: 2
# ============================================

Topic:
  Name: French Basics
  TopicDescription: Conversational French
  Expertise: patient tutor
  Focus: null
  ContextType: null
  Example: |-
    line one
    line two
  Question: null
Decks:
  - Name: Greetings
    Field1Label: French
    Field2Label: English
    IsBidirectional: true
    Cards:
      - ID: 1
        Front: Bonjour
        Back: Hello
        Note: a greeting
        Priority: A
        Tags:
          - french
          - greetings
        LastSeen: "2024-01-15"
        Grade: CORRECT_PERFECT_RECALL
        RepCount: 3
        Easiness: 2.7
        Interval: 16
      - ID: 2
        Front: Merci
        Back: Thank you
        Note: null
        Priority: B
        Tags: []
        LastSeen: null
        Grade: null
        RepCount: 0
        Easiness: 2.5
        Interval: 1
  - Name: Empty Deck
    Field1Label: null
    Field2Label: null
    IsBidirectional: false
    Cards: []
`
	if out != want {
		t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// TestTopicCardBlockMatchesDeckFormat is the regression that keeps the two
// formats from drifting: the per-card lines a topic export emits must be the
// deck export's lines, indented. Both go through cardNodes.
func TestTopicCardBlockMatchesDeckFormat(t *testing.T) {
	deckOut, err := Export(sampleCards(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	topicOut, err := ExportTopic(sampleTopicConfig(), nil,
		[]ExportDeck{{Name: "Greetings", Cards: sampleCards()}}, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	// The topic nests cards six spaces deeper (Decks > deck > Cards > item).
	var want strings.Builder
	for _, line := range strings.Split(strings.TrimRight(deckOut, "\n"), "\n") {
		want.WriteString("      " + line + "\n")
	}
	if !strings.Contains(topicOut, want.String()) {
		t.Errorf("topic card block diverged from the deck format:\n--- topic ---\n%s\n--- want block ---\n%s",
			topicOut, want.String())
	}
}

func TestExportTopicWithoutDecks(t *testing.T) {
	out, err := ExportTopic(sampleTopicConfig(), nil, sampleTopicDecks(), nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Decks:") {
		t.Errorf("config-only export should omit the Decks key entirely:\n%s", out)
	}
	if !strings.Contains(out, "Name: French Basics") {
		t.Errorf("config-only export lost the topic config:\n%s", out)
	}
	// It must still be recognized as a topic file, not garbage.
	if got := Detect(out); got != FormatTopic {
		t.Errorf("Detect(config-only) = %v, want FormatTopic", got)
	}
}

func TestTopicRoundtrip(t *testing.T) {
	out, err := ExportTopic(sampleTopicConfig(), nil, sampleTopicDecks(), sampleTopicMeta(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportTopic(out)
	if err != nil {
		t.Fatal(err)
	}

	if got.Config.Name != "French Basics" || deref(got.Config.Expertise) != "patient tutor" {
		t.Errorf("topic config = %+v", got.Config)
	}
	// Multi-line config must survive the block scalar.
	if deref(got.Config.Example) != "line one\nline two" {
		t.Errorf("Example = %q", deref(got.Config.Example))
	}
	if got.Config.Focus != nil {
		t.Errorf("absent Focus should stay nil, got %q", *got.Config.Focus)
	}
	if len(got.Decks) != 2 {
		t.Fatalf("decks = %d, want 2", len(got.Decks))
	}

	d := got.Decks[0]
	if d.Name != "Greetings" || !d.IsBidirectional || deref(d.Field1Label) != "French" {
		t.Errorf("deck = %+v", d)
	}
	if len(d.Cards) != 2 || len(d.Invalid) != 0 {
		t.Fatalf("cards = %d, invalid = %d", len(d.Cards), len(d.Invalid))
	}
	fwd, err := d.Cards[0].ForwardState()
	if err != nil {
		t.Fatal(err)
	}
	if fwd.RepCount != 3 || fwd.Easiness != 2.7 || fwd.Interval != 16 {
		t.Errorf("forward state = %+v", fwd)
	}
	if len(got.Decks[1].Cards) != 0 {
		t.Errorf("empty deck should round-trip empty, got %d cards", len(got.Decks[1].Cards))
	}
}

// A deck's cards carry reverse params only when the deck is bidirectional; the
// importer must see them so the deck is studied both ways.
func TestTopicRoundtripReverseParams(t *testing.T) {
	cards := sampleCards()
	rev := sm2.State{RepCount: 4, Easiness: 2.9, Interval: 49}
	cards[0].Reverse = &rev

	out, err := ExportTopic(sampleTopicConfig(), nil,
		[]ExportDeck{{Name: "Greetings", IsBidirectional: true, Cards: cards}}, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportTopic(out)
	if err != nil {
		t.Fatal(err)
	}
	card := got.Decks[0].Cards[0]
	if !card.HasReverse {
		t.Fatal("reverse params were dropped")
	}
	rs, err := card.ReverseState()
	if err != nil {
		t.Fatal(err)
	}
	if rs == nil || rs.RepCount != 4 || rs.Easiness != 2.9 || rs.Interval != 49 {
		t.Errorf("reverse state = %+v", rs)
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want Format
	}{
		{"card list", "- Front: a\n  Back: b\n  Priority: A\n", FormatCards},
		{"empty card list", "[]\n", FormatCards},
		{"topic", "Topic:\n  Name: X\n", FormatTopic},
		{"topic with decks", "Topic:\n  Name: X\nDecks: []\n", FormatTopic},
		{"mapping without Topic", "Decks: []\n", FormatUnknown},
		{"scalar", "just a string\n", FormatUnknown},
		{"empty", "", FormatUnknown},
		{"malformed", "Topic:\n\t- bad tab\n", FormatUnknown},
	}
	for _, c := range cases {
		if got := Detect(c.src); got != c.want {
			t.Errorf("Detect(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// A bad card costs its card, not the file — the same leniency Import applies.
func TestImportTopicPartialCards(t *testing.T) {
	src := `Topic:
  Name: French
Decks:
  - Name: Greetings
    Cards:
      - Front: Bonjour
        Back: Hello
        Priority: A
      - Back: no front
        Priority: B
`
	got, err := ImportTopic(src)
	if err != nil {
		t.Fatal(err)
	}
	d := got.Decks[0]
	if len(d.Cards) != 1 || len(d.Invalid) != 1 {
		t.Fatalf("cards = %d, invalid = %d", len(d.Cards), len(d.Invalid))
	}
	if !strings.Contains(d.Invalid[0].Error, `Deck "Greetings", card at index 1`) {
		t.Errorf("error should name the deck and index: %q", d.Invalid[0].Error)
	}
}

// An unnamed deck is reported and skipped rather than failing the whole file.
func TestImportTopicUnnamedDeck(t *testing.T) {
	src := "Topic:\n  Name: French\nDecks:\n  - Cards: []\n  - Name: Good\n"
	got, err := ImportTopic(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decks) != 1 || got.Decks[0].Name != "Good" {
		t.Errorf("decks = %+v", got.Decks)
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0], "Name is required") {
		t.Errorf("errors = %v", got.Errors)
	}
}

func TestImportTopicRejects(t *testing.T) {
	cases := []struct{ name, src, wantErr string }{
		{"card list", "- Front: a\n", "must be a mapping"},
		{"no Topic key", "Decks: []\n", "must be a mapping"},
		{"blank name", "Topic:\n  Name: \"  \"\n", "Topic Name is required"},
		{"decks not a list", "Topic:\n  Name: X\nDecks:\n  Name: nope\n", "Decks must be a list"},
	}
	for _, c := range cases {
		_, err := ImportTopic(c.src)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("ImportTopic(%s) error = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

// A newline in a topic name must not break out of the '#' comment header.
func TestTopicHeaderInjection(t *testing.T) {
	meta := sampleTopicMeta()
	meta.TopicName = "evil\n- Front: injected"
	out, err := ExportTopic(sampleTopicConfig(), nil, nil, meta, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- Front: injected") {
			t.Fatalf("newline escaped the comment header:\n%s", out)
		}
	}
}

func TestExportTopicAnonymousCreator(t *testing.T) {
	meta := sampleTopicMeta()
	meta.CreatorName = nil
	out, err := ExportTopic(sampleTopicConfig(), nil, nil, meta, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Creator: Anonymous") {
		t.Error("nil creator should fall back to Anonymous")
	}
}

func sampleProvenance() *Provenance {
	return &Provenance{
		Author:  str("Jane Doe"),
		License: str("CC-BY-4.0"),
		Source:  str("https://example.com/decks/french"),
	}
}

// TestExportTopicProvenanceGolden pins where the block sits and what it looks
// like: at the very top, ahead of Topic, all three keys in that order. Position
// is part of the format here rather than an implementation detail — a real
// topic's Example and Question are thousands of characters on one line, so a
// Provenance block below them is buried, which is exactly what this ordering
// exists to prevent.
func TestExportTopicProvenanceGolden(t *testing.T) {
	out, err := ExportTopic(sampleTopicConfig(), sampleProvenance(),
		[]ExportDeck{{Name: "Greetings"}}, nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `Provenance:
  Author: Jane Doe
  License: CC-BY-4.0
  Source: https://example.com/decks/french
Topic:
  Name: French Basics
`
	if !strings.HasPrefix(out, want) {
		t.Errorf("provenance block is not leading the file:\n--- got ---\n%s\n--- want prefix ---\n%s", out, want)
	}
}

// With a comment header the attribution still comes first in the document body,
// directly under the '#' block — the header is not YAML and is stripped on
// import, so it cannot carry the terms itself.
func TestExportTopicProvenanceFollowsHeader(t *testing.T) {
	out, err := ExportTopic(sampleTopicConfig(), sampleProvenance(), nil, sampleTopicMeta(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	body := out[strings.LastIndex(out, "# ============================================\n")+
		len("# ============================================\n"):]
	if !strings.HasPrefix(strings.TrimLeft(body, "\n"), "Provenance:") {
		t.Errorf("expected Provenance directly after the header, got:\n%s", out)
	}
}

// The block is omitted entirely rather than emitted as three nulls, both for a
// nil pointer and for a struct nobody filled in — a topic that was never shared
// should export exactly as it did before provenance existed.
func TestExportTopicOmitsEmptyProvenance(t *testing.T) {
	for _, c := range []struct {
		name string
		prov *Provenance
	}{
		{"nil pointer", nil},
		{"all fields unset", &Provenance{}},
		{"blank strings", &Provenance{Author: str(""), License: str("")}},
	} {
		out, err := ExportTopic(sampleTopicConfig(), c.prov, nil, nil, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "Provenance") {
			t.Errorf("%s: expected no Provenance block:\n%s", c.name, out)
		}
	}
}

func TestTopicProvenanceRoundtrip(t *testing.T) {
	out, err := ExportTopic(sampleTopicConfig(), sampleProvenance(), sampleTopicDecks(),
		sampleTopicMeta(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportTopic(out)
	if err != nil {
		t.Fatal(err)
	}
	if deref(got.Provenance.Author) != "Jane Doe" ||
		deref(got.Provenance.License) != "CC-BY-4.0" ||
		deref(got.Provenance.Source) != "https://example.com/decks/french" {
		t.Errorf("provenance = %+v", got.Provenance)
	}
	// The block must not have disturbed anything around it.
	if got.Config.Name != "French Basics" || len(got.Decks) != 2 {
		t.Errorf("config/decks damaged by the provenance block: %+v, %d decks", got.Config, len(got.Decks))
	}
}

// A file with no block imports as a zero Provenance, not as empty strings —
// which is what lets the store write NULLs and the topic page stay quiet.
func TestImportTopicWithoutProvenance(t *testing.T) {
	got, err := ImportTopic("Topic:\n  Name: X\n")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Provenance.IsZero() {
		t.Errorf("expected zero provenance, got %+v", got.Provenance)
	}
}

// Whitespace-only and empty values collapse to nil, so a hand-edited file that
// leaves `Author:` blank imports the same as one that omits it.
func TestImportTopicProvenanceTrims(t *testing.T) {
	src := "Topic:\n  Name: X\nProvenance:\n  Author: \"  Jane Doe  \"\n  License: \"   \"\n  Source: null\n"
	got, err := ImportTopic(src)
	if err != nil {
		t.Fatal(err)
	}
	if deref(got.Provenance.Author) != "Jane Doe" {
		t.Errorf("Author = %q, want trimmed", deref(got.Provenance.Author))
	}
	if got.Provenance.License != nil || got.Provenance.Source != nil {
		t.Errorf("blank values should be nil, got %+v", got.Provenance)
	}
}

// Attribution must never cost the cards. A Provenance key of the wrong shape is
// skipped (a sequence cannot decode into the struct) or reported, but the decks
// still import.
func TestImportTopicMalformedProvenanceKeepsFile(t *testing.T) {
	for _, src := range []string{
		"Topic:\n  Name: X\nProvenance: nonsense\nDecks:\n  - Name: D\n",
		"Topic:\n  Name: X\nProvenance:\n  - Author: Jane\nDecks:\n  - Name: D\n",
		"Topic:\n  Name: X\nProvenance:\n  Author: [1, 2]\nDecks:\n  - Name: D\n",
	} {
		got, err := ImportTopic(src)
		if err != nil {
			t.Fatalf("ImportTopic(%q) failed the whole file: %v", src, err)
		}
		if len(got.Decks) != 1 {
			t.Errorf("ImportTopic(%q) lost the decks: %+v", src, got.Decks)
		}
		if !got.Provenance.IsZero() {
			t.Errorf("ImportTopic(%q) kept junk provenance: %+v", src, got.Provenance)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
