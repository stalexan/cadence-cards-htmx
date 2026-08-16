package server

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/config"
	"cadence-cards/internal/ratelimit"
	"cadence-cards/internal/store"
	"cadence-cards/web"
)

// AI is the Claude surface the handlers use; stubbed in tests.
type AI interface {
	GenerateQuestion(ctx context.Context, cfg claude.TopicConfig, card claude.CardContent) (string, error)
	ChatAboutQuestion(ctx context.Context, cfg claude.TopicConfig, card claude.CardContent, userAnswer string, previous []claude.Message) (string, error)
	ChatAboutTopic(ctx context.Context, cfg claude.TopicConfig, message string, previous []claude.Message, isFirst bool) (string, error)
	SuggestTopicConfig(ctx context.Context, description string) (claude.TopicSuggestion, error)
}

// Server holds the application's HTTP surface.
type Server struct {
	cfg          config.Config
	store        *store.Store
	ai           AI
	limiter      *ratelimit.Limiter
	pages        map[string]*template.Template
	partials     *template.Template
	assetVersion string
}

// New builds the Server and its handler tree.
func New(cfg config.Config, st *store.Store, ai AI) (*Server, http.Handler, error) {
	pages, partials, err := buildTemplates()
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	av, err := assetVersion()
	if err != nil {
		return nil, nil, fmt.Errorf("asset version: %w", err)
	}
	s := &Server{
		cfg:          cfg,
		store:        st,
		ai:           ai,
		limiter:      ratelimit.New(cfg.DisableRateLimiting),
		pages:        pages,
		partials:     partials,
		assetVersion: av,
	}

	mux := http.NewServeMux()

	// Static assets (embedded).
	static := http.FileServerFS(web.Static)
	mux.Handle("GET /static/", cacheStatic(static))
	mux.Handle("GET /favicon.svg", cacheStatic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, web.Static, "static/favicon.svg")
	})))

	// Public.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /register", s.handleRegisterPage)
	mux.HandleFunc("POST /register", s.handleRegister)

	// Root redirect.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) != nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	// Authenticated pages.
	auth := s.requireUser
	mux.HandleFunc("POST /logout", auth(s.handleLogout))
	mux.HandleFunc("GET /dashboard", auth(s.handleDashboard))

	mux.HandleFunc("GET /topics", auth(s.handleTopicsList))
	mux.HandleFunc("GET /topics/new", auth(s.handleTopicNewPage))
	mux.HandleFunc("POST /topics", auth(s.handleTopicCreate))
	mux.HandleFunc("POST /topics/suggest", auth(s.handleTopicSuggest))
	mux.HandleFunc("GET /topics/{id}", auth(s.handleTopicShow))
	mux.HandleFunc("GET /topics/{id}/edit", auth(s.handleTopicEditPage))
	mux.HandleFunc("POST /topics/{id}", auth(s.handleTopicUpdate))
	mux.HandleFunc("POST /topics/{id}/delete", auth(s.handleTopicDelete))
	mux.HandleFunc("GET /topics/{id}/export", auth(s.handleTopicExport))
	mux.HandleFunc("GET /topics/{id}/export-preview", auth(s.handleTopicExportPreview))

	mux.HandleFunc("GET /decks", auth(s.handleDecksList))
	mux.HandleFunc("GET /decks/grid", auth(s.handleDecksGridFragment))
	mux.HandleFunc("GET /decks/new", auth(s.handleDeckNewPage))
	mux.HandleFunc("POST /decks", auth(s.handleDeckCreate))
	mux.HandleFunc("GET /decks/{id}", auth(s.handleDeckShow))
	mux.HandleFunc("GET /decks/{id}/edit", auth(s.handleDeckEditPage))
	mux.HandleFunc("POST /decks/{id}", auth(s.handleDeckUpdate))
	mux.HandleFunc("POST /decks/{id}/delete", auth(s.handleDeckDelete))
	mux.HandleFunc("GET /decks/{id}/export", auth(s.handleDeckExport))
	mux.HandleFunc("GET /decks/{id}/export-preview", auth(s.handleDeckExportPreview))
	mux.HandleFunc("GET /decks/{id}/cards", auth(s.handleDeckCardsFragment))

	mux.HandleFunc("GET /cards", auth(s.handleCardsList))
	mux.HandleFunc("GET /cards/table", auth(s.handleCardsTableFragment))
	mux.HandleFunc("GET /cards/deck-options", auth(s.handleCardDeckOptions))
	mux.HandleFunc("POST /cards/preview", auth(s.handleCardPreview))
	mux.HandleFunc("GET /cards/new", auth(s.handleCardNewPage))
	mux.HandleFunc("POST /cards", auth(s.handleCardCreate))
	mux.HandleFunc("GET /cards/{id}", auth(s.handleCardShow))
	mux.HandleFunc("POST /cards/{id}", auth(s.handleCardUpdate))
	mux.HandleFunc("POST /cards/{id}/delete", auth(s.handleCardDelete))
	mux.HandleFunc("POST /schedules/{id}/reset", auth(s.handleScheduleReset))

	mux.HandleFunc("GET /import", auth(s.handleImportPage))
	mux.HandleFunc("POST /import", auth(s.handleImport))
	mux.HandleFunc("POST /import/detect", auth(s.handleImportDetect))

	mux.HandleFunc("GET /profile", auth(s.handleProfilePage))
	mux.HandleFunc("POST /profile", auth(s.handleProfileUpdate))
	mux.HandleFunc("POST /profile/password", auth(s.handlePasswordChange))

	mux.HandleFunc("GET /chat", auth(s.handleChatIndex))
	mux.HandleFunc("GET /chat/{topicId}", auth(s.handleChatShow))
	mux.HandleFunc("POST /chat/{topicId}/message", auth(s.handleChatMessage))

	mux.HandleFunc("GET /study", auth(s.handleStudyIndex))
	mux.HandleFunc("GET /study/{topicId}/setup", auth(s.handleStudySetup))
	mux.HandleFunc("GET /study/{topicId}", auth(s.handleStudySession))
	mux.HandleFunc("GET /study/{topicId}/next", auth(s.handleStudyNext))
	mux.HandleFunc("POST /study/{topicId}/question", auth(s.handleStudyQuestion))
	mux.HandleFunc("POST /study/{topicId}/chat", auth(s.handleStudyChat))
	mux.HandleFunc("POST /study/schedules/{id}/grade", auth(s.handleStudyGrade))

	// Middleware chain, wrapped innermost first: a request enters through
	// logRequests (last line, outermost) and reaches the mux last. Order is
	// load-bearing — resolveClientIP must wrap rateLimit so the limiter keys on
	// the real X-Real-IP rather than the socket address.
	var h http.Handler = mux
	h = checkOrigin(h)
	h = s.loadSession(h)
	h = securityHeaders(h)
	h = s.rateLimit(h)
	h = resolveClientIP(h)
	h = logRequests(h)
	return s, h, nil
}

// Cleanup drops expired rate-limiter entries (called from the app's hourly
// maintenance ticker).
func (s *Server) Cleanup(now time.Time) {
	s.limiter.Cleanup(now)
}

// cacheStatic marks embedded assets long-lived (they change with the binary).
//
// A request carrying the ?v= fingerprint (see assets.go) can be cached forever,
// because changing the file changes the URL. Anything else — a bare
// /favicon.svg from the browser's default probe, a hand-typed asset URL — gets a
// short TTL instead, so an unversioned path can never pin a stale asset for a
// year.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		next.ServeHTTP(w, r)
	})
}
