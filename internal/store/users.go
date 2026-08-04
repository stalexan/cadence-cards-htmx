package store

import (
	"context"
	"database/sql"
	"errors"
)

// CreateUser inserts a user with a bcrypt password hash. Returns ErrDuplicate
// if the email is taken.
func (s *Store) CreateUser(ctx context.Context, name, email, passwordHash string) (User, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (name, email, password_hash) VALUES (?, ?, ?)`,
		name, email, passwordHash)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrDuplicate
		}
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, id)
}

func scanUser(row *sql.Row) (User, string, error) {
	var u User
	var name, email, hash sql.NullString
	var created, updated string
	if err := row.Scan(&u.ID, &name, &email, &hash, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, "", ErrNotFound
		}
		return User{}, "", err
	}
	u.Name, u.Email = nullStr(name), nullStr(email)
	var err error
	if u.CreatedAt, err = parseTime(created); err != nil {
		return User{}, "", err
	}
	if u.UpdatedAt, err = parseTime(updated); err != nil {
		return User{}, "", err
	}
	return u, hash.String, nil
}

// GetUserByID fetches a user by ID.
func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	u, _, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, name, email, password_hash, created_at, updated_at FROM users WHERE id = ?`, id))
	return u, err
}

// GetUserByEmail fetches a user and their password hash by email (login path).
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, string, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, name, email, password_hash, created_at, updated_at FROM users WHERE email = ?`, email))
}

// UpdateUserProfile updates name and/or email. Returns ErrDuplicate if the
// email is taken by another user.
func (s *Store) UpdateUserProfile(ctx context.Context, userID int64, name, email string) (User, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET name = ?, email = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		name, email, userID)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrDuplicate
		}
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrNotFound
	}
	return s.GetUserByID(ctx, userID)
}

// GetPasswordHash returns the stored bcrypt hash for a user.
func (s *Store) GetPasswordHash(ctx context.Context, userID int64) (string, error) {
	var hash sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash.String, err
}

// UpdatePassword sets a new bcrypt hash for a user.
func (s *Store) UpdatePassword(ctx context.Context, userID int64, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		passwordHash, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
