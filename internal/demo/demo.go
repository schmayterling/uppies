// Package demo fills an empty database with example data that shows every
// part of the interface: each monitor kind, each state, history, incidents,
// maintenance, alert channels and status page settings.
package demo

import (
	"context"
	"errors"
	"strings"
	"time"

	"uppies/internal/health"
	"uppies/internal/store"
)

// ErrNotEmpty is returned when the database already has projects. Demo data
// is never mixed into real data.
var ErrNotEmpty = errors.New("the database already has projects, so no demo data was added")

const dayMs = int64(24 * time.Hour / time.Millisecond)

// monitorSpec describes one demo monitor. The targets are real, stable public
// addresses, or addresses reserved for documentation that never answer.
type monitorSpec struct {
	name, kind, target, keyword string
	intervalS                   int
	paused, hidden              bool
	// display is how the status page draws the monitor, empty for the default bars.
	display  string
	category string
	// state is how the monitor looks right after seeding: "up", "down" or "" for not checked yet.
	state string
	// historyDays is how many past days of history to invent, and badDays maps
	// "days ago" to minutes of downtime on that day.
	historyDays int
	badDays     map[int]int
}

var hibiMonitors = []monitorSpec{
	{name: "website", category: "web", kind: "http", target: "https://example.com/", keyword: "Example Domain", intervalS: 60, state: "up", historyDays: 90, badDays: map[int]int{12: 25, 40: 180}},
	{name: "api", category: "web", kind: "http", target: "https://www.cloudflare.com/cdn-cgi/trace", intervalS: 60, state: "up", historyDays: 90, badDays: map[int]int{5: 4, 6: 95, 70: 10}, display: "latency"},
	{name: "sync service", category: "web", kind: "http", target: "https://uppies-demo.invalid/health", intervalS: 60, state: "down", historyDays: 45, badDays: map[int]int{1: 30}},
	{name: "database port", category: "infrastructure", kind: "tcp", target: "one.one.one.one:443", intervalS: 120, state: "up", historyDays: 20, display: "minimal"},
	{name: "dns record", category: "infrastructure", kind: "dns", target: "example.com", intervalS: 300, state: "up", historyDays: 60, badDays: map[int]int{33: 12}},
	{name: "edge network", category: "infrastructure", kind: "ping", target: "1.1.1.1", intervalS: 60, state: "up", historyDays: 90, display: "summary"},
	{name: "nightly backup", category: "jobs", kind: "push", target: "push", intervalS: 86400, state: "up", historyDays: 30, badDays: map[int]int{9: 1440}},
	{name: "legacy importer", kind: "http", target: "https://example.org/", intervalS: 300, paused: true, state: "up", historyDays: 10},
	{name: "a monitor with an unusually long name that has to wrap somewhere", kind: "tcp", target: "192.0.2.1:81", intervalS: 120, state: "down", historyDays: 3},
	{name: "internal queue", kind: "http", target: "https://example.net/", intervalS: 120, hidden: true, state: "up", historyDays: 14},
	{name: "brand new check", kind: "http", target: "https://www.iana.org/", intervalS: 60},
}

var toolsMonitors = []monitorSpec{
	{name: "wiki", kind: "http", target: "https://www.wikipedia.org/", intervalS: 120, state: "up", historyDays: 90},
	{name: "build runner", kind: "push", target: "push", intervalS: 3600},
}

var billingMonitors = []monitorSpec{
	{name: "checkout", kind: "http", target: "https://example.com/", intervalS: 60, state: "up", historyDays: 90, badDays: map[int]int{20: 45}},
	{name: "invoices", kind: "http", target: "https://example.org/", intervalS: 60, state: "up", historyDays: 90},
}

