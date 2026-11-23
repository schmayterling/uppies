package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"

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
	HistoryDays int // 7 to 365, how far back the status page looks
	// Backfill says how days before a monitor had any data are drawn:
	// "none", "up", "degraded" or "down".
	Backfill string
	// NavLinks are the links of the status page's top bar, one per line as "label | url".
	NavLinks    string
	NavbarBrand string // "icon", "icon-name" or "name"
	NavbarLink  string // "status", "list" or "homepage"
	CustomCSS   string
	FooterText  string
	// Announcement is a plain text notice at the top of this status page.
	Announcement      string
	AnnouncementColor string
	// ShowPoweredBy keeps the "powered by uppies" line in the footer.
	ShowPoweredBy bool
	// NoIndex asks search engines not to list the page.
	NoIndex  bool
	Timezone string // IANA name, such as "Asia/Manila"
	// The easy look settings. Each picks one of a few named choices.
	Surface string // "neutral" or "paper"
	Corners string // "sharp", "soft" or "round"
	Font    string // "system", "rounded", "serif" or "mono"
	// Domain is a hostname that serves this status page at its root, empty for none.
	Domain string
	// HideQuietDays leaves the days without incidents out of the page's timelines.
	HideQuietDays bool
	// GraphFill is how this page's graphs are filled: one of GraphFills, or
	// empty to follow the instance setting.
	GraphFill string
}

// GraphFills are the ways the area under a graph's line can be drawn:
// fading out towards the bottom, in one flat tint, or not at all.
var GraphFills = []string{"gradient", "filled", "none"}

const projectColumns = `id, slug, name, tagline, homepage_url, accent_hue, theme, show_face, lookback_days,
	custom_css, footer_text, show_powered_by, noindex, timezone, domain, surface, corners, font, backfill,
	nav_links, hide_quiet_days, graph_fill, navbar_brand, navbar_link, announcement, announcement_color`

func scanProject(row rowScanner) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Tagline, &p.HomepageURL, &p.AccentHue, &p.Theme,
		&p.ShowFace, &p.HistoryDays, &p.CustomCSS, &p.FooterText, &p.ShowPoweredBy, &p.NoIndex, &p.Timezone, &p.Domain,
		&p.Surface, &p.Corners, &p.Font, &p.Backfill, &p.NavLinks, &p.HideQuietDays, &p.GraphFill, &p.NavbarBrand, &p.NavbarLink,
		&p.Announcement, &p.AnnouncementColor)
	return p, err
}

// Incident automation modes control the response to confirmed monitor transitions.
const (
	IncidentAutomationOff = "off"
	IncidentCreateDraft   = "draft"
	IncidentCreatePublish = "publish"
	IncidentResolveDrafts = "drafts"
	IncidentResolveAll    = "all"
)

var (
	IncidentCreateModes  = []string{IncidentAutomationOff, IncidentCreateDraft, IncidentCreatePublish}
	IncidentResolveModes = []string{IncidentAutomationOff, IncidentResolveDrafts, IncidentResolveAll}
)

