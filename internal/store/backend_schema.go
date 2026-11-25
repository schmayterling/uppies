package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
)

//go:embed schema/*.sql
var backendSchemas embed.FS

const backendSchemaVersion = 28

const schemaTable = `create table if not exists uppies_schema (
	id integer primary key check (id = 1),
	version integer not null
)`

func (s *Store) migrateBackend(ctx context.Context) error {
	if s.Backend() == TursoSQLite {
		var version int
		if err := s.db.QueryRowContext(ctx, "pragma user_version").Scan(&version); err != nil {
			return err
		}
		if version != 0 {
			return errors.New("turso-sqlite requires a separate database file from sqlite")
		}
	}
	if _, err := s.db.ExecContext(ctx, schemaTable); err != nil {
		return err
	}

	initialize := "insert into uppies_schema (id, version) values (1, 0) on conflict (id) do nothing"
	if s.Backend() == MariaDB {
		initialize = "insert into uppies_schema (id, version) values (1, 0) on duplicate key update id = 1"
	}
	if _, err := s.db.ExecContext(ctx, initialize); err != nil {
		return err
	}

	if s.Backend() == MariaDB {
		// MariaDB commits DDL implicitly. Schema statements are idempotent so
		// startup can resume a partially installed schema or upgrade.
		if err := s.applyBackendSchema(ctx, s.db); err != nil {
			return err
		}
	} else if err := s.write(ctx, func(tx *transaction) error {
		return s.applyBackendSchema(ctx, tx.queries)
	}); err != nil {
		return err
	}
	s.schema = backendSchemaVersion
	if s.Backend() == TursoPG {
		return s.verifyTursoPGForeignKeys(ctx)
	}
	return nil
}

func (s *Store) applyBackendSchema(ctx context.Context, q queries) error {
	var version int
	if err := q.QueryRowContext(ctx, "select version from uppies_schema where id = 1").Scan(&version); err != nil {
		return err
	}
	dialect := map[Backend]string{
		Postgres: "postgres", TursoPG: "postgres",
		MariaDB: "mariadb", TursoSQLite: "turso",
	}[s.Backend()]
	upgradeDialect := dialect
	if s.Backend() == TursoPG {
		upgradeDialect = "tursopg"
	}
	for version != backendSchemaVersion {
		var filename string
		target := backendSchemaVersion
		switch {
		case version == 0:
			filename = "schema/" + dialect + ".sql"
		case version >= 22 && version < backendSchemaVersion:
			target = version + 1
			filename = fmt.Sprintf("schema/v%d_%s.sql", target, upgradeDialect)
		default:
			return fmt.Errorf("schema version %d is unsupported for %s by this build (expected %d)", version, s.Backend(), backendSchemaVersion)
		}
		if err := applySchemaFile(ctx, q, filename); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "update uppies_schema set version = ? where id = 1", target); err != nil {
			return err
		}
		version = target
	}
	return nil
}

func applySchemaFile(ctx context.Context, q queries, filename string) error {
	body, err := backendSchemas.ReadFile(filename)
	if err != nil {
		return err
	}
	for i, statement := range strings.Split(strings.TrimSpace(string(body)), "\n\n") {
		if _, err := q.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%s statement %d: %w", filename, i+1, err)
		}
	}
	return nil
}

func (s *Store) verifyTursoPGForeignKeys(ctx context.Context) error {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Uppies only allocates positive IDs. The write is always rolled back.
	_, err = tx.ExecContext(ctx, "insert into page_monitors (project_id, monitor_id, position) values (0, 0, 0)")
	rollback := tx.Rollback()
	if isForeignKey(err) && rollback == nil {
		return nil
	}
	return errors.Join(errors.New("tursopg must enforce foreign keys; use the Uppies Compose server build"), err, rollback)
}
