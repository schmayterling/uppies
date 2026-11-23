package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"uppies/internal/health"
)

const dayMs = int64(24 * time.Hour / time.Millisecond)

// DayStartMs returns the start of the UTC day that contains atMs.
func DayStartMs(atMs int64) int64 {
	return atMs - atMs%dayMs
}

// CheckResult is one finished check and what it did to the monitor's state.
type CheckResult struct {
	MonitorID int64
	AtMs      int64
	OK        bool
	LatencyMs int64
	Error     string
	// CoveredMs is the time this check accounts for: the gap since the previous check.
	CoveredMs int64
	Step      health.Step
	// CertExpiresAtMs is the certificate expiry seen by this check, 0 when it saw none.
	CertExpiresAtMs int64
}

// RecordCheck stores a check, adds it to the day's rollup, updates the
// monitor's state and, on a confirmed change, appends a timeline event and
// opens or closes the monitor's draft incident.
// It returns ErrNotFound when the monitor was deleted while the check ran.
func (s *Store) RecordCheck(ctx context.Context, r CheckResult) error {
	return s.write(ctx, func(tx *transaction) error {
		var name string
		err := tx.QueryRowContext(ctx, "select name from monitors where id = ?", r.MonitorID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			"insert into checks (monitor_id, at_ms, ok, latency_ms, error) values (?, ?, ?, ?, ?)",
			r.MonitorID, r.AtMs, r.OK, r.LatencyMs, r.Error); err != nil {
			return err
		}

		failures := 0
		if !r.OK {
			failures = 1
		}
		conflict := `on conflict (monitor_id, day_start_ms) do update set
			checks = daily_rollups.checks + 1,
			failures = daily_rollups.failures + excluded.failures,
			covered_ms = daily_rollups.covered_ms + excluded.covered_ms,
			down_ms = daily_rollups.down_ms + excluded.down_ms,
			latency_sum_ms = daily_rollups.latency_sum_ms + excluded.latency_sum_ms,
			latency_max_ms = case when daily_rollups.latency_max_ms > excluded.latency_max_ms
				then daily_rollups.latency_max_ms else excluded.latency_max_ms end`
		if s.Backend() == MariaDB {
			conflict = `on duplicate key update
				checks = checks + 1,
				failures = failures + values(failures),
				covered_ms = covered_ms + values(covered_ms),
				down_ms = down_ms + values(down_ms),
				latency_sum_ms = latency_sum_ms + values(latency_sum_ms),
				latency_max_ms = greatest(latency_max_ms, values(latency_max_ms))`
		}
		if _, err := tx.ExecContext(ctx, `
			insert into daily_rollups (monitor_id, day_start_ms, checks, failures, covered_ms, down_ms,
				latency_sum_ms, latency_max_ms)
			values (?, ?, 1, ?, ?, ?, ?, ?) `+conflict,
			r.MonitorID, DayStartMs(r.AtMs), failures, r.CoveredMs, r.Step.DownMs, r.LatencyMs, r.LatencyMs); err != nil {
			return err
		}

		t := r.Step.Tracker
		changed := r.Step.Change != health.NoChange
		if _, err := tx.ExecContext(ctx, `
			update monitors set status = ?, fail_streak = ?, ok_streak = ?, failing_since_ms = ?,
				last_check_at_ms = ?, last_latency_ms = ?, last_error = ?,
				status_since_ms = case when ? = 1 then ? else status_since_ms end,
				cert_expires_at_ms = case when ? = 1 then ? else cert_expires_at_ms end
			where id = ?`,
			t.Status, t.FailStreak, t.OKStreak, t.FailingSinceMs, r.AtMs, r.LatencyMs, r.Error,
			changed, r.AtMs, r.CertExpiresAtMs > 0, r.CertExpiresAtMs, r.MonitorID); err != nil {
			return err
		}
		if !changed {
			return nil
		}

		kind := "monitor_up"
		if r.Step.Change == health.WentDown {
			kind = "monitor_down"
		}
		if _, err := tx.ExecContext(ctx,
			"insert into events (kind, at_ms, monitor_id, title, detail) values (?, ?, ?, ?, ?)",
			kind, r.AtMs, r.MonitorID, name, r.Error); err != nil {
			return err
		}
		if r.Step.Change == health.WentDown {
			return openDraftIncidents(ctx, tx, name, r)
		}
		return resolveDraftIncidents(ctx, tx, r.MonitorID, r.AtMs)
	})
}

