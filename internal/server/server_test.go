package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	cadence "cadence-cards"
	"cadence-cards/internal/claude"
	"cadence-cards/internal/config"
	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

// stubAI implements the AI interface without network calls.
type stubAI struct {
	question   string
	reply      string
	suggestion claude.TopicSuggestion
	err        error
}

func (s stubAI) GenerateQuestion(context.Context, claude.TopicConfig, claude.CardContent) (string, error) {
	return s.question, s.err
}
func (s stubAI) ChatAboutQuestion(context.Context, claude.TopicConfig, claude.CardContent, string, []claude.Message) (string, error) {
	return s.reply, s.err
}
func (s stubAI) ChatAboutTopic(context.Context, claude.TopicConfig, string, []claude.Message, bool) (string, error) {
	return s.reply, s.err
}
func (s stubAI) SuggestTopicConfig(context.Context, string) (claude.TopicSuggestion, error) {
	return s.suggestion, s.err
}

type testApp struct {
	t       *testing.T
	store   *store.Store
	handler http.Handler
	cookie  *http.Cookie
}

func newTestApp(t *testing.T, ai AI) *testApp {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		Port: 0, CookieSecure: false, EnablePublicRegistration: true,
		ClaudeModel: "test", ClaudeMaxTokens: 100,
	}
	if ai == nil {
		ai = stubAI{question: "What does 'hola' mean?", reply: "Correct!"}
	}
	_, handler, err := New(cfg, st, ai)
	if err != nil {
		t.Fatal(err)
	}
	return &testApp{t: t, store: st, handler: handler}
}

// do performs a request with the session cookie and a trusted client IP.
func (a *testApp) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	a.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("X-Real-IP", "10.0.0.9")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, req)
	return w
}

// login registers and signs in a user, capturing the session cookie.
func (a *testApp) login(email string) {
	a.t.Helper()
	w := a.do("POST", "/register", url.Values{
		"name": {"Test"}, "email": {email},
		"password": {"password123"}, "confirmPassword": {"password123"},
	})
	if w.Code != http.StatusSeeOther {
		a.t.Fatalf("register status = %d, body: %s", w.Code, w.Body.String())
	}
	w = a.do("POST", "/login", url.Values{"email": {email}, "password": {"password123"}})
	if w.Code != http.StatusSeeOther {
		a.t.Fatalf("login status = %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			a.cookie = c
			return
		}
	}
	a.t.Fatal("no session cookie set")
}

// seed creates topic/deck/card via the store and returns the card.
func (a *testApp) seed(bidir bool) store.Card {
	a.t.Helper()
	ctx := context.Background()
	u, _, err := a.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		a.t.Fatal(err)
	}
	topic, err := a.store.CreateTopic(ctx, u.ID, store.TopicParams{Name: "Spanish"})
	if err != nil {
		a.t.Fatal(err)
	}
	deck, err := a.store.CreateDeck(ctx, u.ID, store.DeckParams{Name: "Vocab", TopicID: topic.ID, IsBidirectional: bidir})
	if err != nil {
		a.t.Fatal(err)
	}
	card, err := a.store.CreateCard(ctx, u.ID, store.CardParams{
		DeckID: deck.ID, Front: "hola", Back: "hello", Priority: sm2.PriorityA, Tags: []string{"greeting"},
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return card
}

// The sidebar version comes from the embedded VERSION file, so it renders with
// nothing set in the environment — newTestApp's Config never sets a version.
// This guards against reintroducing an env-sourced version, which used to leave
// the badge blank whenever APP_VERSION was unset.
func TestSidebarShowsEmbeddedVersion(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("GET", "/dashboard", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard = %d", w.Code)
	}
	if want := "v" + cadence.Version; !strings.Contains(w.Body.String(), want) {
		t.Errorf("dashboard body does not contain sidebar version %q", want)
	}
}

func TestAuthLifecycle(t *testing.T) {
	app := newTestApp(t, nil)

	// Unauthenticated -> redirect to login.
	if w := app.do("GET", "/dashboard", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("unauthenticated dashboard = %d -> %q", w.Code, w.Header().Get("Location"))
	}
	// HTMX unauthenticated -> HX-Redirect.
	req := httptest.NewRequest("GET", "/cards/table", nil)
	req.Header.Set("X-Real-IP", "10.0.0.9")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("htmx unauthenticated = %d, HX-Redirect %q", w.Code, w.Header().Get("HX-Redirect"))
	}

	app.login("t@example.com")
	if w := app.do("GET", "/dashboard", nil); w.Code != http.StatusOK {
		t.Errorf("dashboard after login = %d", w.Code)
	}

	// Logout kills the session.
	if w := app.do("POST", "/logout", url.Values{}); w.Code != http.StatusSeeOther {
		t.Errorf("logout = %d", w.Code)
	}
	if w := app.do("GET", "/dashboard", nil); w.Code != http.StatusSeeOther {
		t.Errorf("dashboard after logout = %d, want redirect", w.Code)
	}
}

