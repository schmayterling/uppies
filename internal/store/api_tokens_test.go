package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPITokenMigrationPreservesAnExistingUserAndSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "uppies.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := old.Close(); err != nil {
			t.Error(err)
		}
	}()
	setup := strings.Join(migrations[:18], "\n") + `
		pragma user_version = 18;
		insert into users (id, username, password_hash, created_at_ms) values (7, 'existing-admin', 'fixture', 1);
		insert into sessions (token_hash, user_id, expires_at_ms) values ('old-session-digest', 7, 3000);`
	if _, err := old.ExecContext(ctx, setup); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := os.Stat(path + ".backup-v18"); err != nil {
		t.Fatalf("version 18 backup: %v", err)
	}
	if id, err := s.SessionUser(ctx, "old-session-digest", 2999); id != 7 || err != nil {
		t.Fatalf("existing session after migration: user=%d err=%v, want 7", id, err)
	}
	if id, password, err := s.UserPasswordHash(ctx, "existing-admin"); id != 7 || password != "fixture" || err != nil {
		t.Fatalf("existing account after migration: id=%d err=%v", id, err)
	}
	if tokens, err := s.APITokens(ctx); len(tokens) != 0 || err != nil {
		t.Fatalf("migrated tokens: count=%d err=%v, want 0", len(tokens), err)
	}
}

func TestAPITokenGrantsExpireAtTheDeadlineAndFollowTheOwner(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.CreateUser(ctx, "owner", "fixture", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, "other", "fixture", 1); err != nil {
		t.Fatal(err)
	}
	userID, _, err := s.UserPasswordHash(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}

	raw := "synthetic token fixture"
	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])
	const permission = "monitors:read"
	if err := s.CreateAPIToken(ctx, userID, "build", hash, 1000, 2000, []string{permission}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.APITokenAllows(ctx, hash, permission, 1999); !ok || err != nil {
		t.Fatalf("live token with a grant: allowed=%v err=%v", ok, err)
	}
	if ok, err := s.APITokenAllows(ctx, hash, "monitors:update", 1999); ok || err != nil {
		t.Fatalf("live token without this grant: allowed=%v err=%v", ok, err)
	}
	if _, err := s.APITokenAllows(ctx, raw, permission, 1999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup with raw token: err=%v, want ErrNotFound", err)
	}
	if _, err := s.APITokenAllows(ctx, hash, permission, 2000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup at deadline: err=%v, want ErrNotFound", err)
	}
	if err := s.DeleteUser(ctx, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.APITokenAllows(ctx, hash, permission, 1999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted owner: err=%v, want ErrNotFound", err)
	}
}
