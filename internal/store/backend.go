package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"
	_ "turso.tech/database/tursogo"
)

// Backend selects the engine that stores an Uppies install.
type Backend string

const (
	SQLite      Backend = "sqlite"
	Postgres    Backend = "postgres"
	MariaDB     Backend = "mariadb"
	TursoSQLite Backend = "turso-sqlite"
	TursoPG     Backend = "tursopg"
)

// OpenBackend opens a database in the selected engine. The embedded engines
// use path. PostgreSQL and TursoPG use a PostgreSQL connection string, and
// MariaDB uses a Go MySQL driver DSN.
func OpenBackend(ctx context.Context, backend Backend, path, dsn string) (*Store, error) {
	if backend == "" || backend == SQLite {
		return Open(ctx, path)
	}
	db, err := openConnection(backend, path, dsn)
	if err != nil {
		return nil, err
	}
	s := newStore(db, backend)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("connect to %s: %w", backend, err), db.Close())
	}
	if err := s.migrateBackend(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate %s: %w", backend, err), db.Close())
	}
	return s, nil
}

func openConnection(backend Backend, path, dsn string) (*sql.DB, error) {
	switch backend {
	case TursoSQLite:
		if path == "" || strings.Contains(path, "?") {
			return nil, errors.New("turso-sqlite requires a database path without a question mark")
		}
		db, err := sql.Open("turso", path)
		if err != nil {
			return nil, err
		}
		connector := tursoConnector{driver: db.Driver(), path: path}
		if err := db.Close(); err != nil {
			return nil, err
		}
		db = sql.OpenDB(connector)
		db.SetMaxOpenConns(1)
		return db, nil
	case Postgres, TursoPG:
		if dsn == "" {
			return nil, fmt.Errorf("%s requires UPPIES_DATABASE_URL", backend)
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, fmt.Errorf("invalid %s connection string in UPPIES_DATABASE_URL", backend)
		}
		if backend == TursoPG {
			// TursoPG supports extended text binds without a Describe round trip.
			cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
		}
		db := stdlib.OpenDB(*cfg)
		if backend == TursoPG {
			// A single session avoids snapshot write skew in TursoPG.
			db.SetMaxOpenConns(1)
		}
		return db, nil
	case MariaDB:
		if dsn == "" {
			return nil, errors.New("mariadb requires UPPIES_DATABASE_URL")
		}
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			return nil, errors.New("invalid mariadb DSN in UPPIES_DATABASE_URL")
		}
		// Store methods use affected rows to distinguish missing rows from
		// idempotent updates of existing rows.
		cfg.ClientFoundRows = true
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, errors.New("invalid mariadb connector configuration")
		}
		return sql.OpenDB(connector), nil
	default:
		return nil, fmt.Errorf("unknown database backend %q (use sqlite, postgres, mariadb, turso-sqlite, or tursopg)", backend)
	}
}

// Enable foreign keys on each Turso SQLite connection, including after a reconnect.
type tursoConnector struct {
	driver driver.Driver
	path   string
}

func (c tursoConnector) Driver() driver.Driver { return c.driver }

func (c tursoConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	exec := conn.(driver.ExecerContext)
	if _, err := exec.ExecContext(ctx, "pragma foreign_keys = on", nil); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}

type queryRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queries struct {
	runner  queryRunner
	backend Backend
}

type transaction struct{ queries }

func (q queries) ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	statement, args = q.bind(statement, args)
	return q.runner.ExecContext(ctx, statement, args...)
}

func (q queries) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	statement, args = q.bind(statement, args)
	return q.runner.QueryContext(ctx, statement, args...)
}

func (q queries) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	statement, args = q.bind(statement, args)
	return q.runner.QueryRowContext(ctx, statement, args...)
}

// bind translates Uppies' positional parameters for PostgreSQL. Store SQL
// uses question marks only for parameters. Flags are stored as 0 or 1.
func (q queries) bind(statement string, args []any) (string, []any) {
	if q.backend != Postgres && q.backend != TursoPG {
		return statement, args
	}
	var bound strings.Builder
	for i := 1; ; i++ {
		before, after, found := strings.Cut(statement, "?")
		bound.WriteString(before)
		if !found {
			break
		}
		bound.WriteByte('$')
		bound.WriteString(strconv.Itoa(i))
		statement = after
	}
	args = slices.Clone(args)
	for i, arg := range args {
		if flag, ok := arg.(bool); ok {
			if flag {
				args[i] = int64(1)
			} else {
				args[i] = int64(0)
			}
		}
	}
	return bound.String(), args
}

func isUnique(err error) bool {
	if e, ok := errors.AsType[*sqlite.Error](err); ok {
		return e.Code() == 2067 // SQLITE_CONSTRAINT_UNIQUE
	}
	if e, ok := errors.AsType[*pgconn.PgError](err); ok {
		return e.Code == "23505" || (e.Code == "23000" && strings.Contains(e.Message, "UNIQUE constraint failed"))
	}
	if e, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return e.Number == 1062
	}
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isForeignKey(err error) bool {
	if e, ok := errors.AsType[*sqlite.Error](err); ok {
		return e.Code() == 787 // SQLITE_CONSTRAINT_FOREIGNKEY
	}
	if e, ok := errors.AsType[*pgconn.PgError](err); ok {
		return e.Code == "23503"
	}
	if e, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return e.Number == 1452
	}
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
