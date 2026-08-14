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
	question string
	reply    string
	err      error
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
