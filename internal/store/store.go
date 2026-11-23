// Package store keeps all uppies data in one SQLite file.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row asked for by id or slug does not exist.
var ErrNotFound = errors.New("not found")

// Store is the database handle. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// migrations run in order. The index of a step plus one is the schema version
// stored in PRAGMA user_version. Never edit a step that has shipped: add one.
var migrations = []string{
	`
create table projects (
	id integer primary key,
	slug text not null unique check (length(slug) between 1 and 64),
	name text not null check (length(name) between 1 and 120),
	created_at_ms integer not null
) strict;

create table monitors (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	name text not null check (length(name) between 1 and 120),
	kind text not null check (kind in ('http', 'tcp')),
	target text not null check (length(target) between 1 and 2048),
	keyword text not null default '',
	interval_s integer not null check (interval_s between 20 and 86400),
	timeout_ms integer not null check (timeout_ms between 100 and 60000),
	retries integer not null check (retries between 0 and 10),
	retry_interval_s integer not null check (retry_interval_s between 1 and 3600),
	recovery_checks integer not null check (recovery_checks between 1 and 10),
	paused integer not null default 0 check (paused in (0, 1)),
	status text not null default 'unknown' check (status in ('unknown', 'up', 'down')),
	status_since_ms integer not null,
	fail_streak integer not null default 0,
	ok_streak integer not null default 0,
	failing_since_ms integer not null default 0,
	last_check_at_ms integer not null default 0,
	last_latency_ms integer not null default 0,
	last_error text not null default '',
	created_at_ms integer not null
) strict;
create index monitors_project on monitors(project_id);

create table checks (
	id integer primary key,
	monitor_id integer not null references monitors(id) on delete cascade,
	at_ms integer not null,
	ok integer not null check (ok in (0, 1)),
	latency_ms integer not null,
	error text not null default '',
	location text not null default 'local'
) strict;
create index checks_monitor_at on checks(monitor_id, at_ms);
create index checks_at on checks(at_ms);

create table daily_rollups (
	monitor_id integer not null references monitors(id) on delete cascade,
	day_start_ms integer not null,
	checks integer not null,
	failures integer not null,
	covered_ms integer not null,
	down_ms integer not null,
	latency_sum_ms integer not null,
	latency_max_ms integer not null,
	primary key (monitor_id, day_start_ms)
) strict, without rowid;

create table incidents (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	title text not null check (length(title) between 1 and 200),
	impact text not null check (impact in ('minor', 'major', 'critical')),
	status text not null check (status in ('investigating', 'identified', 'monitoring', 'resolved')),
	published integer not null default 0 check (published in (0, 1)),
	started_at_ms integer not null,
	resolved_at_ms integer,
	created_at_ms integer not null
) strict;
create index incidents_project_started on incidents(project_id, started_at_ms);

create table incident_updates (
	id integer primary key,
	incident_id integer not null references incidents(id) on delete cascade,
	status text not null check (status in ('investigating', 'identified', 'monitoring', 'resolved')),
	body text not null check (length(body) between 1 and 5000),
	at_ms integer not null
) strict;
create index incident_updates_incident on incident_updates(incident_id, at_ms);

create table incident_monitors (
	incident_id integer not null references incidents(id) on delete cascade,
	monitor_id integer not null references monitors(id) on delete cascade,
	primary key (incident_id, monitor_id)
) strict, without rowid;
create index incident_monitors_monitor on incident_monitors(monitor_id);

create table events (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	kind text not null check (kind in ('monitor_down', 'monitor_up', 'incident_published', 'incident_update')),
	at_ms integer not null,
	monitor_id integer references monitors(id) on delete set null,
	incident_id integer references incidents(id) on delete set null,
	title text not null,
	detail text not null default ''
) strict;
create index events_project on events(project_id, id);
create index events_monitor on events(monitor_id);
create index events_incident on events(incident_id);

create table users (
	id integer primary key,
	username text not null unique check (length(username) between 1 and 64),
	password_hash text not null,
	created_at_ms integer not null
) strict;

create table sessions (
	token_hash text primary key,
	user_id integer not null references users(id) on delete cascade,
	expires_at_ms integer not null
) strict, without rowid;
create index sessions_user on sessions(user_id);
`,
	`
alter table projects add column tagline text not null default '' check (length(tagline) <= 200);
alter table projects add column homepage_url text not null default '' check (length(homepage_url) <= 2048);
alter table projects add column accent_hue integer not null default 200 check (accent_hue between 0 and 360);
alter table projects add column theme text not null default 'auto' check (theme in ('auto', 'light', 'dark'));
alter table projects add column show_face integer not null default 1 check (show_face in (0, 1));
alter table projects add column history_days integer not null default 90 check (history_days in (30, 60, 90));
alter table projects add column custom_css text not null default '' check (length(custom_css) <= 20000);

alter table monitors add column public integer not null default 1 check (public in (0, 1));

create table alert_channels (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	kind text not null check (kind in ('webhook', 'discord', 'slack')),
	url text not null check (length(url) between 1 and 2048),
	created_at_ms integer not null
) strict;
create index alert_channels_project on alert_channels(project_id);
`,
	`
alter table projects add column footer_text text not null default '' check (length(footer_text) <= 500);
alter table projects add column show_powered_by integer not null default 1 check (show_powered_by in (0, 1));
alter table projects add column noindex integer not null default 0 check (noindex in (0, 1));
alter table projects add column timezone text not null default 'UTC' check (length(timezone) between 1 and 64);
alter table projects add column domain text not null default '' check (length(domain) <= 253);
create unique index projects_domain on projects(domain) where domain <> '';
`,
	`
create table project_images (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	kind text not null check (kind in ('logo', 'favicon')),
	content_type text not null,
	data blob not null check (length(data) between 1 and 524288),
	updated_at_ms integer not null,
	unique (project_id, kind)
) strict;
`,
	// Step 5 rebuilds two tables, because SQLite cannot change a CHECK
	// constraint in place: monitors gain three kinds and four columns, and
	// alert channels gain the email kind.
	`
create table monitors_new (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	name text not null check (length(name) between 1 and 120),
	kind text not null check (kind in ('http', 'tcp', 'dns', 'ping', 'push')),
	target text not null check (length(target) between 1 and 2048),
	keyword text not null default '',
	interval_s integer not null check (interval_s between 20 and 86400),
	timeout_ms integer not null check (timeout_ms between 100 and 60000),
	retries integer not null check (retries between 0 and 10),
	retry_interval_s integer not null check (retry_interval_s between 1 and 3600),
	recovery_checks integer not null check (recovery_checks between 1 and 10),
	paused integer not null default 0 check (paused in (0, 1)),
	status text not null default 'unknown' check (status in ('unknown', 'up', 'down')),
	status_since_ms integer not null,
	fail_streak integer not null default 0,
	ok_streak integer not null default 0,
	failing_since_ms integer not null default 0,
	last_check_at_ms integer not null default 0,
	last_latency_ms integer not null default 0,
	last_error text not null default '',
	created_at_ms integer not null,
	public integer not null default 1 check (public in (0, 1)),
	push_token text not null default '',
	last_push_at_ms integer not null default 0,
	cert_expires_at_ms integer not null default 0,
	cert_warned_days integer not null default 0
) strict;
insert into monitors_new (id, project_id, name, kind, target, keyword, interval_s, timeout_ms, retries,
	retry_interval_s, recovery_checks, paused, status, status_since_ms, fail_streak, ok_streak,
	failing_since_ms, last_check_at_ms, last_latency_ms, last_error, created_at_ms, public)
select id, project_id, name, kind, target, keyword, interval_s, timeout_ms, retries,
	retry_interval_s, recovery_checks, paused, status, status_since_ms, fail_streak, ok_streak,
	failing_since_ms, last_check_at_ms, last_latency_ms, last_error, created_at_ms, public
from monitors;
drop table monitors;
alter table monitors_new rename to monitors;
create index monitors_project on monitors(project_id);
create unique index monitors_push_token on monitors(push_token) where push_token <> '';

create table alert_channels_new (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	kind text not null check (kind in ('webhook', 'discord', 'slack', 'email')),
	url text not null check (length(url) between 1 and 2048),
	created_at_ms integer not null
) strict;
insert into alert_channels_new select id, project_id, kind, url, created_at_ms from alert_channels;
drop table alert_channels;
alter table alert_channels_new rename to alert_channels;
create index alert_channels_project on alert_channels(project_id);

create table maintenance_windows (
	id integer primary key,
	project_id integer not null references projects(id) on delete cascade,
	title text not null check (length(title) between 1 and 200),
	starts_at_ms integer not null,
	ends_at_ms integer not null check (ends_at_ms > starts_at_ms),
	created_at_ms integer not null
) strict;
create index maintenance_windows_project on maintenance_windows(project_id, ends_at_ms);
`,
	`
alter table monitors add column display text not null default 'bars' check (display in ('bars', 'latency', 'summary', 'minimal'));
alter table projects add column surface text not null default 'neutral' check (surface in ('neutral', 'paper'));
alter table projects add column corners text not null default 'soft' check (corners in ('sharp', 'soft', 'round'));
alter table projects add column font text not null default 'system' check (font in ('system', 'rounded', 'serif', 'mono'));
`,
	`
alter table monitors add column category text not null default '' check (length(category) <= 80);
`,
	`
create table instance_settings (
	key text primary key,
	value text not null
) strict, without rowid;
`,
	// Step 9 frees the length of history from the three values the old
	// history_days column allows. That column stays behind unused, because
	// changing its CHECK would mean rebuilding the projects table.
	`
alter table projects add column lookback_days integer not null default 90 check (lookback_days between 7 and 365);
update projects set lookback_days = history_days;
alter table projects add column backfill text not null default 'none' check (backfill in ('none', 'up', 'degraded', 'down'));
`,
	`
alter table projects add column nav_links text not null default '' check (length(nav_links) <= 2000);
`,
	// Step 11 holds the images of the whole install. The first one is the
	// background of the sign-in page.
	`
create table instance_images (
	kind text primary key check (kind in ('login_background')),
	content_type text not null,
	data blob not null check (length(data) between 1 and 2097152),
	updated_at_ms integer not null
) strict;
`,
	`
alter table projects add column hide_quiet_days integer not null default 0 check (hide_quiet_days in (0, 1));
`,
}