func TestRegisterRejectsMismatchedConfirmation(t *testing.T) {
	app := newTestApp(t, nil)
	w := app.do("POST", "/register", url.Values{
		"name": {"Test"}, "email": {"t@example.com"},
		"password": {"password123"}, "confirmPassword": {"password124"},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Passwords do not match") {
		t.Fatalf("mismatched confirmation = %d, body %q", w.Code, w.Body.String())
	}
	// The account must not have been created.
	if _, _, err := app.store.GetUserByEmail(context.Background(), "t@example.com"); err == nil {
		t.Error("user was created despite the mismatch")
	}
}

func TestLoginLockoutAfterThreeFailures(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.cookie = nil

	for i := 0; i < 3; i++ {
		w := app.do("POST", "/login", url.Values{"email": {"t@example.com"}, "password": {"wrong"}})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d = %d", i+1, w.Code)
		}
	}
	// Correct password now blocked by the account lockout.
	w := app.do("POST", "/login", url.Values{"email": {"t@example.com"}, "password": {"password123"}})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Too many failed attempts") {
		t.Errorf("locked-out login = %d, body should mention lockout", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	app := newTestApp(t, nil)
	w := app.do("GET", "/login", nil)
	h := w.Header()
	if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing security headers: %v", h)
	}
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q, want strict self-only policy", csp)
	}
}

func TestOriginCheckRejectsCrossSite(t *testing.T) {
	app := newTestApp(t, nil)
	req := httptest.NewRequest("POST", "/login", strings.NewReader("email=a@b.c&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", "10.0.0.9")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	app.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", w.Code)
	}
}