// Monitor is one thing uppies checks, with its settings and current state.
type Monitor struct {
	ID             int64
	Name           string
	Kind           string // "http", "tcp", "dns", "ping" or "push"
	Target         string // a URL for http, host:port for tcp, a hostname for dns and ping
	Keyword        string // http only: text the response body must contain, empty for none
	Tags           string // comma-separated labels for the admin
	IntervalS      int
	TimeoutMs      int
	Retries        int
	RetryIntervalS int
	RecoveryChecks int
	Paused         bool

	AutoCreateIncident  string
	AutoResolveIncident string

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

const monitorColumns = `id, name, kind, target, keyword, interval_s, timeout_ms, retries,
	retry_interval_s, recovery_checks, paused, status, status_since_ms, fail_streak, ok_streak,
	failing_since_ms, last_check_at_ms, last_latency_ms, last_error, push_token, last_push_at_ms,
	cert_expires_at_ms, cert_warned_days, tags, auto_create_incident, auto_resolve_incident`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMonitor(row rowScanner) (Monitor, error) {
	var m Monitor
	err := row.Scan(&m.ID, &m.Name, &m.Kind, &m.Target, &m.Keyword, &m.IntervalS, &m.TimeoutMs,
		&m.Retries, &m.RetryIntervalS, &m.RecoveryChecks, &m.Paused, &m.Tracker.Status, &m.StatusSinceMs,
		&m.Tracker.FailStreak, &m.Tracker.OKStreak, &m.Tracker.FailingSinceMs, &m.LastCheckAtMs,
		&m.LastLatencyMs, &m.LastError, &m.PushToken, &m.LastPushAtMs, &m.CertExpiresAtMs, &m.CertWarnedDays, &m.Tags,
		&m.AutoCreateIncident, &m.AutoResolveIncident)
	return m, err
}

// ErrSlugTaken is returned when a project slug is already in use.
var ErrSlugTaken = errors.New("slug already in use")

// CreateProject adds a project and returns its id, or ErrSlugTaken.
func (s *Store) CreateProject(ctx context.Context, slug, name string, nowMs int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		insert into projects (slug, name, created_at_ms, position)
		values (?, ?, ?, (select coalesce(max(position), 0) + 1 from projects))
		returning id`, slug, name, nowMs).Scan(&id)
	if isUnique(err) {
		return 0, ErrSlugTaken
	}
	return id, err
}

// Projects lists all projects in the order the admin gave them.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	return s.projects(ctx, "select "+projectColumns+" from projects order by position, id")
}

// ProjectsAfter lists the next projects in ID order.
func (s *Store) ProjectsAfter(ctx context.Context, after int64, limit int) ([]Project, error) {
	return s.projects(ctx, "select "+projectColumns+" from projects where id > ? order by id limit ?", after, limit)
}

func (s *Store) projects(ctx context.Context, query string, args ...any) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	return updateProject(ctx, s.db.ExecContext, p)
}

// EditProject applies changes to the current settings in one transaction.
func (s *Store) EditProject(ctx context.Context, id int64, edit func(*Project) error) error {
	return s.write(ctx, func(tx *transaction) error {
		p, err := scanProject(tx.QueryRowContext(ctx, "select "+projectColumns+" from projects where id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := edit(&p); err != nil {
			return err
		}
		return updateProject(ctx, tx.ExecContext, p)
	})
}

type execSQL func(context.Context, string, ...any) (sql.Result, error)

func updateProject(ctx context.Context, exec execSQL, p Project) error {
	// Callers that do not set a look get the default one.
	p.Surface = cmp.Or(p.Surface, "neutral")
	p.Corners = cmp.Or(p.Corners, "soft")
	p.Font = cmp.Or(p.Font, "system")
	p.Backfill = cmp.Or(p.Backfill, "none")
	p.NavbarBrand = cmp.Or(p.NavbarBrand, "icon-name")
	p.NavbarLink = cmp.Or(p.NavbarLink, "status")
	res, err := exec(ctx, `
		update projects set name = ?, tagline = ?, homepage_url = ?, accent_hue = ?, theme = ?,
			show_face = ?, lookback_days = ?, custom_css = ?, footer_text = ?, show_powered_by = ?,
			noindex = ?, timezone = ?, domain = ?, surface = ?, corners = ?, font = ?, backfill = ?,
			nav_links = ?, hide_quiet_days = ?, graph_fill = ?, navbar_brand = ?, navbar_link = ?,
			announcement = ?, announcement_color = ?
		where id = ?`,
		p.Name, p.Tagline, p.HomepageURL, p.AccentHue, p.Theme, p.ShowFace, p.HistoryDays, p.CustomCSS,
		p.FooterText, p.ShowPoweredBy, p.NoIndex, p.Timezone, p.Domain, p.Surface, p.Corners, p.Font, p.Backfill,
		p.NavLinks, p.HideQuietDays, p.GraphFill, p.NavbarBrand, p.NavbarLink, p.Announcement, p.AnnouncementColor, p.ID)
	if isUnique(err) {
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

// DeleteProject removes a project and its page-specific data.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "delete from projects where id = ?", id)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// CreateMonitor adds a monitor in the unknown state and returns its id.
func (s *Store) CreateMonitor(ctx context.Context, m Monitor, nowMs int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		insert into monitors (name, kind, target, keyword, interval_s, timeout_ms, retries,
			retry_interval_s, recovery_checks, paused, push_token, status_since_ms, created_at_ms, tags,
			auto_create_incident, auto_resolve_incident, position)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (select coalesce(max(position), 0) + 1 from monitors))
		returning id`,
		m.Name, m.Kind, m.Target, m.Keyword, m.IntervalS, m.TimeoutMs, m.Retries,
		m.RetryIntervalS, m.RecoveryChecks, m.Paused, m.PushToken, nowMs, nowMs, m.Tags,
		cmp.Or(m.AutoCreateIncident, IncidentCreateDraft), cmp.Or(m.AutoResolveIncident, IncidentResolveDrafts)).Scan(&id)
	return id, err
}

