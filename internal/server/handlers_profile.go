package server

import (
	"errors"
	"net/http"
	"net/mail"

	"golang.org/x/crypto/bcrypt"

	"cadence-cards/internal/store"
)

// profileData feeds profile.html.
type profileData struct {
	ProfileError    string
	ProfileSaved    bool
	PasswordError   string
	PasswordChanged bool
}

func (s *Server) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "profile", profileData{
		ProfileSaved:    r.URL.Query().Get("saved") == "true",
		PasswordChanged: r.URL.Query().Get("passwordChanged") == "true",
	})
}

func (s *Server) handleProfileUpdate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "name")
	email := formStr(r, "email")

	fail := func(msg string) {
		s.render(w, r, http.StatusBadRequest, "profile", profileData{ProfileError: msg})
	}
	if name == "" || email == "" {
		fail("Name and email are required.")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		fail("Please enter a valid email address.")
		return
	}
	if _, err := s.store.UpdateUserProfile(r.Context(), userFrom(r).ID, name, email); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			fail("An account with this email already exists.")
			return
		}
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/profile?saved=true", http.StatusSeeOther)
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r)
	current := r.FormValue("currentPassword")
	next := r.FormValue("newPassword")
	confirm := r.FormValue("confirmPassword")

	fail := func(msg string) {
		s.render(w, r, http.StatusBadRequest, "profile", profileData{PasswordError: msg})
	}
	if len(next) < 8 {
		fail("New password must be at least 8 characters.")
		return
	}
	if len(next) > bcryptMaxBytes {
		fail("New password must be at most 72 characters.")
		return
	}
	if next != confirm {
		fail("New passwords do not match.")
		return
	}
	hash, err := s.store.GetPasswordHash(r.Context(), user.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		fail("Current password is incorrect.")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcryptCost)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.UpdatePassword(r.Context(), user.ID, string(newHash)); err != nil {
		s.serverError(w, r, err)
		return
	}
	// Invalidate every other session for this user.
	if err := s.store.DeleteOtherSessions(r.Context(), user.ID, sessionToken(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/profile?passwordChanged=true", http.StatusSeeOther)
}