func TestStudyLoop(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)
	sched := card.ForwardSchedule()

	// next -> study card fragment with the pending question stub.
	w := app.do("GET", "/study/1/next?deckIds=1&total=1&completed=0", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("next = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"hola", `hx-post="/study/1/question"`, "grade-area", `name="version" value="0"`,
		fmt.Sprintf(`href="/cards/%d"`, card.ID),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("next fragment missing %q", want)
		}
	}

	// Question fragment via the stub.
	w = app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "What does &#39;hola&#39; mean?") {
		t.Errorf("question = %d, body %q", w.Code, w.Body.String())
	}

	// Chat about the answer (conversationId 0 = opened lazily server-side).
	w = app.do("POST", "/study/1/chat", url.Values{
		"scheduleId": {itoa(sched.ID)}, "message": {"it means hello"}, "conversationId": {"0"},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Correct!") {
		t.Errorf("chat = %d", w.Code)
	}

	// Grade -> graded fragment, version bumped.
	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"CORRECT_PERFECT_RECALL"}, "version": {"0"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	// grade-on-green marks the chosen button; it replaced the old "selected"
	// class when the grading buttons became a solid-fill vertical stack.
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "grade-on-green") {
		t.Fatalf("grade = %d, body %q", w.Code, w.Body.String())
	}
	got, err := app.store.GetSchedule(context.Background(), 1, sched.ID)
	if err != nil || got.Version != 1 || got.RepCount != 1 {
		t.Errorf("post-grade schedule = %+v, %v", got, err)
	}

	// Change the grade: applied to the pre-grade state (the initial one here),
	// so easiness stays 2.5 rather than keeping the 2.6 the first grade set,
	// and the rep count does not advance a second time.
	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"CORRECT_WITH_HESITATION"}, "version": {"1"}, "regrade": {"1"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "grade-on-yellow") {
		t.Fatalf("regrade = %d, body %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `name="regrade" value="1"`) {
		t.Error("graded fragment should stay regradable")
	}
	got, err = app.store.GetSchedule(context.Background(), 1, sched.ID)
	if err != nil || got.Version != 2 || got.RepCount != 1 || got.Interval != 1 || got.Easiness < 2.49 || got.Easiness > 2.51 {
		t.Errorf("regraded schedule = %+v, %v", got, err)
	}

	// Stale regrade -> the same 409 conflict fragment as a stale grade.
	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"INCORRECT"}, "version": {"1"}, "regrade": {"1"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusConflict {
		t.Errorf("stale regrade = %d, want 409", w.Code)
	}

	// Stale grade -> 409 conflict fragment.
	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"INCORRECT"}, "version": {"0"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale grade = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "updated elsewhere") || !strings.Contains(w.Body.String(), `hx-swap-oob="beforeend:#chat-messages"`) {
		t.Errorf("conflict fragment wrong: %s", w.Body.String())
	}

	// Everything reviewed today -> next returns session complete.
	w = app.do("GET", "/study/1/next?deckIds=1&total=1&completed=1", nil)
	if !strings.Contains(w.Body.String(), "Study Session Complete!") {
		t.Errorf("expected session complete, got: %s", w.Body.String())
	}

	// Limit reached -> session complete even with cards due.
	if _, err := app.store.ResetProgress(context.Background(), 1, sched.ID); err != nil {
		t.Fatal(err)
	}
	// The reset dropped the pre-grade snapshot: a regrade now has nothing to
	// rewind to and gets the refetch-the-card conflict fragment.
	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"INCORRECT"}, "version": {"3"}, "regrade": {"1"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "updated elsewhere") {
		t.Errorf("regrade without snapshot = %d, body %q", w.Code, w.Body.String())
	}
	w = app.do("GET", "/study/1/next?deckIds=1&limit=2&total=2&completed=2", nil)
	if !strings.Contains(w.Body.String(), "Study Session Complete!") {
		t.Error("limit should end the session")
	}
}

func TestCardTableFilterFragment(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)
	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")
	deck, _ := app.store.GetDeck(ctx, u.ID, 1)
	app.store.CreateCard(ctx, u.ID, store.CardParams{DeckID: deck.ID, Front: "adiós", Back: "goodbye", Priority: sm2.PriorityB})

	w := app.do("GET", "/cards/table?priority=A", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("table = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "hola") || strings.Contains(body, "adiós") {
		t.Errorf("priority filter not applied: %s", body)
	}

	w = app.do("GET", "/cards/table?q=goodbye", nil)
	if !strings.Contains(w.Body.String(), "adiós") || strings.Contains(w.Body.String(), "hola") {
		t.Error("search filter not applied")
	}

	w = app.do("GET", "/cards/table?tag=greeting", nil)
	if !strings.Contains(w.Body.String(), "hola") || strings.Contains(w.Body.String(), "adiós") {
		t.Error("tag filter not applied")
	}
}