// Open opens the database file at path, creating and migrating it as needed.
func Open(ctx context.Context, path string) (*Store, error) {
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	// Write transactions take the lock up front, so a read followed by a write
	// cannot fail with "database is locked" halfway through.
	query.Set("_txlock", "immediate")
	dsn := "file:" + url.PathEscape(path) + "?" + query.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx, path); err != nil {
		closeErr := db.Close()
		return nil, errors.Join(fmt.Errorf("migrate %s: %w", path, err), closeErr)
	}
	return s, nil
}

// Close releases the database file.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context, path string) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "pragma user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d)", version, len(migrations))
	}
	if version == len(migrations) {
		return nil
	}
	if version > 0 {
		if err := s.backup(ctx, fmt.Sprintf("%s.backup-v%d", path, version)); err != nil {
			return fmt.Errorf("back up before upgrading: %w", err)
		}
	}

	// Steps that rebuild a table need foreign keys switched off, and that
	// setting cannot change inside a transaction. So every step runs on one
	// connection with them off, and checks them itself before it commits.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "pragma foreign_keys = off"); err != nil {
		return errors.Join(err, conn.Close())
	}
	for step := version; step < len(migrations) && err == nil; step++ {
		if err = applyMigration(ctx, conn, step); err != nil {
			err = fmt.Errorf("schema step %d: %w", step+1, err)
		}
	}
	_, restoreErr := conn.ExecContext(ctx, "pragma foreign_keys = on")
	return errors.Join(err, restoreErr, conn.Close())
}

// backup writes a copy of the database next to it, unless that copy already
// exists from an earlier attempt at the same upgrade.
func (s *Store) backup(ctx context.Context, target string) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "vacuum into ?", target)
	return err
}

func applyMigration(ctx context.Context, conn *sql.Conn, step int) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := runMigration(ctx, tx, step); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func runMigration(ctx context.Context, tx *sql.Tx, step int) error {
	if _, err := tx.ExecContext(ctx, migrations[step]); err != nil {
		return err
	}
	broken, err := tx.QueryContext(ctx, "pragma foreign_key_check")
	if err != nil {
		return err
	}
	hasBroken := broken.Next()
	if err := errors.Join(broken.Err(), broken.Close()); err != nil {
		return err
	}
	if hasBroken {
		return errors.New("the step left rows that point at missing rows")
	}
	// PRAGMA takes no bound parameters. The value is a loop counter, not input.
	_, err = tx.ExecContext(ctx, fmt.Sprintf("pragma user_version = %d", step+1))
	return err
}

// write runs fn in one transaction and commits when fn returns nil.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
