package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cadence-cards/internal/store"
)

// bcryptCost matches password.ts (12 rounds).
const bcryptCost = 12

// bcryptMaxBytes is bcrypt's hard input limit; GenerateFromPassword errors on
// anything longer, so the handlers must reject it as a validation failure.
const bcryptMaxBytes = 72

// dummyHash (cost 12, matching real hashes) is compared against on the
// unknown-email login path so its response time matches the known-email path;
// otherwise the ~250ms bcrypt gap confirms which addresses have accounts.
const dummyHash = "$2a$12$qxcZsqF6pdR1AC1VWzi5yuyudKDqye7z.rn04iz3Fu871JIEEVMNi"

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// loginData feeds the login page template.
type loginData struct {
	Error      string
	Registered bool
	Email      string
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if userFrom(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", loginData{
		Registered: r.URL.Query().Get("registered") == "true",
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ip := clientIP(r)
	email := formStr(r, "email")
	password := r.FormValue("password")

	fail := func(msg string) {
		s.render(w, r, http.StatusUnauthorized, "login", loginData{Error: msg, Email: email})
	}

	// Lockout checks before touching the database (port of auth.ts authorize).
	if s.limiter.IsIPLockedOut(ip, now) || s.limiter.CheckAuthAttemptRateLimit(ip, now) {
		slog.Warn("login blocked: IP locked out", "ip", ip)
		fail("Too many failed attempts. Please try again later.")
		return
	}
	if email == "" || password == "" {
		fail("Email and password are required.")
		return
	}
	if s.limiter.IsAccountLockedOut(email, now) {
		slog.Warn("login blocked: account locked out", "email", email, "ip", ip)
		fail("Too many failed attempts. Please try again later.")
		return
	}

	user, hash, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Burn the same bcrypt work as a real comparison (see dummyHash).
			bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
			s.limiter.RecordFailedAuth(ip, email, now)
			slog.Warn("login failed: unknown email", "email", email, "ip", ip)
			fail("Invalid email or password.")
			return
		}
		s.serverError(w, r, err)
		return
	}
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		s.limiter.RecordFailedAuth(ip, email, now)
		slog.Warn("login failed: bad password", "email", email, "ip", ip)
		fail("Invalid email or password.")
		return
	}

	s.limiter.ClearFailedAttempts(email)
	token, err := s.store.CreateSession(r.Context(), user.ID, now)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setSessionCookie(w, token)
	slog.Info("login succeeded", "userId", user.ID, "ip", ip)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		if err := s.store.DeleteSession(r.Context(), token); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// registerData feeds the register page template.
type registerData struct {
	Error string
	Name  string
	Email string
}

func (s *Server) registrationEnabled(w http.ResponseWriter, r *http.Request) bool {
	if !s.cfg.EnablePublicRegistration {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return false
	}
	return true
}

func (s *Server) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	if !s.registrationEnabled(w, r) {
		return
	}
	s.render(w, r, http.StatusOK, "register", registerData{})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registrationEnabled(w, r) {
		return
	}
	name := formStr(r, "name")
	email := formStr(r, "email")
	password := r.FormValue("password")
	confirm := r.FormValue("confirmPassword")

	fail := func(msg string) {
		s.render(w, r, http.StatusBadRequest, "register", registerData{Error: msg, Name: name, Email: email})
	}
	if name == "" || email == "" {
		fail("Name and email are required.")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		fail("Please enter a valid email address.")
		return
	}
	if len(password) < 8 {
		fail("Password must be at least 8 characters.")
		return
	}
	if len(password) > bcryptMaxBytes {
		fail("Password must be at most 72 characters.")
		return
	}
	if password != confirm {
		fail("Passwords do not match.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if _, err := s.store.CreateUser(r.Context(), name, email, string(hash)); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			fail("An account with this email already exists.")
			return
		}
		s.serverError(w, r, err)
		return
	}
	slog.Info("user registered", "email", email, "ip", clientIP(r))
	http.Redirect(w, r, "/login?registered=true", http.StatusSeeOther)
}