// The deck page's "New Card" button carries the deck through to the form, and
// the form pre-selects both selects from it — the topic only indirectly, via
// the deck it owns.
func TestDeckPageNewCardPrefillsTopicAndDeck(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)
	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")
	deck, _ := app.store.GetDeck(ctx, u.ID, 1)

	w := app.do("GET", "/decks/"+itoa(deck.ID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("deck page = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `href="/cards/new?deckId=`+itoa(deck.ID)+`"`) {
		t.Fatalf("deck page has no prefilled new-card link: %s", w.Body.String())
	}

	w = app.do("GET", "/cards/new?deckId="+itoa(deck.ID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("card form = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<option value="`+itoa(deck.TopicID)+`" selected>Spanish</option>`) {
		t.Errorf("topic not preselected: %s", body)
	}
	if !strings.Contains(body, `<option value="`+itoa(deck.ID)+`" selected>Vocab</option>`) {
		t.Errorf("deck not preselected: %s", body)
	}
}

func TestChatMessage(t *testing.T) {
	app := newTestApp(t, stubAI{reply: "¡Claro! **Hola** means hello."})
	app.login("t@example.com")
	app.seed(false)

	w := app.do("POST", "/chat/1/message", url.Values{"message": {"what does hola mean?"}, "conversationId": {"0"}})
	if w.Code != http.StatusOK {
		t.Fatalf("chat = %d", w.Code)
	}
	body := w.Body.String()
	// Markdown rendered server-side.
	if !strings.Contains(body, "<strong>Hola</strong>") {
		t.Errorf("markdown not rendered: %s", body)
	}
	// OOB update hands the browser the server-issued conversation ID — the
	// transcript itself stays server-side.
	if !strings.Contains(body, `id="chat-conversation"`) || !strings.Contains(body, "hx-swap-oob") {
		t.Error("missing OOB conversation-ID update")
	}
}

func TestCardUpdateVersionConflictPage(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seed(false)

	form := url.Values{
		"deckId": {itoa(card.DeckID)}, "front": {"hola!"}, "back": {"hello"},
		"priority": {"A"}, "tags": {""}, "version": {"0"},
	}
	if w := app.do("POST", "/cards/1", form); w.Code != http.StatusSeeOther {
		t.Fatalf("update = %d", w.Code)
	}
	// Replay with the stale version -> conflict banner page, still in edit
	// mode with the submitted values and the fresh version, so the user can
	// review and save again instead of retyping.
	w := app.do("POST", "/cards/1", form)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "modified by another request") {
		t.Errorf("stale update = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Edit Card") || !strings.Contains(body, "hola!") {
		t.Errorf("conflict page dropped edit mode or typed input: %.300s", body)
	}
	if !strings.Contains(body, `name="version" value="1"`) {
		t.Errorf("conflict page should carry the fresh version: %.300s", body)
	}
}

func TestImportEndpoint(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	yaml := "- Front: uno\n  Back: one\n  Priority: A\n- Back: broken\n  Priority: B\n"
	w := app.do("POST", "/import", url.Values{"deckId": {"1"}, "yamlContent": {yaml}})
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Imported 1 card") || !strings.Contains(body, "Card at index 1") {
		t.Errorf("import result wrong: %s", body)
	}
	if strings.Contains(body, "switched to bidirectional") {
		t.Errorf("no reverse params, deck should not flip: %s", body)
	}
}

// TestImportEndpointReverseParams covers YAML carrying both directions' SM-2
// state: the reverse schedule is stored and the deck is flipped bidirectional
// so it is actually studied.
func TestImportEndpointReverseParams(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	yaml := "- Front: ABCs\n  Back: abecedario\n  Priority: A\n  Tags: []\n" +
		"  LastSeen: 2026-06-23\n  Grade: CORRECT_PERFECT_RECALL\n  RepCount: 4\n  Easiness: 2.9\n  Interval: 49\n" +
		"  ReverseLastSeen: 2026-06-23\n  ReverseGrade: CORRECT_PERFECT_RECALL\n  ReverseRepCount: 4\n" +
		"  ReverseEasiness: 2.9\n  ReverseInterval: 49\n"
	w := app.do("POST", "/import", url.Values{"deckId": {"1"}, "yamlContent": {yaml}})
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Imported 1 card") || !strings.Contains(body, "switched to bidirectional") {
		t.Errorf("import result wrong: %s", body)
	}

	cards, _, err := app.store.ListCards(context.Background(), 1, store.CardListParams{})
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	var imported store.Card
	for _, c := range cards {
		if c.Front == "ABCs" {
			imported = c
		}
	}
	rev := imported.ReverseSchedule()
	if rev == nil || rev.RepCount != 4 || rev.Easiness != 2.9 || rev.Interval != 49 {
		t.Errorf("imported reverse schedule = %+v", rev)
	}
}

func TestHealthNoAuth(t *testing.T) {
	app := newTestApp(t, nil)
	w := app.do("GET", "/api/health", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("health = %d %s", w.Code, w.Body.String())
	}
}

func TestRegistrationGate(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Config{CookieSecure: false, EnablePublicRegistration: false, ClaudeModel: "test", ClaudeMaxTokens: 100}
	_, handler, err := New(cfg, st, stubAI{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/register", nil)
	req.Header.Set("X-Real-IP", "10.0.0.9")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("register with registration disabled = %d -> %q", w.Code, w.Header().Get("Location"))
	}
}

// Import failures must use 422: the htmx-config swaps only 2xx/3xx, 409 and
// 422, so any other error status renders no feedback at all.
func TestImportErrorRendersVisibleFragment(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	w := app.do("POST", "/import", url.Values{"deckId": {"1"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty import = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "required") {
		t.Errorf("import error fragment missing message: %s", w.Body.String())
	}
}

func TestRegisterRejectsOverlongPassword(t *testing.T) {
	app := newTestApp(t, nil)
	long := strings.Repeat("a", 73) // over bcrypt's 72-byte limit
	w := app.do("POST", "/register", url.Values{
		"name": {"T"}, "email": {"long@example.com"},
		"password": {long}, "confirmPassword": {long},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "at most 72") {
		t.Errorf("overlong password register = %d, want 400 with message; body: %.200s", w.Code, w.Body.String())
	}
}

func TestCardDeleteRejectsProtocolRelativeRedirect(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")
	for _, dest := range []string{"//evil.example/phish", `/\evil.example`, "https://evil.example"} {
		card, err := app.store.CreateCard(ctx, u.ID, store.CardParams{
			DeckID: 1, Front: "front " + dest, Back: "back", Priority: sm2.PriorityB,
		})
		if err != nil {
			t.Fatalf("CreateCard: %v", err)
		}
		w := app.do("POST", "/cards/"+itoa(card.ID)+"/delete", url.Values{"redirect": {dest}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/cards" {
			t.Errorf("redirect=%q -> %d %q, want fallback to /cards", dest, w.Code, w.Header().Get("Location"))
		}
	}
}

// The fragment endpoints tell the browser which URL reflects the rendered
// filter state, so refresh/back round-trip.
func TestCardsTableFragmentPushesFilterURL(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seed(false)

	w := app.do("GET", "/cards/table?priority=A", nil)
	if got := w.Header().Get("HX-Push-Url"); got != "/cards?priority=A" {
		t.Errorf("HX-Push-Url = %q, want /cards?priority=A", got)
	}

	w = app.do("GET", "/decks/grid?q=vo", nil)
	if got := w.Header().Get("HX-Push-Url"); got != "/decks?q=vo" {
		t.Errorf("deck grid HX-Push-Url = %q, want /decks?q=vo", got)
	}
}

// suggestionFixture is a full proposal, so a test can assert on any field.
var suggestionFixture = claude.TopicSuggestion{
	Name:        "Spanish",
	TopicDesc:   "Mexican Spanish",
	Expertise:   "language tutor",
	Focus:       "vocabulary and grammar",
	ContextType: "cultural context",
	Example:     "H: What does 'pelo' mean?\n\nA: Hair, anywhere on the body.",
	Question:    "Ask about the back of the card without revealing it.",
}

// The suggestion fills blanks and leaves typed values alone — the property that
// makes the button safe to press on a half-filled or already-saved topic.
func TestTopicSuggestFillsOnlyBlankFields(t *testing.T) {
	app := newTestApp(t, stubAI{suggestion: suggestionFixture})
	app.login("t@example.com")

	w := app.do("POST", "/topics/suggest", url.Values{
		"seed": {"Mexican Spanish, vocabulary and grammar"},
		// Already typed by the user; must survive.
		"name":  {"Español"},
		"focus": {"just the subjunctive"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("suggest = %d, want 200", w.Code)
	}
	body := w.Body.String()

	// Matched as rendered input values, not bare substrings: the field help
	// quotes phrases like "language tutor" as examples, so a plain Contains
	// would pass whether or not the input was actually filled.
	for _, keep := range []string{"Español", "just the subjunctive"} {
		if !strings.Contains(body, `value="`+keep+`"`) {
			t.Errorf("suggest overwrote a typed value: %q not in an input", keep)
		}
	}
	if strings.Contains(body, `value="Spanish"`) {
		t.Error("suggested name replaced the typed one")
	}
	for _, filled := range []string{"Mexican Spanish", "language tutor", "cultural context"} {
		if !strings.Contains(body, `value="`+filled+`"`) {
			t.Errorf("blank field not filled: %q not in an input", filled)
		}
	}
	if !strings.Contains(body, suggestionFixture.Question) {
		t.Error("suggested question prompt not rendered into its textarea")
	}
	// 5 of 7 were blank (name and focus were typed).
	if !strings.Contains(body, "5 fields filled in") {
		t.Errorf("notice missing or miscounted; body: %.400s", body)
	}
	// A suggestion always writes optional settings, so they must come back open
	// or the user cannot see what was proposed.
	if !strings.Contains(body, "<details class=\"disclosure\" open>") {
		t.Error("optional settings stayed collapsed after a suggestion")
	}
}

func TestTopicSuggestRequiresDescription(t *testing.T) {
	app := newTestApp(t, stubAI{suggestion: suggestionFixture})
	app.login("t@example.com")

	w := app.do("POST", "/topics/suggest", url.Values{"seed": {"   "}})
	if w.Code != http.StatusOK {
		t.Fatalf("empty seed = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "what you are studying") {
		t.Errorf("no prompt for a description; body: %.300s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "filled in") {
		t.Error("AI was called despite an empty description")
	}
}

// A suggestion is optional sugar: when Claude is unreachable the form must come
// back usable, with the failure as a bubble at 200 rather than an error page.
func TestTopicSuggestRendersAIFailureAsBubble(t *testing.T) {
	// A server with no key must say so, not "try again" — the distinction is
	// the difference between a fixable setup problem and a transient one.
	cases := []struct {
		err  error
		want string
	}{
		{claude.ErrBadAuth, "API key was rejected"},
		{claude.ErrNotConfigured, "no Claude API key"},
		{claude.ErrRateLimited, "rate-limited"},
	}
	for _, c := range cases {
		app := newTestApp(t, stubAI{err: c.err})
		app.login("t@example.com")

		w := app.do("POST", "/topics/suggest", url.Values{"seed": {"chess openings"}})
		if w.Code != http.StatusOK {
			t.Fatalf("%v = %d, want 200", c.err, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("%v: missing copy %q; body: %.300s", c.err, c.want, body)
		}
		if !strings.Contains(body, `name="name"`) {
			t.Errorf("%v: form fields not re-rendered after the failure", c.err)
		}
	}
}

// The disclosure is only worth hiding if it opens when it holds something: a
// blank new-topic form collapses, an existing topic's settings do not.
func TestTopicFormDisclosureOpensWhenItHasContent(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("GET", "/topics/new", nil)
	if strings.Contains(w.Body.String(), `class="disclosure" open`) {
		t.Error("blank new-topic form opened the optional settings")
	}
	// Placeholders must show the prompt builder's real fallbacks.
	for _, def := range []string{claude.PromptDefaults.Expertise, claude.PromptDefaults.ContextType} {
		if !strings.Contains(w.Body.String(), `placeholder="`+def+`"`) {
			t.Errorf("default %q not shown as a placeholder", def)
		}
	}

	ctx := context.Background()
	u, _, _ := app.store.GetUserByEmail(ctx, "t@example.com")
	expertise := "language tutor"
	topic, err := app.store.CreateTopic(ctx, u.ID, store.TopicParams{Name: "Spanish", Expertise: &expertise})
	if err != nil {
		t.Fatal(err)
	}

	w = app.do("GET", "/topics/"+itoa(topic.ID)+"/edit", nil)
	if !strings.Contains(w.Body.String(), `class="disclosure" open`) {
		t.Error("topic with settings did not open the optional settings")
	}
	if !strings.Contains(w.Body.String(), expertise) {
		t.Error("stored expertise not rendered into the edit form")
	}
}

// ---------- Markdown card content ----------

// seedMarkdownCard makes a card whose three fields all carry markdown, so the
// display tests can assert on each independently.
func (a *testApp) seedMarkdownCard() store.Card {
	a.t.Helper()
	ctx := context.Background()
	u, _, err := a.store.GetUserByEmail(ctx, "t@example.com")
	if err != nil {
		a.t.Fatal(err)
	}
	topic, err := a.store.CreateTopic(ctx, u.ID, store.TopicParams{Name: "Markdown"})
	if err != nil {
		a.t.Fatal(err)
	}
	deck, err := a.store.CreateDeck(ctx, u.ID, store.DeckParams{Name: "Syntax", TopicID: topic.ID})
	if err != nil {
		a.t.Fatal(err)
	}
	note := "a *noted* thing"
	card, err := a.store.CreateCard(ctx, u.ID, store.CardParams{
		DeckID: deck.ID, Front: "**bold** front", Back: "- listed back",
		Note: &note, Priority: sm2.PriorityB,
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return card
}

func TestCardShowRendersMarkdown(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seedMarkdownCard()

	w := app.do("GET", "/cards/"+itoa(card.ID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("card show = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<strong>bold</strong>", "<li>listed back</li>", "<em>noted</em>"} {
		if !strings.Contains(body, want) {
			t.Errorf("card detail missing %q", want)
		}
	}
	if strings.Contains(body, "**bold**") {
		t.Error("card detail still shows raw markdown source")
	}
}

// The edit form must hand back the source, not the rendering — otherwise the
// next save would persist HTML and destroy what the author wrote.
func TestCardEditFormKeepsMarkdownSource(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	card := app.seedMarkdownCard()

	w := app.do("GET", "/cards/"+itoa(card.ID)+"?edit=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("card edit = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "**bold** front") {
		t.Error("edit textarea lost the markdown source")
	}
	if strings.Contains(body, "<strong>bold</strong>") {
		t.Error("edit form rendered the markdown instead of showing its source")
	}
}

func TestCardTableStripsMarkdown(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seedMarkdownCard()

	w := app.do("GET", "/cards/table", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("card table = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "bold front") {
		t.Error("card table lost the front text")
	}
	if strings.Contains(body, "**bold**") || strings.Contains(body, "<strong>") {
		t.Error("card table should show neither raw markdown nor rendered HTML")
	}
}

func TestCardPreviewRendersAllThreeFields(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/cards/preview", url.Values{
		"front": {"**b**"}, "back": {"_i_"}, "note": {"`c`"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<strong>b</strong>", "<em>i</em>", "<code>c</code>", ">Front<", ">Back<", ">Note<"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q; body: %s", want, body)
		}
	}
}

func TestCardPreviewOmitsEmptyNote(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/cards/preview", url.Values{"front": {"f"}, "back": {"b"}})
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), ">Note<") {
		t.Error("preview rendered a Note section for a card with no note")
	}
}

// Every branch must answer 200: base.html configures htmx to swap only 2xx,
// 409 and 422, so any other status would silently leave a stale render.
func TestCardPreviewEmptyFormRendersNotice(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/cards/preview", url.Values{"front": {""}, "back": {" "}, "note": {""}})
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Nothing to preview yet") {
		t.Errorf("empty preview did not render the notice; body: %s", w.Body.String())
	}
}

// The endpoint echoes user input straight back as HTML, so it is the sharpest
// place to pin the "raw HTML never survives" boundary.
func TestCardPreviewNeverEmitsRawHTML(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")

	w := app.do("POST", "/cards/preview", url.Values{
		"front": {"<script>alert(1)</script>"},
		"back":  {"[x](javascript:alert(1))"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<script>") {
		t.Error("preview emitted a raw script tag")
	}
	if strings.Contains(strings.ToLower(body), "javascript:") {
		t.Error("preview emitted a javascript: destination")
	}
}

func TestCardPreviewRequiresAuth(t *testing.T) {
	app := newTestApp(t, nil)

	w := app.do("POST", "/cards/preview", url.Values{"front": {"**b**"}})
	if w.Code == http.StatusOK {
		t.Errorf("unauthenticated preview = %d, want a redirect", w.Code)
	}
	if strings.Contains(w.Body.String(), "<strong>") {
		t.Error("unauthenticated preview rendered content")
	}
}

// The study answer panel is the other place card content is read, and it goes
// through StudyItem.Prompt/Answer rather than Front/Back.
func TestStudyCardRendersMarkdown(t *testing.T) {
	app := newTestApp(t, nil)
	app.login("t@example.com")
	app.seedMarkdownCard()

	w := app.do("GET", "/study/1/next?deckIds=1&total=1&completed=0", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("next = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<strong>bold</strong>", "<li>listed back</li>", "<em>noted</em>"} {
		if !strings.Contains(body, want) {
			t.Errorf("study card missing %q", want)
		}
	}
	if strings.Contains(body, "**bold**") {
		t.Error("study card still shows raw markdown source")
	}
}
