package store

import (
	"context"
	"database/sql"
	"errors"
)

// APIToken is credential metadata safe for the admin UI. The digest is only
// used to look up a token during authentication.
type APIToken struct {
	ID              int64
	Name            string
	Owner           string
	CreatedAtMs     int64
	ExpiresAtMs     int64
	RevokedAtMs     int64
	PermissionCount int
}

const apiTokenMetadata = `
	select t.id, t.name, u.username, t.created_at_ms, t.expires_at_ms, coalesce(t.revoked_at_ms, 0),
		(select count(*) from api_token_permissions p where p.token_id = t.id)
	from api_tokens t join users u on u.id = t.user_id`

func scanAPIToken(row rowScanner) (APIToken, error) {
	var t APIToken
	err := row.Scan(&t.ID, &t.Name, &t.Owner, &t.CreatedAtMs, &t.ExpiresAtMs, &t.RevokedAtMs, &t.PermissionCount)
	return t, err
}

func (s *Store) APITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, apiTokenMetadata+" order by t.id desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []APIToken
	for rows.Next() {
		token, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) APIToken(ctx context.Context, id int64) (APIToken, []string, error) {
	token, err := scanAPIToken(s.db.QueryRowContext(ctx, apiTokenMetadata+" where t.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, nil, ErrNotFound
	}
	if err != nil {
		return APIToken{}, nil, err
	}
	rows, err := s.db.QueryContext(ctx, "select permission from api_token_permissions where token_id = ? order by permission", id)
	if err != nil {
		return APIToken{}, nil, err
	}
	defer rows.Close()
	var permissions []string
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return APIToken{}, nil, err
		}
		permissions = append(permissions, permission)
	}
	return token, permissions, rows.Err()
}

func (s *Store) CreateAPIToken(
	ctx context.Context, userID int64, name, tokenHash string, nowMs, expiresAtMs int64, permissions []string,
) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			insert into api_tokens (user_id, name, token_hash, created_at_ms, expires_at_ms)
			values (?, ?, ?, ?, ?)`, userID, name, tokenHash, nowMs, expiresAtMs)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return insertAPITokenPermissions(ctx, tx, id, permissions)
	})
}

func insertAPITokenPermissions(ctx context.Context, tx *sql.Tx, id int64, permissions []string) error {
	for _, permission := range permissions {
		if _, err := tx.ExecContext(ctx,
			"insert into api_token_permissions (token_id, permission) values (?, ?)", id, permission); err != nil {
			return err
		}
	}
	return nil
}

// SetAPITokenPermissions replaces a live token's grants as a single change.
func (s *Store) SetAPITokenPermissions(ctx context.Context, id, nowMs int64, permissions []string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var active bool
		err := tx.QueryRowContext(ctx, `
			select exists (select 1 from api_tokens where id = ? and revoked_at_ms is null and expires_at_ms > ?)`,
			id, nowMs).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "delete from api_token_permissions where token_id = ?", id); err != nil {
			return err
		}
		return insertAPITokenPermissions(ctx, tx, id, permissions)
	})
}

func (s *Store) RevokeAPIToken(ctx context.Context, id, nowMs int64) error {
	res, err := s.db.ExecContext(ctx, "update api_tokens set revoked_at_ms = ? where id = ? and revoked_at_ms is null", nowMs, id)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// APITokenAllows checks a live credential and one exact permission in the same
// read. It returns ErrNotFound for unknown, expired or revoked credentials.
func (s *Store) APITokenAllows(ctx context.Context, tokenHash, permission string, nowMs int64) (bool, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from api_token_permissions p where p.token_id = t.id and p.permission = ?)
		from api_tokens t where t.token_hash = ? and t.expires_at_ms > ? and t.revoked_at_ms is null`,
		permission, tokenHash, nowMs).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return allowed, err
}
