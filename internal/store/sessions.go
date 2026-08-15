package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// SessionDuration matches the source app's 30-day Auth.js session maxAge.
const SessionDuration = 30 * 24 * time.Hour

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession mints a session for userID and returns the raw cookie token.
// Only the SHA-256 of the token is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64, now time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, fmtTime(now.Add(SessionDuration)))
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetSessionUser resolves a raw cookie token to its user, if the session is
// valid and unexpired.
func (s *Store) GetSessionUser(ctx context.Context, token string, now time.Time) (User, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), fmtTime(now)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, userID)
}

// DeleteSession removes one session (logout).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// DeleteOtherSessions removes all of a user's sessions except the current one
// (password change).
func (s *Store) DeleteOtherSessions(ctx context.Context, userID int64, keepToken string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, hashToken(keepToken))
	return err
}

// DeleteUserSessions removes every session a user has (CLI password reset,
// which has no session of its own to keep).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// DeleteExpiredSessions clears expired sessions (hourly ticker).
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, fmtTime(now))
	return err
}
