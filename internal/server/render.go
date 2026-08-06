package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cadence-cards/internal/markdown"
	"cadence-cards/internal/store"
	"cadence-cards/web"
)

// funcMap holds helpers available to all templates.
var funcMap = template.FuncMap{
	// dict builds a map inline: {{template "x" (dict "A" 1 "B" 2)}}.
	"dict": func(pairs ...any) (map[string]any, error) {
		if len(pairs)%2 != 0 {
			return nil, fmt.Errorf("dict: odd argument count")
		}
		m := make(map[string]any, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			key, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
			}
			m[key] = pairs[i+1]
		}
		return m, nil
	},
	"formatDate": func(t time.Time) string {
		return t.In(time.Local).Format("Jan 2, 2006")
	},
	"formatDateTime": func(t time.Time) string {
		return t.In(time.Local).Format("Jan 2, 2006 3:04 PM")
	},
	"formatDatePtr": func(t *time.Time) string {
		if t == nil {
			return "Never"
		}
		return t.In(time.Local).Format("Jan 2, 2006")
	},
	// json serializes a value for hidden inputs (chat history) and hx-vals.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"markdown": markdown.Render,
	"add":      func(a, b int) int { return a + b },
	"sub":      func(a, b int) int { return a - b },
	"deref": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"joinTags":  func(tags []string) string { return strings.Join(tags, ", ") },
	"hasPrefix": strings.HasPrefix,
	"lower":     func(v any) string { return strings.ToLower(fmt.Sprint(v)) },
	"deref64": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"progressPercent": func(done, total int) int {
		if total <= 0 {
			return 0
		}
		return min(100, done*100/total)
	},
	"percent": func(part, whole int) int {
		if whole <= 0 {
			return 0
		}
		return part * 100 / whole
	},
	"avatarInitial": func(u *store.User) string {
		src := ""
		if u != nil {
			if u.Name != nil && *u.Name != "" {
				src = *u.Name
			} else if u.Email != nil {
				src = *u.Email
			}
		}
		if src == "" {
			return "?"
		}
		return strings.ToUpper(string([]rune(src)[0]))
	},
}

// buildTemplates parses one cloned set per page: layout + all partials + the
// page file. The map key is the page name (file base without .html).
func buildTemplates() (pages map[string]*template.Template, partials *template.Template, err error) {
	partialFiles, err := fs.Glob(web.Templates, "templates/partials/*.html")
	if err != nil {
		return nil, nil, err
	}

	// Partials-only set for HTMX fragment rendering.
	partials, err = template.New("partials").Funcs(funcMap).ParseFS(web.Templates, "templates/partials/*.html")
	if err != nil {
		return nil, nil, err
	}

	pageFiles, err := fs.Glob(web.Templates, "templates/pages/*.html")
	if err != nil {
		return nil, nil, err
	}

	pages = make(map[string]*template.Template, len(pageFiles))
	for _, pf := range pageFiles {
		name := strings.TrimSuffix(pf[strings.LastIndex(pf, "/")+1:], ".html")
		layout := "templates/layout/base.html"
		// login/register use the centered auth layout.
		if name == "login" || name == "register" {
			layout = "templates/layout/auth.html"
		}
		files := append([]string{layout}, partialFiles...)
		files = append(files, pf)
		t, err := template.New("base.html").Funcs(funcMap).ParseFS(web.Templates, files...)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", pf, err)
		}
		pages[name] = t
	}
	return pages, partials, nil
}

// pageData wraps every full-page render.
type pageData struct {
	User       *store.User
	Path       string
	AppVersion string
	// AssetVersion fingerprints web/static so the layouts can bust the cache
	// on /static URLs. Distinct from AppVersion, which is informational and
	// empty unless APP_VERSION is set.
	AssetVersion string
	Data         any
}

// render writes a full page using its layout.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("unknown page template %q", page))
		return
	}
	pd := pageData{
		User:         userFrom(r),
		Path:         r.URL.Path,
		AppVersion:   s.cfg.AppVersion,
		AssetVersion: s.assetVersion,
		Data:         data,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "base", pd); err != nil {
		slog.Error("template execute failed", "page", page, "error", err)
	}
}

// fragment writes a named partial for an HTMX swap.
func (s *Server) fragment(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.partials.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("fragment execute failed", "fragment", name, "error", err)
	}
}

// serverError logs and renders a plain 500.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error", "path", r.URL.Path, "error", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

// storeError maps typed store errors onto HTTP responses for page handlers.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrVersionConflict):
		http.Error(w, "Modified by another request. Please refresh and try again.", http.StatusConflict)
	default:
		s.serverError(w, r, err)
	}
}
