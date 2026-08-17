package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cadence-cards/internal/samples"
	"cadence-cards/internal/store"
)

func TestSamplesGallery(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	all, err := samples.All()
	if err != nil {
		t.Fatal(err)
	}

	w := app.do("GET", "/samples", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("gallery = %d", w.Code)
	}
	body := w.Body.String()
	for _, s := range all {
		if !strings.Contains(body, s.Name) {
			t.Errorf("gallery missing sample %q", s.Name)
		}
		if !strings.Contains(body, `hx-post="/samples/`+s.Slug+`"`) {
			t.Errorf("sample %q has no add button", s.Slug)
		}
		if !strings.Contains(body, s.Description) {
			t.Errorf("sample %q has no blurb", s.Slug)
		}
	}
}

// Adding a sample is the whole feature: it must produce a topic the user can
// study immediately, with its decks, its cards, its tutor configuration and its
// attribution all intact.
func TestSampleImportCreatesAStudyableTopic(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	ctx := context.Background()
	u, _, err := app.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		t.Fatal(err)
	}

	all, err := samples.All()
	if err != nil {
		t.Fatal(err)
	}
	// The bidirectional language sample, so the reverse schedules are covered.
	var sample samples.Sample
	for _, s := range all {
		if s.Slug == "spanish-essentials" {
			sample = s
		}
	}
	if sample.Slug == "" {
		t.Fatal("spanish-essentials sample not found")
	}

	w := app.do("POST", "/samples/"+sample.Slug, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("add = %d, body: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "Sample added.") ||
		!strings.Contains(body, sample.Name) {
		t.Errorf("result fragment: %s", body)
	}

	topics, err := app.store.ListTopics(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 {
		t.Fatalf("topics = %d, want 1", len(topics))
	}
	topic := topics[0]
	if topic.Name != sample.Name {
		t.Errorf("topic name = %q, want %q", topic.Name, sample.Name)
	}
	if topic.CardCount != sample.CardCount {
		t.Errorf("cards = %d, want %d", topic.CardCount, sample.CardCount)
	}
	// The tutor configuration is the reason a sample beats an empty topic.
	if topic.Expertise == nil || topic.Example == nil {
		t.Errorf("tutor configuration was not carried: %+v", topic)
	}
	// And the attribution, which the samples exist partly to demonstrate.
	if topic.Author == nil || topic.License == nil {
		t.Errorf("provenance was not carried: %+v", topic)
	}

	// Every card must be due now, or the first study session is empty and the
	// sample has taught the user that the app does nothing. Bidirectional deck,
	// so that is one due schedule per direction per card.
	due, err := app.store.CountDue(ctx, u.ID, topic.ID,
		store.StudyFilter{IncludeNew: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if due != sample.CardCount*2 {
		t.Errorf("due schedules = %d, want %d (every card, both directions)", due, sample.CardCount*2)
	}
}

// Adding the same sample twice is a thing people do. It must not 500 on the
// UNIQUE(name, user_id) constraint, and the user must be told what happened.
func TestSampleImportTwiceRenames(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	if w := app.do("POST", "/samples/getting-started", nil); w.Code != http.StatusOK {
		t.Fatalf("first add = %d", w.Code)
	}
	w := app.do("POST", "/samples/getting-started", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("second add = %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "(2)") || !strings.Contains(body, "already existed") {
		t.Errorf("second add should explain the rename: %s", body)
	}
}

// The response is swapped into the page, and htmx discards 404 bodies — a 404
// here would leave a button that does nothing at all when clicked.
func TestSampleImportUnknownSlugRendersVisibleFragment(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/samples/no-such-sample", nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown slug = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no longer available") {
		t.Errorf("expected an explanation, got: %s", w.Body.String())
	}
}

func TestSamplePreviewServesRawYAML(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("GET", "/samples/go-concurrency/preview", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "Provenance:") || !strings.Contains(body, "Topic:") {
		t.Errorf("preview body: %.200s", body)
	}

	if w := app.do("GET", "/samples/nope/preview", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown preview = %d, want 404", w.Code)
	}
}

// Everything under /samples writes to the user's account or reads bundled
// content on their behalf, so all of it sits behind auth like every other route.
func TestSampleRoutesRequireAuth(t *testing.T) {
	app := newTestApp(t, nil)
	for _, c := range []struct {
		method, path string
		form         url.Values
	}{
		{"GET", "/samples", nil},
		{"GET", "/samples/getting-started/preview", nil},
		{"POST", "/samples/getting-started", url.Values{}},
	} {
		if w := app.do(c.method, c.path, c.form); w.Code != http.StatusSeeOther {
			t.Errorf("%s %s = %d, want redirect to login", c.method, c.path, w.Code)
		}
	}
}

// The gallery is useless if nothing points at it, and the places that point at
// it are exactly where a new account with nothing in it lands.
func TestEmptyAccountIsPointedAtTheSamples(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	for _, path := range []string{"/dashboard", "/topics", "/import"} {
		body := app.do("GET", path, nil).Body.String()
		if !strings.Contains(body, `href="/samples"`) {
			t.Errorf("%s does not link to /samples", path)
		}
	}

	// Once there is a topic, the dashboard nudge has done its job and goes away.
	app.seed(false)
	if body := app.do("GET", "/dashboard", nil).Body.String(); strings.Contains(body, `href="/samples"`) {
		t.Error("dashboard still nudges after a topic exists")
	}
}

// An account with topics renders neither the dashboard nudge nor the topics
// empty state, which between them were the only prominent links — leaving the
// gallery reachable only by typing the URL. The Topics header is the one entry
// point that does not disappear the moment the app is in use.
func TestSamplesStayReachableOnceTopicsExist(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	body := app.do("GET", "/topics", nil).Body.String()
	if strings.Contains(body, "No topics yet") {
		t.Fatal("expected a populated topics page")
	}
	if !strings.Contains(body, `href="/samples"`) {
		t.Error("topics page does not link to /samples once topics exist")
	}
	if !strings.Contains(app.do("GET", "/import", nil).Body.String(), `href="/samples"`) {
		t.Error("import page does not link to /samples")
	}
}