// MonitorDisplays are the ways a status page can draw a monitor.
var MonitorDisplays = []string{"bars", "latency", "summary", "minimal"}

// displayOrDefault lets callers that do not care about the display leave it empty.
func displayOrDefault(display string) string {
	if display == "" {
		return "bars"
	}
	return display
}

// UpdateMonitor changes a monitor's settings. Its state and history are kept.
func (s *Store) UpdateMonitor(ctx context.Context, m Monitor) error {
	return updateMonitor(ctx, s.db.ExecContext, m)
}

// EditMonitor preserves unspecified settings and updates only configuration.
func (s *Store) EditMonitor(ctx context.Context, id int64, edit func(*Monitor) error) error {
	return s.write(ctx, func(tx *transaction) error {
		m, err := scanMonitor(tx.QueryRowContext(ctx, "select "+monitorColumns+" from monitors where id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := edit(&m); err != nil {
			return err
		}
		return updateMonitor(ctx, tx.ExecContext, m)
	})
}

func updateMonitor(ctx context.Context, exec execSQL, m Monitor) error {
	res, err := exec(ctx, `
		update monitors set name = ?, kind = ?, target = ?, keyword = ?, interval_s = ?, timeout_ms = ?,
			retries = ?, retry_interval_s = ?, recovery_checks = ?, paused = ?, push_token = ?, tags = ?,
			auto_create_incident = coalesce(nullif(?, ''), auto_create_incident),
			auto_resolve_incident = coalesce(nullif(?, ''), auto_resolve_incident)
		where id = ?`,
		m.Name, m.Kind, m.Target, m.Keyword, m.IntervalS, m.TimeoutMs, m.Retries, m.RetryIntervalS,
		m.RecoveryChecks, m.Paused, m.PushToken, m.Tags, m.AutoCreateIncident, m.AutoResolveIncident, m.ID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// DeleteMonitor removes a monitor and its history. It leaves every status page with it.
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

// Monitors lists every monitor in the order the admin gave them.
func (s *Store) Monitors(ctx context.Context) ([]Monitor, error) {
	return s.monitors(ctx, "select "+monitorColumns+" from monitors order by position, id")
}

// MonitorsAfter lists the next monitors in ID order.
func (s *Store) MonitorsAfter(ctx context.Context, after int64, limit int) ([]Monitor, error) {
	return s.monitors(ctx, "select "+monitorColumns+" from monitors where id > ? order by id limit ?", after, limit)
}

// SetMonitorOrder puts the monitors in the order of ids. A monitor that is
// not named keeps its place behind the named ones.
func (s *Store) SetMonitorOrder(ctx context.Context, ids []int64) error {
	return s.write(ctx, func(tx *transaction) error {
		if _, err := tx.ExecContext(ctx, "update monitors set position = position + ?", len(ids)); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, "update monitors set position = ? where id = ?", i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
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
