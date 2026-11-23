package store

import (
	"context"
	"database/sql"
	"errors"
)

// HasUsers reports whether an admin account exists.
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "select exists (select 1 from users)").Scan(&exists)
	return exists, err
}

// CreateFirstUser creates the admin account. It returns false and creates
// nothing when an account already exists.
func (s *Store) CreateFirstUser(ctx context.Context, username, passwordHash string, nowMs int64) (bool, error) {
	var created bool
	err := s.write(ctx, func(tx *transaction) error {
		res, err := tx.ExecContext(ctx, `
			insert into users (username, password_hash, created_at_ms)
			select ?, ?, ? where not exists (select 1 from users)`, username, passwordHash, nowMs)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		created = n == 1
		return err
	})
	return created && err == nil, err
}

// UserPasswordHash returns the id and password hash for a username, or ErrNotFound.
func (s *Store) UserPasswordHash(ctx context.Context, username string) (int64, string, error) {
	var id int64
	var hash string
	err := s.db.QueryRowContext(ctx, "select id, password_hash from users where username = ?", username).
		Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	return id, hash, err
}

// CreateSession stores a session and drops the ones that have expired.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID, nowMs, expiresAtMs int64) error {
	return s.write(ctx, func(tx *transaction) error {
		if _, err := tx.ExecContext(ctx, "delete from sessions where expires_at_ms <= ?", nowMs); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"insert into sessions (token_hash, user_id, expires_at_ms) values (?, ?, ?)",
			tokenHash, userID, expiresAtMs)
		return err
	})
}

// SessionUser returns the user id of a live session, or ErrNotFound.
func (s *Store) SessionUser(ctx context.Context, tokenHash string, nowMs int64) (int64, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx,
		"select user_id from sessions where token_hash = ? and expires_at_ms > ?", tokenHash, nowMs).
		Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return userID, err
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "delete from sessions where token_hash = ?", tokenHash)
	return err
}
