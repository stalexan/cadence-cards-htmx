package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

// TestDeckExport covers the download headers and the SM-2 toggle. Neither was
// exercised before, so the Content-Disposition filename and the dormant-reverse
// guard below had no regression cover.
func TestDeckExport(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)

	w := app.do("GET", "/decks/"+itoa(card.DeckID)+"/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("export = %d", w.Code)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="vocab_cards.yaml"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "text/yaml" {
		t.Errorf("Content-Type = %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Front: hola") || !strings.Contains(body, "# Deck: Vocab") {
		t.Errorf("export body: %s", body)
	}
	// Without the flag the SM-2 block is omitted entirely.
	if strings.Contains(body, "Easiness:") {
		t.Errorf("export without SM-2 leaked study params: %s", body)
	}

	w = app.do("GET", "/decks/"+itoa(card.DeckID)+"/export?includeSm2Params=true", nil)
	if !strings.Contains(w.Body.String(), "Easiness: 2.5") {
		t.Errorf("export with SM-2 missing params: %s", w.Body.String())
	}

	// The preview serves the same bytes as text/plain, with no attachment
	// header, so htmx can swap it into the textarea.
	w = app.do("GET", "/decks/"+itoa(card.DeckID)+"/export-preview", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("preview = %d, disposition %q", w.Code, w.Header().Get("Content-Disposition"))
	}
}

// The export writes the schedule's stored easiness straight through, so this is
// the end of the rounding chain: a card studied enough to reach an EF the
// unrounded formula would render as 2.8000000000000003 must export as "2.8".
func TestDeckExportWritesRoundedEasiness(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	ctx := context.Background()

	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	// Three perfect recalls: 2.5 -> 2.6 -> 2.7 -> 2.8.
	sc := card.ForwardSchedule()
	for range 3 {
		graded, err := app.store.RecordReview(ctx, u.ID, sc.ID, sm2.GradeCorrectPerfectRecall,
			sc.Version, time.Now())
		if err != nil {
			t.Fatalf("RecordReview: %v", err)
		}
		sc = &graded
	}

	body := app.do("GET", "/decks/"+itoa(card.DeckID)+"/export?includeSm2Params=true", nil).Body.String()
	if !strings.Contains(body, "Easiness: 2.8\n") {
		t.Errorf("export easiness not rounded: %s", body)
	}
}

// A reverse schedule left behind when bidirectionality was switched off must
// not be exported: re-importing it would flip the target deck back.
func TestDeckExportOmitsDormantReverseSchedule(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(true) // bidirectional: the card gets a reverse schedule
	ctx := context.Background()

	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	deck, err := app.store.GetDeck(ctx, u.ID, card.DeckID)
	if err != nil {
		t.Fatal(err)
	}
	// Switch it off; the reverse schedule stays behind, dormant.
	if _, err := app.store.UpdateDeck(ctx, u.ID, deck.ID, store.DeckParams{
		Name: deck.Name, TopicID: deck.TopicID, IsBidirectional: false,
	}); err != nil {
		t.Fatal(err)
	}

	w := app.do("GET", "/decks/"+itoa(deck.ID)+"/export?includeSm2Params=true", nil)
	if strings.Contains(w.Body.String(), "ReverseEasiness") {
		t.Errorf("dormant reverse schedule leaked into the export: %s", w.Body.String())
	}
}

