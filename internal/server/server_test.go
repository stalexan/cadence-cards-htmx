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
	w := a.do("POST", "/register", url.Values{"name": {"Test"}, "email": {email}, "password": {"password123"}})
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
	for _, want := range []string{"hola", `hx-post="/study/1/question"`, "grade-area", `name="version" value="0"`} {
		if !strings.Contains(body, want) {
			t.Errorf("next fragment missing %q", want)
		}
	}

	// Question fragment via the stub.
	w = app.do("POST", "/study/1/question", url.Values{"scheduleId": {itoa(sched.ID)}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "What does &#39;hola&#39; mean?") {
		t.Errorf("question = %d, body %q", w.Code, w.Body.String())
	}

	// Chat about the answer.
	w = app.do("POST", "/study/1/chat", url.Values{
		"scheduleId": {itoa(sched.ID)}, "message": {"it means hello"}, "history": {"[]"},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Correct!") {
		t.Errorf("chat = %d", w.Code)
	}

	// Grade -> graded fragment, version bumped.
	w = app.do("POST", fmt.Sprintf("/study/schedules/%d/grade", sched.ID), url.Values{
		"topicId": {"1"}, "grade": {"CORRECT_PERFECT_RECALL"}, "version": {"0"},
		"deckIds": {"1"}, "total": {"1"}, "completed": {"0"},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "selected") {
		t.Fatalf("grade = %d", w.Code)
	}
	got, err := app.store.GetSchedule(context.Background(), 1, sched.ID)
	if err != nil || got.Version != 1 || got.RepCount != 1 {
		t.Errorf("post-grade schedule = %+v, %v", got, err)
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
	if !strings.Contains(w.Body.String(), "Session complete") {
		t.Errorf("expected session complete, got: %s", w.Body.String())
	}

	// Limit reached -> session complete even with cards due.
	if _, err := app.store.ResetProgress(context.Background(), 1, sched.ID); err != nil {
		t.Fatal(err)
	}
	w = app.do("GET", "/study/1/next?deckIds=1&limit=2&total=2&completed=2", nil)
	if !strings.Contains(w.Body.String(), "Session complete") {
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

	w := app.do("POST", "/chat/1/message", url.Values{"message": {"what does hola mean?"}, "history": {"[]"}})
	if w.Code != http.StatusOK {
		t.Fatalf("chat = %d", w.Code)
	}
	body := w.Body.String()
	// Markdown rendered server-side.
	if !strings.Contains(body, "<strong>Hola</strong>") {
		t.Errorf("markdown not rendered: %s", body)
	}
	// OOB history update carries the transcript.
	if !strings.Contains(body, `id="chat-history"`) || !strings.Contains(body, "hx-swap-oob") {
		t.Error("missing OOB history update")
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
	// Replay with the stale version -> conflict banner page.
	w := app.do("POST", "/cards/1", form)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "modified by another request") {
		t.Errorf("stale update = %d", w.Code)
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
