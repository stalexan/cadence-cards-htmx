package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"cadence-cards/internal/store"
)

const sessionCookie = "cadence_session"

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxClientIP
)

// userFrom returns the logged-in user attached by loadSession, or nil.
func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

// clientIP returns the resolved client IP.
func clientIP(r *http.Request) string {
	ip, _ := r.Context().Value(ctxClientIP).(string)
	return ip
}

// setSessionCookie writes the session cookie (30-day expiry).
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(store.SessionDuration / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the session cookie.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// loadSession attaches the user to the request context when a valid session
// cookie is present. It never rejects — RequireUser does that.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			u, err := s.store.GetSessionUser(r.Context(), c.Value, time.Now())
			if err == nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxUser, &u))
			} else if !errors.Is(err, store.ErrNotFound) {
				s.serverError(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireUser gates authenticated routes: browsers are redirected to /login,
// HTMX requests get HX-Redirect so the whole page navigates.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) == nil {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// sessionToken returns the raw cookie token for the current request.
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}