// openDraftIncidents prepares a draft on every status page that shows the
// monitor. A monitor on no page gets none.
func openDraftIncidents(ctx context.Context, tx *transaction, monitorName string, r CheckResult) error {
	rows, err := tx.QueryContext(ctx, "select project_id from page_monitors where monitor_id = ? order by project_id", r.MonitorID)
	if err != nil {
		return err
	}
	var projectIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		projectIDs = append(projectIDs, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, projectID := range projectIDs {
		if err := openDraftIncident(ctx, tx, projectID, monitorName, r); err != nil {
			return err
		}
	}
	return nil
}

// openDraftIncident prepares a private incident for a confirmed outage, so
// that publishing it only needs a person's decision.
func openDraftIncident(ctx context.Context, tx *transaction, projectID int64, monitorName string, r CheckResult) error {
	// The raw check error can name private hosts, and this text becomes public
	// when the draft is published. The error stays in the admin timeline.
	const firstUpdate = "we are looking into a problem with %s."
	_, err := insertIncident(ctx, tx, Incident{
		ProjectID:   projectID,
		Title:       monitorName + " is down",
		Impact:      "major",
		Status:      "investigating",
		StartedAtMs: r.Step.Tracker.FailingSinceMs,
		MonitorIDs:  []int64{r.MonitorID},
	}, fmt.Sprintf(firstUpdate, monitorName), r.AtMs)
	return err
}

// resolveDraftIncidents closes the unpublished incidents of a recovered
// monitor. Published incidents stay open until a person resolves them.
func resolveDraftIncidents(ctx context.Context, tx *transaction, monitorID, nowMs int64) error {
	rows, err := tx.QueryContext(ctx, `
		select i.id from incidents i
		join incident_monitors im on im.incident_id = i.id
		where im.monitor_id = ? and i.published = 0 and i.resolved_at_ms is null`, monitorID)
	if err != nil {
		return err
	}
	var drafts []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		drafts = append(drafts, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range drafts {
		if err := addIncidentUpdate(ctx, tx, id, "resolved", "the monitor recovered", nowMs); err != nil {
			return err
		}
	}
	return nil
}

// DayRollup is one monitor's totals for one UTC day.
type DayRollup struct {
	DayStartMs int64
	Checks     int64
	Failures   int64
	CoveredMs  int64
	DownMs     int64
	// LatencyAvgMs is the mean response time of the day's checks.
	LatencyAvgMs int64
}

// DailyRollups returns each monitor's days from sinceMs on, oldest first,
// for all monitors a status page shows in one query.
func (s *Store) DailyRollups(ctx context.Context, projectID, sinceMs int64) (map[int64][]DayRollup, error) {
	rows, err := s.db.QueryContext(ctx, `
		select r.monitor_id, r.day_start_ms, r.checks, r.failures, r.covered_ms, r.down_ms,
			r.latency_sum_ms
		from daily_rollups r
		where r.monitor_id in (select monitor_id from page_monitors where project_id = ?) and r.day_start_ms >= ?
		order by r.monitor_id, r.day_start_ms`, projectID, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byMonitor := make(map[int64][]DayRollup)
	for rows.Next() {
		var monitorID int64
		var d DayRollup
		var latencySum int64
		if err := rows.Scan(&monitorID, &d.DayStartMs, &d.Checks, &d.Failures, &d.CoveredMs, &d.DownMs, &latencySum); err != nil {
			return nil, err
		}
		d.LatencyAvgMs = latencySum / max(d.Checks, 1)
		byMonitor[monitorID] = append(byMonitor[monitorID], d)
	}
	return byMonitor, rows.Err()
}

// PruneChecks deletes raw checks older than beforeMs. Rollups and events stay.
func (s *Store) PruneChecks(ctx context.Context, beforeMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, "delete from checks where at_ms < ?", beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Event is one entry of the timeline.
type Event struct {
	ID int64
	// ProjectID is the status page an incident's entry belongs to, and 0 for
	// a monitor's entry, which belongs to every page that shows the monitor.
	ProjectID  int64
	Kind       string
	AtMs       int64
	MonitorID  sql.NullInt64
	IncidentID sql.NullInt64
	Title      string
	Detail     string
}

// Events returns a project's timeline, newest first. beforeID pages backwards:
// pass 0 for the first page, then the last id of the previous page.
func (s *Store) Events(ctx context.Context, projectID, beforeID int64, limit int) ([]Event, error) {
	if beforeID == 0 {
		beforeID = 1<<63 - 1
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, coalesce(project_id, 0), kind, at_ms, monitor_id, incident_id, title, detail
		from events
		where (project_id = ? or monitor_id in (select monitor_id from page_monitors where project_id = ?)) and id < ?
		order by id desc limit ?`, projectID, projectID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Kind, &e.AtMs, &e.MonitorID, &e.IncidentID, &e.Title, &e.Detail); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// AllDailyRollups returns every monitor's days from sinceMs on, oldest first,
// for the overview of all projects.
func (s *Store) AllDailyRollups(ctx context.Context, sinceMs int64) (map[int64][]DayRollup, error) {
	rows, err := s.db.QueryContext(ctx, `
		select monitor_id, day_start_ms, checks, failures, covered_ms, down_ms
		from daily_rollups where day_start_ms >= ? order by monitor_id, day_start_ms`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byMonitor := make(map[int64][]DayRollup)
	for rows.Next() {
		var monitorID int64
		var d DayRollup
		if err := rows.Scan(&monitorID, &d.DayStartMs, &d.Checks, &d.Failures, &d.CoveredMs, &d.DownMs); err != nil {
			return nil, err
		}
		byMonitor[monitorID] = append(byMonitor[monitorID], d)
	}
	return byMonitor, rows.Err()
}

// RecentEvents returns the newest timeline entries across all projects.
func (s *Store) RecentEvents(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, coalesce(project_id, 0), kind, at_ms, monitor_id, incident_id, title, detail
		from events order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Kind, &e.AtMs, &e.MonitorID, &e.IncidentID, &e.Title, &e.Detail); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ImportDailyRollup stores one day of history for a monitor, replacing what
// was there. It exists for data that did not come from live checks.
func (s *Store) ImportDailyRollup(ctx context.Context, monitorID int64, d DayRollup, latencySumMs, latencyMaxMs int64) error {
	conflict := `on conflict (monitor_id, day_start_ms) do update set
		checks = excluded.checks, failures = excluded.failures, covered_ms = excluded.covered_ms,
		down_ms = excluded.down_ms, latency_sum_ms = excluded.latency_sum_ms, latency_max_ms = excluded.latency_max_ms`
	if s.Backend() == MariaDB {
		conflict = `on duplicate key update
			checks = values(checks), failures = values(failures), covered_ms = values(covered_ms),
			down_ms = values(down_ms), latency_sum_ms = values(latency_sum_ms), latency_max_ms = values(latency_max_ms)`
	}
	_, err := s.db.ExecContext(ctx, `
		insert into daily_rollups (monitor_id, day_start_ms, checks, failures, covered_ms, down_ms,
			latency_sum_ms, latency_max_ms)
		values (?, ?, ?, ?, ?, ?, ?, ?) `+conflict,
		monitorID, d.DayStartMs, d.Checks, d.Failures, d.CoveredMs, d.DownMs, latencySumMs, latencyMaxMs)
	return err
}

// LatenciesSince returns the response times of a monitor's successful checks
// since sinceMs, smallest first. Raw checks are pruned after two days, so the
// list is bounded by the monitor's interval: 8640 rows at the shortest one.
func (s *Store) LatenciesSince(ctx context.Context, monitorID, sinceMs int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		select latency_ms from checks
		where monitor_id = ? and at_ms >= ? and ok = 1
		order by latency_ms`, monitorID, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var latencies []int64
	for rows.Next() {
		var ms int64
		if err := rows.Scan(&ms); err != nil {
			return nil, err
		}
		latencies = append(latencies, ms)
	}
	return latencies, rows.Err()
}

// CheckBucket sums up a monitor's checks in one stretch of time.
type CheckBucket struct {
	StartMs  int64
	Checks   int64
	Failures int64
	// LatencyAvgMs and LatencyMaxMs cover the successful checks only. Both are 0 when every check failed.
	LatencyAvgMs int64
	LatencyMaxMs int64
}

// CheckBuckets groups a monitor's checks since sinceMs into stretches of
// bucketMs, oldest first. A stretch without checks is left out.
func (s *Store) CheckBuckets(ctx context.Context, monitorID, sinceMs, bucketMs int64) ([]CheckBucket, error) {
	bucketStart := "at_ms - at_ms % ?"
	if s.Backend() == TursoPG {
		bucketStart = "cast(at_ms - at_ms % cast(? as bigint) as bigint)"
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+bucketStart+`, count(*), sum(case when ok = 0 then 1 else 0 end),
			sum(case when ok = 1 then latency_ms else 0 end),
			coalesce(max(case when ok = 1 then latency_ms end), 0)
		from checks
		where monitor_id = ? and at_ms >= ?
		group by 1 order by 1`, bucketMs, monitorID, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var buckets []CheckBucket
	for rows.Next() {
		var b CheckBucket
		var latencySum int64
		if err := rows.Scan(&b.StartMs, &b.Checks, &b.Failures, &latencySum, &b.LatencyMaxMs); err != nil {
			return nil, err
		}
		if successful := b.Checks - b.Failures; successful > 0 {
			b.LatencyAvgMs = latencySum / successful
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// Check is one stored check of a monitor.
type Check struct {
	AtMs      int64
	OK        bool
	LatencyMs int64
	Error     string
}

// RecentChecks returns a monitor's latest checks, newest first.
func (s *Store) RecentChecks(ctx context.Context, monitorID int64, limit int) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, `
		select at_ms, ok, latency_ms, error from checks
		where monitor_id = ? order by at_ms desc limit ?`, monitorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checks []Check
	for rows.Next() {
		var c Check
		if err := rows.Scan(&c.AtMs, &c.OK, &c.LatencyMs, &c.Error); err != nil {
			return nil, err
		}
		checks = append(checks, c)
	}
	return checks, rows.Err()
}

// ImportChecks stores past checks of a monitor as they are, in one
// transaction. Unlike RecordCheck it touches neither the monitor's state nor
// the daily figures: it is for history that is already counted elsewhere.
func (s *Store) ImportChecks(ctx context.Context, monitorID int64, checks []Check) error {
	return s.write(ctx, func(tx *transaction) error {
		for _, c := range checks {
			if _, err := tx.ExecContext(ctx,
				"insert into checks (monitor_id, at_ms, ok, latency_ms, error) values (?, ?, ?, ?, ?)",
				monitorID, c.AtMs, c.OK, c.LatencyMs, c.Error); err != nil {
				return err
			}
		}
		return nil
	})
}

// MonitorRollups returns one monitor's days from sinceMs on, oldest first.
func (s *Store) MonitorRollups(ctx context.Context, monitorID, sinceMs int64) ([]DayRollup, error) {
	rows, err := s.db.QueryContext(ctx, `
		select day_start_ms, checks, failures, covered_ms, down_ms, latency_sum_ms
		from daily_rollups where monitor_id = ? and day_start_ms >= ? order by day_start_ms`, monitorID, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []DayRollup
	for rows.Next() {
		var d DayRollup
		var latencySum int64
		if err := rows.Scan(&d.DayStartMs, &d.Checks, &d.Failures, &d.CoveredMs, &d.DownMs, &latencySum); err != nil {
			return nil, err
		}
		d.LatencyAvgMs = latencySum / max(d.Checks, 1)
		days = append(days, d)
	}
	return days, rows.Err()
}
