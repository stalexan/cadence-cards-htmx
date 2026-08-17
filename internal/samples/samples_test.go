package samples

import (
	"strings"
	"testing"

	"cadence-cards/internal/yamlio"
)

// The bundled files are data, and data with no test is data that breaks in
// front of a user. Every assertion here is something a new account's first
// click depends on, so a bad sample fails the build — including the Docker
// build, which runs the unit tests — rather than the gallery.
func TestCatalogLoads(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("catalog failed to load: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no samples bundled")
	}

	seen := map[string]bool{}
	for _, s := range all {
		if seen[s.Slug] {
			t.Errorf("duplicate slug %q", s.Slug)
		}
		seen[s.Slug] = true

		if s.Name == "" {
			t.Errorf("%s: no topic name", s.Slug)
		}
		// The gallery card is title + blurb + counts; a sample with no blurb
		// renders as a bare name and tells a new user nothing.
		if s.Description == "" {
			t.Errorf("%s: no TopicDescription to use as a blurb", s.Slug)
		}
		if s.DeckCount == 0 || s.CardCount == 0 {
			t.Errorf("%s: %d decks / %d cards — a sample with nothing to study is not a sample",
				s.Slug, s.DeckCount, s.CardCount)
		}
	}
}

// Every prompt-config field is set deliberately. A sample exists to show what a
// well-configured topic looks like, and one that leans on claude.PromptDefaults
// teaches the opposite of what the topic form is asking for.
func TestSamplesConfigureTheTutorFully(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		c := s.Topic.Config
		for _, f := range []struct {
			name string
			val  *string
		}{
			{"TopicDescription", c.TopicDescription},
			{"Expertise", c.Expertise},
			{"Focus", c.Focus},
			{"ContextType", c.ContextType},
			{"Example", c.Example},
			{"Question", c.Question},
		} {
			if f.val == nil || strings.TrimSpace(*f.val) == "" {
				t.Errorf("%s: %s is blank", s.Slug, f.name)
			}
		}
	}
}

// Samples are redistributable by construction, and they are also the app's own
// demonstration of the Provenance block — one shipped without attribution would
// be advertising the opposite of what it is for.
func TestSamplesCarryProvenance(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		p := s.Topic.Provenance
		if p.IsZero() {
			t.Errorf("%s: no Provenance block", s.Slug)
			continue
		}
		if p.Author == nil || p.License == nil {
			t.Errorf("%s: provenance needs at least an Author and a License, got %+v", s.Slug, p)
		}
	}
}

// Cards must arrive new. A sample carrying someone's SM-2 history would hand a
// new user a topic that is half "already learned" and mostly not due, so the
// first study session — the entire point of adding a sample — would come up
// empty.
func TestSamplesCarryNoStudyState(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		for _, d := range s.Topic.Decks {
			for _, c := range d.Cards {
				if c.LastSeen != nil || c.Grade != nil {
					t.Errorf("%s: card %q carries study history (LastSeen/Grade)", s.Slug, c.Front)
				}
				fwd, err := c.ForwardState()
				if err != nil {
					t.Fatalf("%s: card %q: %v", s.Slug, c.Front, err)
				}
				if fwd.RepCount != 0 {
					t.Errorf("%s: card %q starts at RepCount %d, want 0", s.Slug, c.Front, fwd.RepCount)
				}
			}
		}
		// Nor may the raw file mention them — the check above reads defaults,
		// so a stray key that parsed into the default would slip past it.
		for _, key := range []string{"RepCount:", "Easiness:", "Interval:", "LastSeen:", "Grade:"} {
			if strings.Contains(s.YAML, key) {
				t.Errorf("%s: file contains SM-2 key %q; export samples with study progress off", s.Slug, key)
			}
		}
	}
}

// A sample has to survive the same round trip a user's own export does, since
// that is exactly what happens the first time someone re-exports the topic they
// added.
func TestSamplesRoundTrip(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if got := yamlio.Detect(s.YAML); got != yamlio.FormatTopic {
			t.Errorf("%s: Detect = %v, want FormatTopic", s.Slug, got)
		}
		again, err := yamlio.ImportTopic(s.YAML)
		if err != nil {
			t.Fatalf("%s: re-import failed: %v", s.Slug, err)
		}
		if again.Config.Name != s.Name || len(again.Decks) != s.DeckCount {
			t.Errorf("%s: re-import diverged: %q / %d decks", s.Slug, again.Config.Name, len(again.Decks))
		}
	}
}

func TestGet(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	want := all[0]

	got, ok, err := Get(want.Slug)
	if err != nil || !ok {
		t.Fatalf("Get(%q) = ok %v, err %v", want.Slug, ok, err)
	}
	if got.Name != want.Name {
		t.Errorf("Get(%q).Name = %q, want %q", want.Slug, got.Name, want.Name)
	}

	if _, ok, err := Get("no-such-sample"); err != nil || ok {
		t.Errorf("Get(unknown) = ok %v, err %v; want false, nil", ok, err)
	}
	// Slugs come off a URL, so the lookup must not be talked into a file.
	if _, ok, _ := Get("../../etc/passwd"); ok {
		t.Error("Get accepted a traversal slug")
	}
}
