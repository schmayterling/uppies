package store

import (
	"context"
	"database/sql"
	"errors"

	"modernc.org/sqlite"

	"uppies/internal/health"
)

// Project groups monitors and incidents and has one public status page.
type Project struct {
	ID   int64
	Slug string
	Name string

	// How the public status page looks.
	Tagline     string
	HomepageURL string
	AccentHue   int    // 0 to 360, the hue of the accent colour
	Theme       string // "auto", "light" or "dark"
	ShowFace    bool
	HistoryDays int // 30, 60 or 90
	CustomCSS   string
	FooterText  string
	// ShowPoweredBy keeps the "powered by uppies" line in the footer.
	ShowPoweredBy bool
	// NoIndex asks search engines not to list the page.
	NoIndex  bool
	Timezone string // IANA name, such as "Asia/Manila"
	// Domain is a hostname that serves this status page at its root, empty for none.
	Domain string
}

const projectColumns = `id, slug, name, tagline, homepage_url, accent_hue, theme, show_face, history_days,
	custom_css, footer_text, show_powered_by, noindex, timezone, domain`

func scanProject(row rowScanner) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Tagline, &p.HomepageURL, &p.AccentHue, &p.Theme,
		&p.ShowFace, &p.HistoryDays, &p.CustomCSS, &p.FooterText, &p.ShowPoweredBy, &p.NoIndex, &p.Timezone, &p.Domain)
	return p, err
}

// Monitor is one thing uppies checks, with its settings and current state.
type Monitor struct {
	ID             int64
	ProjectID      int64
	Name           string
	Kind           string // "http", "tcp", "dns", "ping" or "push"
	Target         string // a URL for http, host:port for tcp, a hostname for dns and ping
	Keyword        string // http only: text the response body must contain, empty for none
	IntervalS      int
	TimeoutMs      int
	Retries        int
	RetryIntervalS int
	RecoveryChecks int
	Paused         bool
	Public         bool // shown on the status page

	Tracker       health.Tracker
	StatusSinceMs int64
	LastCheckAtMs int64
	LastLatencyMs int64
	LastError     string

	// PushToken is the secret in a push monitor's URL, empty for other kinds.
	PushToken    string
	LastPushAtMs int64
	// CertExpiresAtMs is when the certificate seen by the last HTTPS check expires, 0 if none.
	CertExpiresAtMs int64
	// CertWarnedDays is the last warning threshold already alerted on, 0 for none.
	CertWarnedDays int
}

const monitorColumns = `id, project_id, name, kind, target, keyword, interval_s, timeout_ms, retries,
	retry_interval_s, recovery_checks, paused, public, status, status_since_ms, fail_streak, ok_streak,
	failing_since_ms, last_check_at_ms, last_latency_ms, last_error, push_token, last_push_at_ms,
	cert_expires_at_ms, cert_warned_days`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMonitor(row rowScanner) (Monitor, error) {
	var m Monitor
	err := row.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Kind, &m.Target, &m.Keyword, &m.IntervalS, &m.TimeoutMs,
		&m.Retries, &m.RetryIntervalS, &m.RecoveryChecks, &m.Paused, &m.Public, &m.Tracker.Status, &m.StatusSinceMs,
		&m.Tracker.FailStreak, &m.Tracker.OKStreak, &m.Tracker.FailingSinceMs, &m.LastCheckAtMs,
		&m.LastLatencyMs, &m.LastError, &m.PushToken, &m.LastPushAtMs, &m.CertExpiresAtMs, &m.CertWarnedDays)
	return m, err
}

// ErrSlugTaken is returned when a project slug is already in use.
var ErrSlugTaken = errors.New("slug already in use")

// sqliteConstraintUnique is SQLite's extended result code SQLITE_CONSTRAINT_UNIQUE.
const sqliteConstraintUnique = 2067