// Seed fills an empty database. It returns ErrNotEmpty and changes nothing
// when any project exists.
func Seed(ctx context.Context, st *store.Store, now time.Time) error {
	existing, err := st.Projects(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return ErrNotEmpty
	}
	nowMs := now.UnixMilli()

	hibi, hibiIDs, err := seedProject(ctx, st, now, store.Project{
		Slug: "hibi", Name: "hibi", Tagline: "notes that sync, most of the time.", HomepageURL: "https://example.com",
		AccentHue: 330, Theme: "auto", ShowFace: true, HistoryDays: 90, ShowPoweredBy: true, Timezone: "Asia/Manila",
		FooterText: "questions? write to help@hibi.example",
	}, hibiMonitors)
	if err != nil {
		return err
	}
	channels := []store.AlertChannel{
		{Kind: "email", URL: "ops@hibi.example"},
		{Kind: "discord", URL: "https://discord.example/api/webhooks/demo"},
		{Kind: "slack", URL: "https://hooks.slack.example/services/demo"},
		{Kind: "webhook", URL: "https://hibi.example/hooks/uptime"},
	}
	for _, c := range channels {
		c.ProjectID = hibi
		if _, err := st.CreateAlertChannel(ctx, c, nowMs); err != nil {
			return err
		}
	}
	if err := seedIncidents(ctx, st, hibi, hibiIDs, nowMs); err != nil {
		return err
	}
	upcoming := store.MaintenanceWindow{ProjectID: hibi, Title: "database upgrade", StartsAtMs: nowMs + 2*dayMs, EndsAtMs: nowMs + 2*dayMs + 2*3600_000}
	if _, err := st.CreateMaintenanceWindow(ctx, upcoming, nowMs); err != nil {
		return err
	}

	if _, _, err := seedProject(ctx, st, now, store.Project{
		Slug: "internal-tools", Name: "internal tools for the team with a long project name",
		AccentHue: 200, Theme: "dark", ShowFace: false, HistoryDays: 30, ShowPoweredBy: false, NoIndex: true, Timezone: "UTC",
		Corners: "sharp", Font: "mono", Backfill: "up",
	}, toolsMonitors); err != nil {
		return err
	}

	billing, _, err := seedProject(ctx, st, now, store.Project{
		Slug: "billing", Name: "billing", Tagline: "payments and invoices.",
		AccentHue: 150, Theme: "light", ShowFace: true, HistoryDays: 60, ShowPoweredBy: true, Timezone: "Europe/Berlin",
		Surface: "paper", Corners: "round", Font: "rounded",
	}, billingMonitors)
	if err != nil {
		return err
	}
	// A window that is running now: this project shows what maintenance looks like.
	running := store.MaintenanceWindow{ProjectID: billing, Title: "moving to a new payment provider", StartsAtMs: nowMs - 3600_000, EndsAtMs: nowMs + 6*3600_000}
	if _, err := st.CreateMaintenanceWindow(ctx, running, nowMs); err != nil {
		return err
	}

	_, err = st.CreateProject(ctx, "empty", "an empty project", nowMs)
	return err
}

// seedProject creates a project with its settings and monitors, and returns
// the project id and the monitor ids by name.
func seedProject(ctx context.Context, st *store.Store, now time.Time, p store.Project, specs []monitorSpec) (int64, map[string]int64, error) {
	nowMs := now.UnixMilli()
	id, err := st.CreateProject(ctx, p.Slug, p.Name, nowMs)
	if err != nil {
		return 0, nil, err
	}
	p.ID = id
	if err := st.UpdateProject(ctx, p); err != nil {
		return 0, nil, err
	}
	ids := make(map[string]int64, len(specs))
	for _, spec := range specs {
		monitorID, err := seedMonitor(ctx, st, now, id, spec)
		if err != nil {
			return 0, nil, err
		}
		ids[spec.name] = monitorID
	}
	return id, ids, nil
}