func TestTopicExport(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	ctx := context.Background()
	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	deck, err := app.store.GetDeck(ctx, u.ID, card.DeckID)
	if err != nil {
		t.Fatal(err)
	}
	topicID := deck.TopicID

	// Config-only: no Decks key at all.
	w := app.do("GET", "/topics/"+itoa(topicID)+"/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("export = %d", w.Code)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="spanish_topic.yaml"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Topic:") || !strings.Contains(body, "Name: Spanish") {
		t.Errorf("topic export body: %s", body)
	}
	// "\nDecks:" and not "Decks:" — the comment header carries a "# Decks: 0"
	// line that a bare substring check would match.
	if strings.Contains(body, "\nDecks:") {
		t.Errorf("export without decks should omit the key: %s", body)
	}

	// With decks and cards.
	w = app.do("GET", "/topics/"+itoa(topicID)+"/export?includeDecks=true", nil)
	body = w.Body.String()
	for _, want := range []string{"\nDecks:", "Name: Vocab", "Front: hola", "IsBidirectional: false"} {
		if !strings.Contains(body, want) {
			t.Errorf("export missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Easiness:") {
		t.Errorf("export without SM-2 leaked study params: %s", body)
	}

	w = app.do("GET", "/topics/"+itoa(topicID)+"/export?includeDecks=true&includeSm2Params=true", nil)
	if !strings.Contains(w.Body.String(), "Easiness: 2.5") {
		t.Errorf("export with SM-2 missing params: %s", w.Body.String())
	}

	w = app.do("GET", "/topics/"+itoa(topicID)+"/export-preview?includeDecks=true", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("preview = %d, disposition %q", w.Code, w.Header().Get("Content-Disposition"))
	}
}

// Another user's topic must 404 rather than export.
func TestTopicExportIsScopedToOwner(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	ctx := context.Background()
	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	deck, err := app.store.GetDeck(ctx, u.ID, card.DeckID)
	if err != nil {
		t.Fatal(err)
	}

	// Registering again swaps the session to a second user on the same store.
	app.login("other@example.com")
	w := app.do("GET", "/topics/"+itoa(deck.TopicID)+"/export", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("other user's export = %d, want 404", w.Code)
	}
}

// A topic export round-trips through /import into a new topic.
func TestTopicImportEndpoint(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	ctx := context.Background()
	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	deck, err := app.store.GetDeck(ctx, u.ID, card.DeckID)
	if err != nil {
		t.Fatal(err)
	}

	exported := app.do("GET",
		"/topics/"+itoa(deck.TopicID)+"/export?includeDecks=true&includeSm2Params=true", nil).Body.String()

	// No deckId: a topic file brings its own decks.
	w := app.do("POST", "/import", url.Values{"yamlContent": {exported}})
	if w.Code != http.StatusOK {
		t.Fatalf("topic import = %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Created topic") || !strings.Contains(body, "Spanish (2)") {
		t.Errorf("import result: %s", body)
	}
	if !strings.Contains(body, "1 deck") || !strings.Contains(body, "1 card") {
		t.Errorf("import counts wrong: %s", body)
	}
	if !strings.Contains(body, "already existed") {
		t.Errorf("rename should be explained: %s", body)
	}

	topics, err := app.store.ListTopics(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 {
		t.Fatalf("topics = %d, want 2", len(topics))
	}
	cards, total, err := app.store.ListCards(ctx, u.ID, store.CardListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("cards = %d, want 2", total)
	}
	for _, c := range cards {
		if c.Front != "hola" {
			t.Errorf("unexpected card %q", c.Front)
		}
	}
}

// A topic file imported into a fresh account creates everything from nothing —
// the case the import page's old "no decks" empty state used to block.
func TestTopicImportWithNoExistingDecks(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	src := `Topic:
  Name: German
  Expertise: strict tutor
Decks:
  - Name: Verbs
    IsBidirectional: true
    Cards:
      - Front: sprechen
        Back: to speak
        Priority: A
        Tags: []
`
	w := app.do("POST", "/import", url.Values{"yamlContent": {src}})
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Created topic") {
		t.Errorf("import result: %s", w.Body.String())
	}

	ctx := context.Background()
	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	topics, err := app.store.ListTopics(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Name != "German" {
		t.Fatalf("topics = %+v", topics)
	}
	if topics[0].Expertise == nil || *topics[0].Expertise != "strict tutor" {
		t.Errorf("prompt config not carried: %+v", topics[0])
	}
	cards, _, err := app.store.ListCards(ctx, u.ID, store.CardListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].ReverseSchedule() == nil {
		t.Errorf("bidirectional deck should have given the card a reverse schedule: %+v", cards)
	}
	// The import page must render for an account that still has no decks.
	if w := app.do("GET", "/import", nil); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "yamlContent") {
		t.Errorf("import page with no decks = %d", w.Code)
	}
}

// Bad cards inside a topic file cost their card, not the import.
func TestTopicImportPartialFailure(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	src := "Topic:\n  Name: German\nDecks:\n  - Name: Verbs\n    Cards:\n" +
		"      - Front: sprechen\n        Back: to speak\n        Priority: A\n" +
		"      - Back: no front\n        Priority: B\n"
	w := app.do("POST", "/import", url.Values{"yamlContent": {src}})
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "1 card") || !strings.Contains(body, "1 item") {
		t.Errorf("counts should show 1 imported / 1 failed: %s", body)
	}
	if !strings.Contains(body, "card at index 1") {
		t.Errorf("per-card error should be listed: %s", body)
	}
}

// Unrecognized YAML must render a visible 422 rather than a discarded 4xx.
func TestImportUnknownFormatRendersVisibleFragment(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	w := app.do("POST", "/import", url.Values{"yamlContent": {"Decks: []\n"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown format = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Unrecognized YAML") {
		t.Errorf("missing message: %s", w.Body.String())
	}
}

// A card list still requires a target deck; the message is now specific to it.
func TestCardImportStillRequiresDeck(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	w := app.do("POST", "/import", url.Values{"yamlContent": {"- Front: a\n  Back: b\n  Priority: A\n"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("card import without deck = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "target deck is required") {
		t.Errorf("missing message: %s", w.Body.String())
	}
}

// A topic file whose Topic block is unusable fails as a 422, not a 500.
func TestTopicImportRejectsMissingName(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/import", url.Values{"yamlContent": {"Topic:\n  Name: \"\"\n"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("nameless topic = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Topic Name is required") {
		t.Errorf("missing message: %s", w.Body.String())
	}
}

// Guards the sanitizer used for both export filenames.
func TestSanitizeFilename(t *testing.T) {
	// Non-ASCII collapses to one underscore per rune, not per byte.
	cases := map[string]string{
		"Vocab":          "vocab",
		"French Basics":  "french_basics",
		"Añejo/Tricky!":  "a_ejo_tricky_",
		"Über Cards 101": "_ber_cards_101",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- Import format detection ----------

// The hint is what tells the user whether the target-deck field applies, so it
// has to agree with what POST /import would actually do with the same bytes.
func TestImportDetect(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	// A real topic export, fetched from the export endpoint rather than
	// hand-written, so the hint is tested against bytes the app produces.
	u, _, err := app.store.GetUserByEmail(context.Background(), "t@example.com")
	if err != nil {
		t.Fatal(err)
	}
	topics, err := app.store.ListTopics(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	exp := app.do("GET", "/topics/"+itoa(topics[0].ID)+"/export?includeDecks=true", nil)
	if exp.Code != http.StatusOK {
		t.Fatalf("export status = %d", exp.Code)
	}

	cases := []struct {
		name    string
		yaml    string
		format  string
		wantAll []string
	}{
		{
			name:    "topic export",
			yaml:    exp.Body.String(),
			format:  "topic",
			wantAll: []string{"Topic export detected", "Spanish", "1 deck", "1 card"},
		},
		{
			name:    "config-only topic",
			yaml:    "Topic:\n  Name: Spanish\n",
			format:  "topic",
			wantAll: []string{"Topic export detected", "Settings only"},
		},
		{
			name:    "card list",
			yaml:    "- Front: hola\n  Back: hello\n  Priority: A\n- Front: adios\n  Back: bye\n  Priority: B\n",
			format:  "cards",
			wantAll: []string{"Card list detected", "2 cards ready"},
		},
		{
			name:    "garbage",
			yaml:    "just some prose, not yaml at all: [",
			format:  "unknown",
			wantAll: []string{"does not look like"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := app.do("POST", "/import/detect", url.Values{"yamlContent": {c.yaml}})
			// Always 200: the hint is advisory, and htmx would discard the
			// body on most error statuses anyway.
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, `data-import-format="`+c.format+`"`) {
				t.Errorf("want format %q, got: %s", c.format, body)
			}
			for _, want := range c.wantAll {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q in: %s", want, body)
				}
			}
		})
	}
}

// An empty box renders an empty verdict, which is what restores the deck field
// after the user clears the textarea.
func TestImportDetectEmpty(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/import/detect", url.Values{"yamlContent": {"   \n  "}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-import-format=""`) {
		t.Errorf("want empty format, got: %s", body)
	}
	if strings.Contains(body, "alert") {
		t.Errorf("empty input should render no alert: %s", body)
	}
}

// Detection is behind auth like every other import route.
func TestImportDetectRequiresAuth(t *testing.T) {
	app := newTestApp(t, nil)
	w := app.do("POST", "/import/detect", url.Values{"yamlContent": {"Topic:\n  Name: X\n"}})
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want redirect to login", w.Code)
	}
}

// Choosing and dropping a file are both client-side (app.js reads the file
// into the textarea), so the only thing the server can guarantee is that the
// wiring attributes ship. Without them the button renders and silently does
// nothing.
func TestImportPageOffersFilePicker(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	body := app.do("GET", "/import", nil).Body.String()
	for _, want := range []string{
		`type="file"`,
		`data-file-input`,
		`accept=".yaml,.yml,text/yaml"`,
		`data-file-drop`,           // the drag-and-drop zone
		`data-file-into="#f-yaml"`, // names the textarea both routes fill
		`data-file-name`,           // the "which file did I pick" readout
	} {
		if !strings.Contains(body, want) {
			t.Errorf("import page missing %q", want)
		}
	}
	// A named file input would be submitted alongside the textarea and force
	// the form to multipart, which the handler does not parse.
	if strings.Contains(body, `type="file" name=`) || strings.Contains(body, `name="file"`) {
		t.Error("file input must not carry a name attribute")
	}
}
