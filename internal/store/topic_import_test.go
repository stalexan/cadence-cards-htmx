package store

import (
	"testing"
	"time"

	"cadence-cards/internal/sm2"
)

func sampleTopicImport() TopicImportParams {
	lastSeen := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)
	grade := sm2.GradeCorrectPerfectRecall
	return TopicImportParams{
		Topic: TopicParams{
			Name:             "Spanish",
			TopicDescription: strPtr("Travel Spanish"),
			Expertise:        strPtr("patient tutor"),
		},
		Decks: []DeckImportParams{
			{
				Name:            "Verbs",
				Field1Label:     strPtr("Spanish"),
				IsBidirectional: true,
				Cards: []ImportCardParams{
					{Front: "hablar", Back: "to speak", Priority: sm2.PriorityA, Tags: []string{"verb"},
						Forward: sm2.State{LastSeen: &lastSeen, Grade: &grade, RepCount: 3, Easiness: 2.8, Interval: 12},
						Reverse: &sm2.State{RepCount: 1, Easiness: 2.5, Interval: 1}},
					{Front: "comer", Back: "to eat", Priority: sm2.PriorityB, Forward: sm2.InitialState()},
				},
			},
			{Name: "Nouns", Cards: []ImportCardParams{
				{Front: "casa", Back: "house", Priority: sm2.PriorityC, Forward: sm2.InitialState()},
			}},
		},
	}
}

func strPtr(s string) *string { return &s }

func TestImportTopic(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	res, err := s.ImportTopic(ctx, u.ID, sampleTopicImport())
	if err != nil {
		t.Fatalf("ImportTopic: %v", err)
	}
	if res.TopicName != "Spanish" || res.Renamed {
		t.Errorf("first import should keep the name: %+v", res)
	}
	if res.DeckCount != 2 || res.CardCount != 3 {
		t.Errorf("counts = %d decks / %d cards, want 2/3", res.DeckCount, res.CardCount)
	}

	topic, err := s.GetTopic(ctx, u.ID, res.TopicID)
	if err != nil {
		t.Fatalf("GetTopic: %v", err)
	}
	if topic.TopicDescription == nil || *topic.TopicDescription != "Travel Spanish" {
		t.Errorf("prompt config was not carried: %+v", topic)
	}

	decks, err := s.ListDecks(ctx, u.ID, &res.TopicID)
	if err != nil {
		t.Fatalf("ListDecks: %v", err)
	}
	if len(decks) != 2 {
		t.Fatalf("decks = %d", len(decks))
	}
	byName := map[string]Deck{}
	for _, d := range decks {
		byName[d.Name] = d
	}
	if v := byName["Verbs"]; !v.IsBidirectional || v.Field1Label == nil || *v.Field1Label != "Spanish" {
		t.Errorf("deck settings were not carried: %+v", v)
	}
	// "Nouns" carried no reverse params and said nothing about direction.
	if byName["Nouns"].IsBidirectional {
		t.Error("Nouns should have stayed unidirectional")
	}

	cards, _, err := s.ListCards(ctx, u.ID, CardListParams{TopicID: &res.TopicID})
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if len(cards) != 3 {
		t.Fatalf("cards = %d", len(cards))
	}
	var hablar Card
	for _, c := range cards {
		if c.Front == "hablar" {
			hablar = c
		}
	}
	fwd := hablar.ForwardSchedule()
	if fwd == nil || fwd.RepCount != 3 || fwd.Easiness != 2.8 || fwd.Interval != 12 {
		t.Errorf("forward schedule = %+v", fwd)
	}
	rev := hablar.ReverseSchedule()
	if rev == nil || rev.RepCount != 1 {
		t.Errorf("reverse schedule = %+v", rev)
	}
	if len(hablar.Tags) != 1 || hablar.Tags[0] != "verb" {
		t.Errorf("tags = %v", hablar.Tags)
	}
	// comer has no reverse params, but its deck is bidirectional, so it still
	// gets a reverse schedule at the initial state.
	for _, c := range cards {
		if c.Front == "comer" && c.ReverseSchedule() == nil {
			t.Error("bidirectional deck should give every card a reverse schedule")
		}
	}
}