func seedMonitor(ctx context.Context, st *store.Store, now time.Time, projectID int64, spec monitorSpec) (int64, error) {
	nowMs := now.UnixMilli()
	m := store.Monitor{
		ProjectID: projectID, Name: spec.name, Kind: spec.kind, Target: spec.target, Keyword: spec.keyword,
		IntervalS: spec.intervalS, TimeoutMs: 10_000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
		Paused: spec.paused, Public: !spec.hidden, Display: spec.display, Category: spec.category,
	}
	if spec.kind == "push" {
		// A token goes into a URL, so the spaces of the name become dashes.
		m.PushToken = "demo-" + strings.ReplaceAll(spec.name, " ", "-")
	}
	id, err := st.CreateMonitor(ctx, m, nowMs)
	if err != nil {
		return 0, err
	}

	todayStartMs := store.DayStartMs(nowMs)
	for daysAgo := spec.historyDays; daysAgo >= 1; daysAgo-- {
		downMs := int64(spec.badDays[daysAgo]) * 60_000
		checks := dayMs / (int64(spec.intervalS) * 1000)
		day := store.DayRollup{
			DayStartMs: todayStartMs - int64(daysAgo)*dayMs, Checks: checks,
			Failures: downMs / (int64(spec.intervalS) * 1000), CoveredMs: dayMs, DownMs: downMs,
		}
		// The response time drifts between 30 and 65 ms so that the latency line has a shape.
		averageMs := int64(30 + (daysAgo*7)%36)
		if err := st.ImportDailyRollup(ctx, id, day, checks*averageMs, 180); err != nil {
			return 0, err
		}
	}

	switch spec.state {
	case "up":
		step := health.Step{Tracker: health.Tracker{Status: health.Up}}
		err = st.RecordCheck(ctx, store.CheckResult{MonitorID: id, AtMs: nowMs, OK: true, LatencyMs: 42, Step: step})
		if err == nil && spec.kind == "push" {
			err = st.RecordPush(ctx, m.PushToken, nowMs)
		}
	case "down":
		tracker := health.Tracker{Status: health.Down, FailStreak: 3, FailingSinceMs: nowMs - 25*60_000}
		step := health.Step{Tracker: tracker, Change: health.WentDown, DownMs: 25 * 60_000}
		err = st.RecordCheck(ctx, store.CheckResult{
			MonitorID: id, AtMs: nowMs, Error: "no answer within 10s", CoveredMs: 25 * 60_000, Step: step,
		})
	}
	return id, err
}

// seedIncidents adds one incident in each state a page can show: open with
// several updates, resolved in the past, and a draft only the admin sees.
func seedIncidents(ctx context.Context, st *store.Store, projectID int64, monitors map[string]int64, nowMs int64) error {
	const hourMs = 3600_000
	open := store.Incident{
		ProjectID: projectID, Title: "slow sync for some notes", Impact: "minor", Status: "investigating",
		Published: true, StartedAtMs: nowMs - 3*hourMs, MonitorIDs: []int64{monitors["api"]},
	}
	openID, err := st.CreateIncident(ctx, open, "some notes take longer than usual to sync. we are looking into it.", nowMs-3*hourMs)
	if err != nil {
		return err
	}
	if err := st.AddIncidentUpdate(ctx, openID, "identified", "a queue worker is stuck. we are restarting it.", nowMs-2*hourMs); err != nil {
		return err
	}
	if err := st.AddIncidentUpdate(ctx, openID, "monitoring", "the queue is draining. sync times are back to normal for most people.", nowMs-hourMs); err != nil {
		return err
	}

	past := store.Incident{
		ProjectID: projectID, Title: "website outage", Impact: "major", Status: "investigating",
		Published: true, StartedAtMs: nowMs - 40*dayMs, MonitorIDs: []int64{monitors["website"]},
	}
	pastID, err := st.CreateIncident(ctx, past, "the website does not load.", nowMs-40*dayMs)
	if err != nil {
		return err
	}
	if err := st.AddIncidentUpdate(ctx, pastID, "resolved", "a certificate had expired. it is renewed and we added an expiry check.", nowMs-40*dayMs+3*hourMs); err != nil {
		return err
	}

	critical := store.Incident{
		ProjectID: projectID, Title: "data centre power failure", Impact: "critical", Status: "investigating",
		Published: true, StartedAtMs: nowMs - 70*dayMs,
	}
	criticalID, err := st.CreateIncident(ctx, critical, "everything hosted in the frankfurt data centre is unreachable.", nowMs-70*dayMs)
	if err != nil {
		return err
	}
	return st.AddIncidentUpdate(ctx, criticalID, "resolved", "power is back and all services have recovered.", nowMs-70*dayMs+5*hourMs)
}