// CreateProject adds a project and returns its id, or ErrSlugTaken.
func (s *Store) CreateProject(ctx context.Context, slug, name string, nowMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"insert into projects (slug, name, created_at_ms) values (?, ?, ?)", slug, name, nowMs)
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintUnique {
		return 0, ErrSlugTaken
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Projects lists all projects by name.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, "select "+projectColumns+" from projects order by name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ProjectBySlug returns the project with that slug, or ErrNotFound.
func (s *Store) ProjectBySlug(ctx context.Context, slug string) (Project, error) {
	return s.project(ctx, "select "+projectColumns+" from projects where slug = ?", slug)
}

// ProjectByID returns the project with that id, or ErrNotFound.
func (s *Store) ProjectByID(ctx context.Context, id int64) (Project, error) {
	return s.project(ctx, "select "+projectColumns+" from projects where id = ?", id)
}

func (s *Store) project(ctx context.Context, query string, arg any) (Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// ErrDomainTaken is returned when another project already uses a domain.
var ErrDomainTaken = errors.New("domain already in use")

// UpdateProject changes a project's name and status page settings. The slug
// stays. It returns ErrDomainTaken when the domain belongs to another project.
func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	res, err := s.db.ExecContext(ctx, `
		update projects set name = ?, tagline = ?, homepage_url = ?, accent_hue = ?, theme = ?,
			show_face = ?, history_days = ?, custom_css = ?, footer_text = ?, show_powered_by = ?,
			noindex = ?, timezone = ?, domain = ?
		where id = ?`,
		p.Name, p.Tagline, p.HomepageURL, p.AccentHue, p.Theme, p.ShowFace, p.HistoryDays, p.CustomCSS,
		p.FooterText, p.ShowPoweredBy, p.NoIndex, p.Timezone, p.Domain, p.ID)
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintUnique {
		return ErrDomainTaken
	}
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// ProjectByDomain returns the project served on a hostname, or ErrNotFound.
func (s *Store) ProjectByDomain(ctx context.Context, domain string) (Project, error) {
	if domain == "" {
		return Project{}, ErrNotFound
	}
	return s.project(ctx, "select "+projectColumns+" from projects where domain = ?", domain)
}

// DeleteProject removes a project with its monitors, history and incidents.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "delete from projects where id = ?", id)
	return err
}

// CreateMonitor adds a monitor in the unknown state and returns its id.
func (s *Store) CreateMonitor(ctx context.Context, m Monitor, nowMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		insert into monitors (project_id, name, kind, target, keyword, interval_s, timeout_ms, retries,
			retry_interval_s, recovery_checks, paused, public, push_token, status_since_ms, created_at_ms)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProjectID, m.Name, m.Kind, m.Target, m.Keyword, m.IntervalS, m.TimeoutMs, m.Retries,
		m.RetryIntervalS, m.RecoveryChecks, m.Paused, m.Public, m.PushToken, nowMs, nowMs)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMonitor changes a monitor's settings. Its state and history are kept.
func (s *Store) UpdateMonitor(ctx context.Context, m Monitor) error {
	res, err := s.db.ExecContext(ctx, `
		update monitors set name = ?, kind = ?, target = ?, keyword = ?, interval_s = ?, timeout_ms = ?,
			retries = ?, retry_interval_s = ?, recovery_checks = ?, paused = ?, public = ?
		where id = ?`,
		m.Name, m.Kind, m.Target, m.Keyword, m.IntervalS, m.TimeoutMs, m.Retries, m.RetryIntervalS,
		m.RecoveryChecks, m.Paused, m.Public, m.ID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// DeleteMonitor removes a monitor and its history.
func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "delete from monitors where id = ?", id)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Monitor returns the monitor with that id, or ErrNotFound.
func (s *Store) Monitor(ctx context.Context, id int64) (Monitor, error) {
	row := s.db.QueryRowContext(ctx, "select "+monitorColumns+" from monitors where id = ?", id)
	m, err := scanMonitor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Monitor{}, ErrNotFound
	}
	return m, err
}

// Monitors lists every monitor, ordered by project and name.
func (s *Store) Monitors(ctx context.Context) ([]Monitor, error) {
	return s.monitors(ctx, "select "+monitorColumns+" from monitors order by project_id, name, id")
}

// MonitorsByProject lists one project's monitors by name.
func (s *Store) MonitorsByProject(ctx context.Context, projectID int64) ([]Monitor, error) {
	return s.monitors(ctx, "select "+monitorColumns+" from monitors where project_id = ? order by name, id", projectID)
}

func (s *Store) monitors(ctx context.Context, query string, args ...any) ([]Monitor, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var monitors []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		monitors = append(monitors, m)
	}
	return monitors, rows.Err()
}

// RecordPush notes that the job behind a push monitor reported in. It returns
// ErrNotFound when no monitor has that token.
func (s *Store) RecordPush(ctx context.Context, token string, nowMs int64) error {
	if token == "" {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, "update monitors set last_push_at_ms = ? where push_token = ?", nowMs, token)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// LastPushAtMs returns when a push monitor last reported in, 0 for never.
func (s *Store) LastPushAtMs(ctx context.Context, monitorID int64) (int64, error) {
	var atMs int64
	err := s.db.QueryRowContext(ctx, "select last_push_at_ms from monitors where id = ?", monitorID).Scan(&atMs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return atMs, err
}

// SetCertWarnedDays remembers the certificate warning threshold already alerted on.
func (s *Store) SetCertWarnedDays(ctx context.Context, monitorID int64, days int) error {
	_, err := s.db.ExecContext(ctx, "update monitors set cert_warned_days = ? where id = ?", days, monitorID)
	return err
}