// Importing the same file twice must succeed, suffixing the name rather than
// failing on the UNIQUE(name, user_id) constraint.
func TestImportTopicSuffixesDuplicateName(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	want := []struct {
		name    string
		renamed bool
	}{{"Spanish", false}, {"Spanish (2)", true}, {"Spanish (3)", true}}
	for i, w := range want {
		res, err := s.ImportTopic(ctx, u.ID, sampleTopicImport())
		if err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
		if res.TopicName != w.name || res.Renamed != w.renamed {
			t.Errorf("import %d = %q (renamed %v), want %q (renamed %v)",
				i, res.TopicName, res.Renamed, w.name, w.renamed)
		}
	}

	topics, err := s.ListTopics(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if len(topics) != 3 {
		t.Errorf("topics = %d, want 3", len(topics))
	}
}

// The suffix also has to dodge a name a plain CreateTopic already took.
func TestImportTopicSuffixesAroundExistingTopic(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.CreateTopic(ctx, u.ID, TopicParams{Name: "Spanish"}); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	res, err := s.ImportTopic(ctx, u.ID, sampleTopicImport())
	if err != nil {
		t.Fatalf("ImportTopic: %v", err)
	}
	if res.TopicName != "Spanish (2)" || !res.Renamed {
		t.Errorf("got %q (renamed %v), want \"Spanish (2)\"", res.TopicName, res.Renamed)
	}
}

// Topic names are unique per user, so another user importing the same file
// keeps the unsuffixed name.
func TestImportTopicIsPerUser(t *testing.T) {
	s := newTestStore(t)
	u1, err := s.CreateUser(ctx, "One", "one@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u2, err := s.CreateUser(ctx, "Two", "two@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := s.ImportTopic(ctx, u1.ID, sampleTopicImport()); err != nil {
		t.Fatalf("import u1: %v", err)
	}
	res, err := s.ImportTopic(ctx, u2.ID, sampleTopicImport())
	if err != nil {
		t.Fatalf("import u2: %v", err)
	}
	if res.TopicName != "Spanish" || res.Renamed {
		t.Errorf("second user got %q (renamed %v), want an unsuffixed name", res.TopicName, res.Renamed)
	}
	// u2's topic must not be visible to u1.
	if _, err := s.GetTopic(ctx, u1.ID, res.TopicID); err == nil {
		t.Error("u1 can read u2's imported topic")
	}
}

// A file naming two decks the same would hit UNIQUE(name, topic_id); suffix
// instead so the whole import doesn't fail.
func TestImportTopicDuplicateDeckNamesInFile(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p := TopicImportParams{
		Topic: TopicParams{Name: "Spanish"},
		Decks: []DeckImportParams{
			{Name: "Verbs", Cards: []ImportCardParams{{Front: "a", Back: "b", Priority: sm2.PriorityA, Forward: sm2.InitialState()}}},
			{Name: "Verbs", Cards: []ImportCardParams{{Front: "c", Back: "d", Priority: sm2.PriorityA, Forward: sm2.InitialState()}}},
			{Name: "Verbs"},
		},
	}
	res, err := s.ImportTopic(ctx, u.ID, p)
	if err != nil {
		t.Fatalf("ImportTopic: %v", err)
	}
	if res.DeckCount != 3 {
		t.Errorf("decks = %d, want 3", res.DeckCount)
	}
	if len(res.DeckRenames) != 2 {
		t.Errorf("renames = %v, want 2", res.DeckRenames)
	}

	decks, err := s.ListDecks(ctx, u.ID, &res.TopicID)
	if err != nil {
		t.Fatalf("ListDecks: %v", err)
	}
	names := map[string]bool{}
	for _, d := range decks {
		names[d.Name] = true
	}
	for _, want := range []string{"Verbs", "Verbs (2)", "Verbs (3)"} {
		if !names[want] {
			t.Errorf("missing deck %q; got %v", want, names)
		}
	}
}

// Reverse params in the file mean the deck is studied both ways, the same rule
// ImportCards applies to an existing deck.
func TestImportTopicReverseParamsFlipDeck(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p := TopicImportParams{
		Topic: TopicParams{Name: "Spanish"},
		Decks: []DeckImportParams{{
			Name:            "Verbs",
			IsBidirectional: false, // the file says no...
			Cards: []ImportCardParams{{
				Front: "hablar", Back: "to speak", Priority: sm2.PriorityA,
				Forward: sm2.InitialState(),
				// ...but reverse progress says otherwise.
				Reverse: &sm2.State{RepCount: 4, Easiness: 2.9, Interval: 49},
			}},
		}},
	}
	res, err := s.ImportTopic(ctx, u.ID, p)
	if err != nil {
		t.Fatalf("ImportTopic: %v", err)
	}
	decks, err := s.ListDecks(ctx, u.ID, &res.TopicID)
	if err != nil {
		t.Fatalf("ListDecks: %v", err)
	}
	if !decks[0].IsBidirectional {
		t.Error("reverse params should have made the deck bidirectional")
	}
	cards, _, err := s.ListCards(ctx, u.ID, CardListParams{TopicID: &res.TopicID})
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	rev := cards[0].ReverseSchedule()
	if rev == nil || rev.RepCount != 4 || rev.Easiness != 2.9 || rev.Interval != 49 {
		t.Errorf("reverse schedule = %+v", rev)
	}
}

// A config-only file (no decks) is legal and creates an empty topic.
func TestImportTopicConfigOnly(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Test", "test@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	res, err := s.ImportTopic(ctx, u.ID, TopicImportParams{
		Topic: TopicParams{Name: "Spanish", Question: strPtr("ask tersely")},
	})
	if err != nil {
		t.Fatalf("ImportTopic: %v", err)
	}
	if res.DeckCount != 0 || res.CardCount != 0 {
		t.Errorf("counts = %d/%d, want 0/0", res.DeckCount, res.CardCount)
	}
	topic, err := s.GetTopic(ctx, u.ID, res.TopicID)
	if err != nil {
		t.Fatalf("GetTopic: %v", err)
	}
	if topic.Question == nil || *topic.Question != "ask tersely" {
		t.Errorf("question = %v", topic.Question)
	}
}
