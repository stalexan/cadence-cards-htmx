package yamlio

// Port of web/src/lib/yaml-utils.test.ts, plus round-trip and
// Svelte-compatibility fixtures.

import (
	"strings"
	"testing"
	"time"

	"cadence-cards/internal/sm2"
)

func str(s string) *string { return &s }

func sampleCards() []ExportCard {
	seen := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	g := sm2.GradeCorrectPerfectRecall
	return []ExportCard{
		{
			Front: "Bonjour", Back: "Hello", Note: str("a greeting"), Priority: sm2.PriorityA,
			Tags: []string{"french", "greetings"},
			// Easiness above the 2.5 starting value must survive a roundtrip.
			Forward: sm2.State{LastSeen: &seen, Grade: &g, RepCount: 3, Easiness: 2.7, Interval: 16},
		},
		{
			Front: "Merci", Back: "Thank you", Priority: sm2.PriorityB,
			Forward: sm2.InitialState(),
		},
	}
}

func sampleMeta() *Metadata {
	return &Metadata{FormatVersion: "1.0", DeckName: "French Basics", CreatorName: str("Sean"), ExportDate: "2024-01-20", CardCount: 2}
}

func TestRoundtripCoreFields(t *testing.T) {
	out, err := Export(sampleCards(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	valid, invalid, err := Import(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) != 0 || len(valid) != 2 {
		t.Fatalf("valid=%d invalid=%d", len(valid), len(invalid))
	}
	c := valid[0]
	if c.Front != "Bonjour" || c.Back != "Hello" || c.Priority != "A" {
		t.Errorf("card = %+v", c)
	}
	if len(c.Tags) != 2 || c.Tags[0] != "french" {
		t.Errorf("tags = %v", c.Tags)
	}
	// SM-2 params were not exported, so schema defaults apply on import.
	if c.Easiness != 2.5 || c.Interval != 1 {
		t.Errorf("defaults = easiness %v interval %d", c.Easiness, c.Interval)
	}
}

func TestRoundtripWithMetadataHeader(t *testing.T) {
	out, err := Export(sampleCards(), sampleMeta(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Deck: French Basics") {
		t.Error("missing metadata header")
	}
	valid, invalid, err := Import(out)
	if err != nil || len(invalid) != 0 {
		t.Fatalf("import: %v, invalid=%v", err, invalid)
	}
	if valid[0].Front != "Bonjour" || valid[1].Front != "Merci" {
		t.Errorf("fronts = %v, %v", valid[0].Front, valid[1].Front)
	}
}

func TestRoundtripSM2Params(t *testing.T) {
	out, err := Export(sampleCards(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	valid, _, err := Import(out)
	if err != nil {
		t.Fatal(err)
	}
	c := valid[0]
	if c.LastSeen == nil || *c.LastSeen != "2024-01-15" {
		t.Errorf("LastSeen = %v", c.LastSeen)
	}
	if c.Grade == nil || *c.Grade != string(sm2.GradeCorrectPerfectRecall) {
		t.Errorf("Grade = %v", c.Grade)
	}
	if c.RepCount != 3 || c.Easiness != 2.7 || c.Interval != 16 {
		t.Errorf("SM-2 = %+v", c)
	}
	// Null lastSeen exports as null and survives the roundtrip.
	if valid[1].LastSeen != nil {
		t.Errorf("null LastSeen = %v", valid[1].LastSeen)
	}

	// State conversion parses the date as UTC midnight (new Date('YYYY-MM-DD')).
	st, err := c.ForwardState()
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	if st.LastSeen == nil || !st.LastSeen.Equal(want) {
		t.Errorf("parsed LastSeen = %v, want %v", st.LastSeen, want)
	}
}

func TestRoundtripReverseParams(t *testing.T) {
	cards := sampleCards()
	seen := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	g := sm2.GradeCorrectWithHesitation
	cards[0].Reverse = &sm2.State{LastSeen: &seen, Grade: &g, RepCount: 2, Easiness: 2.4, Interval: 6}

	out, err := Export(cards, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	valid, invalid, err := Import(out)
	if err != nil || len(invalid) != 0 {
		t.Fatalf("import: %v %v", err, invalid)
	}

	c := valid[0]
	if !c.HasReverse {
		t.Fatal("HasReverse should be true")
	}
	rev, err := c.ReverseState()
	if err != nil || rev == nil {
		t.Fatalf("ReverseState = (%v, %v)", rev, err)
	}
	if *rev.Grade != g || rev.RepCount != 2 || rev.Easiness != 2.4 || rev.Interval != 6 {
		t.Errorf("reverse state = %+v", rev)
	}
	// Card without reverse keys has no reverse state.
	if valid[1].HasReverse {
		t.Error("card 2 should not have reverse data")
	}
	if rs, _ := valid[1].ReverseState(); rs != nil {
		t.Errorf("card 2 ReverseState = %+v, want nil", rs)
	}
}

func TestValidationPartitions(t *testing.T) {
	yaml := `
- Front: Good
  Back: Card
  Priority: A
- Back: Missing front
  Priority: B
`
	valid, invalid, err := Import(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 1 || valid[0].Front != "Good" {
		t.Errorf("valid = %+v", valid)
	}
	if len(invalid) != 1 || !strings.Contains(invalid[0].Error, "Card at index 1") {
		t.Errorf("invalid = %+v", invalid)
	}
}

func TestNoEasinessUpperCap(t *testing.T) {
	yaml := `
- Front: Practiced
  Back: Card
  Priority: A
  Easiness: 3.2
`
	valid, invalid, err := Import(yaml)
	if err != nil || len(invalid) != 0 {
		t.Fatalf("import: %v %v", err, invalid)
	}
	if valid[0].Easiness != 3.2 {
		t.Errorf("easiness = %v, want 3.2", valid[0].Easiness)
	}
}

func TestValidationRules(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"easiness below floor", "- Front: a\n  Back: b\n  Priority: A\n  Easiness: 1.2", "Easiness"},
		{"negative repcount", "- Front: a\n  Back: b\n  Priority: A\n  RepCount: -1", "RepCount"},
		{"interval zero", "- Front: a\n  Back: b\n  Priority: A\n  Interval: 0", "Interval"},
		{"bad priority", "- Front: a\n  Back: b\n  Priority: D", "Priority"},
		{"bad grade", "- Front: a\n  Back: b\n  Priority: A\n  Grade: WRONG", "Grade"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid, invalid, err := Import(tc.yaml)
			if err != nil {
				t.Fatal(err)
			}
			if len(valid) != 0 || len(invalid) != 1 || !strings.Contains(invalid[0].Error, tc.want) {
				t.Errorf("valid=%d invalid=%+v", len(valid), invalid)
			}
		})
	}
}

func TestNotAnArray(t *testing.T) {
	if _, _, err := Import("front: not-an-array"); err == nil || !strings.Contains(err.Error(), "array of cards") {
		t.Errorf("err = %v, want array-of-cards error", err)
	}
	if _, _, err := Import("::: not : valid : yaml :::"); err == nil || !strings.Contains(err.Error(), "Error parsing YAML") {
		t.Errorf("err = %v, want parse error", err)
	}
}

// TestImportSvelteExportFixture pins compatibility with the npm-yaml output
// shape produced by the Svelte app (unquoted dates, block sequences, nulls,
// metadata header).
func TestImportSvelteExportFixture(t *testing.T) {
	fixture := `# ============================================
# Flashcard Deck Export
# ============================================
# Format Version: 1.0
# Deck: Vocabulary
# Creator: Sean
# Exported: 2026-08-01
# Cards: 2
# ============================================

- ID: 1
  Front: hola
  Back: hello
  Note: null
  Priority: A
  Tags:
    - greeting
  LastSeen: 2026-07-20
  Grade: CORRECT_PERFECT_RECALL
  RepCount: 2
  Easiness: 2.6
  Interval: 6
  ReverseLastSeen: null
  ReverseGrade: null
  ReverseRepCount: 0
  ReverseEasiness: 2.5
  ReverseInterval: 1
- ID: 2
  Front: adiós
  Back: goodbye
  Note: a farewell
  Priority: B
  Tags: []
  LastSeen: null
  Grade: null
  RepCount: 0
  Easiness: 2.5
  Interval: 1
`
	valid, invalid, err := Import(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) != 0 || len(valid) != 2 {
		t.Fatalf("valid=%d invalid=%+v", len(valid), invalid)
	}

	c := valid[0]
	// npm-yaml emits the date unquoted; it must still arrive as a string.
	if c.LastSeen == nil || *c.LastSeen != "2026-07-20" {
		t.Errorf("LastSeen = %v", c.LastSeen)
	}
	// Reverse keys present (even as nulls) -> reverse schedule detected.
	if !c.HasReverse {
		t.Error("reverse keys with null values should still mark HasReverse")
	}
	rev, err := c.ReverseState()
	if err != nil || rev == nil || rev.Easiness != 2.5 || rev.LastSeen != nil {
		t.Errorf("reverse state = (%+v, %v)", rev, err)
	}
	if valid[1].Note == nil || *valid[1].Note != "a farewell" {
		t.Errorf("note = %v", valid[1].Note)
	}
	if valid[1].HasReverse {
		t.Error("card 2 has no reverse keys")
	}
}

// TestImportBothDirectionsFixture pins the fully-populated shape now produced
// for bidirectional decks: every card carries real values for both directions,
// with an integer where a float is expected and a block-scalar Note.
func TestImportBothDirectionsFixture(t *testing.T) {
	fixture := `- ID: 1
  Front: ABCs
  Back: abecedario
  Note: alphabet is alfabeto
  Priority: A
  Tags: []
  LastSeen: 2026-06-23
  Grade: CORRECT_PERFECT_RECALL
  RepCount: 4
  Easiness: 2.9
  Interval: 49
  ReverseLastSeen: 2026-06-23
  ReverseGrade: CORRECT_PERFECT_RECALL
  ReverseRepCount: 4
  ReverseEasiness: 2.9
  ReverseInterval: 49
- ID: 2
  Front: abortion
  Back: aborto, interrupción del embarazo
  Note: |-
    - "El aborto" is the standard, most common term
    - The verb "to abort" is "abortar"
  Priority: A
  Tags: []
  LastSeen: 2025-11-15
  Grade: CORRECT_PERFECT_RECALL
  RepCount: 6
  Easiness: 3.08
  Interval: 422
  ReverseLastSeen: 2026-04-18
  ReverseGrade: CORRECT_PERFECT_RECALL
  ReverseRepCount: 5
  ReverseEasiness: 3
  ReverseInterval: 147
`
	valid, invalid, err := Import(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) != 0 || len(valid) != 2 {
		t.Fatalf("valid=%d invalid=%+v", len(valid), invalid)
	}

	for i, c := range valid {
		if !c.HasReverse {
			t.Errorf("card %d: HasReverse = false", i)
		}
	}

	fwd, err := valid[0].ForwardState()
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	if fwd.RepCount != 4 || fwd.Easiness != 2.9 || fwd.Interval != 49 ||
		fwd.Grade == nil || *fwd.Grade != sm2.GradeCorrectPerfectRecall ||
		fwd.LastSeen == nil || !fwd.LastSeen.Equal(want) {
		t.Errorf("card 0 forward = %+v", fwd)
	}
	rev, err := valid[0].ReverseState()
	if err != nil {
		t.Fatal(err)
	}
	if rev == nil || rev.RepCount != 4 || rev.Easiness != 2.9 || rev.Interval != 49 ||
		rev.Grade == nil || *rev.Grade != sm2.GradeCorrectPerfectRecall ||
		rev.LastSeen == nil || !rev.LastSeen.Equal(want) {
		t.Errorf("card 0 reverse = %+v", rev)
	}

	// The two directions diverge on card 2, and ReverseEasiness is written as
	// an integer.
	fwd2, _ := valid[1].ForwardState()
	if fwd2.Easiness != 3.08 || fwd2.Interval != 422 || fwd2.RepCount != 6 {
		t.Errorf("card 1 forward = %+v", fwd2)
	}
	rev2, err := valid[1].ReverseState()
	if err != nil {
		t.Fatal(err)
	}
	if rev2 == nil || rev2.Easiness != 3 || rev2.Interval != 147 || rev2.RepCount != 5 {
		t.Errorf("card 1 reverse = %+v", rev2)
	}
	if rev2.LastSeen == nil || !rev2.LastSeen.Equal(time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("card 1 ReverseLastSeen = %v", rev2.LastSeen)
	}
	if valid[1].Note == nil || !strings.Contains(*valid[1].Note, "\n") {
		t.Errorf("block-scalar note = %v", valid[1].Note)
	}
}

// TestExportGolden pins the exact bytes of the Go exporter for a
// representative deck so format drift is caught.
func TestExportGolden(t *testing.T) {
	out, err := Export(sampleCards(), sampleMeta(), true)
	if err != nil {
		t.Fatal(err)
	}
	want := `# ============================================
# Flashcard Deck Export
# ============================================
# Format Version: 1.0
# Deck: French Basics
# Creator: Sean
# Exported: 2024-01-20
# Cards: 2
# ============================================

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
`
	if out != want {
		t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestAnonymousCreator(t *testing.T) {
	meta := sampleMeta()
	meta.CreatorName = nil
	out, err := Export(sampleCards(), meta, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Creator: Anonymous") {
		t.Error("nil creator should fall back to Anonymous")
	}
}

// Markdown card content is routinely multi-line, and the export format has to
// carry it back unchanged. yaml.v3 emits a string containing a newline as a
// literal block scalar, which preserves the breaks exactly; this pins that so
// a future encoder change cannot quietly start folding card text.
func TestRoundTripMultilineMarkdown(t *testing.T) {
	note := "Mnemonic:\n\n1. first\n2. second"
	front := "# Heading\n\n- bullet one\n- bullet two\n\n```go\nfmt.Println(\"hi\")\n```"
	back := "A single line that is quite a lot longer than eighty characters, to be sure the encoder does not fold it."

	out, err := Export([]ExportCard{{
		Front: front, Back: back, Note: &note,
		Priority: sm2.PriorityB, Tags: []string{"md"},
		Forward: sm2.InitialState(),
	}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Front: |-") {
		t.Errorf("multi-line front was not written as a literal block:\n%s", out)
	}

	valid, invalid, err := Import(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) != 0 || len(valid) != 1 {
		t.Fatalf("re-import gave %d valid, %d invalid", len(valid), len(invalid))
	}
	got := valid[0]
	if got.Front != front {
		t.Errorf("front round-trip:\n got %q\nwant %q", got.Front, front)
	}
	if got.Back != back {
		t.Errorf("back round-trip (folded?):\n got %q\nwant %q", got.Back, back)
	}
	if got.Note == nil || *got.Note != note {
		t.Errorf("note round-trip: got %v, want %q", got.Note, note)
	}
}
