package store

import (
	"context"
	"database/sql"
	"strings"
)

// Incident statuses, in the order an incident usually moves through them.
var IncidentStatuses = []string{"investigating", "identified", "monitoring", "resolved"}

// Incident impacts, mildest first.
var IncidentImpacts = []string{"minor", "major", "critical"}

// Incident is a problem communicated on a status page. An unpublished
// incident is a private draft that only the admin sees.
type Incident struct {
	ID              int64
	ProjectID       int64
	Title           string
	Impact          string
	Status          string
	Published       bool
	StartedAtMs     int64
	ResolvedAtMs    sql.NullInt64
	SourceMonitorID sql.NullInt64
	Updates         []IncidentUpdate // newest first
	MonitorIDs      []int64
}

// IncidentUpdate is one dated message in an incident's timeline.
type IncidentUpdate struct {
	ID     int64
	Status string
	Title  string
	Body   string
	AtMs   int64
}

// CreateIncident stores an incident with its first update and affected monitors.
func (s *Store) CreateIncident(ctx context.Context, in Incident, firstUpdate IncidentUpdate, nowMs int64) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *transaction) error {
		var err error
		id, err = insertIncident(ctx, tx, in, firstUpdate, nowMs, sql.NullInt64{})
		return err
	})
	return id, err
}

func insertIncident(
	ctx context.Context, tx *transaction, in Incident, firstUpdate IncidentUpdate, nowMs int64, sourceMonitorID sql.NullInt64,
) (int64, error) {
	var resolvedAtMs sql.NullInt64
	if in.Status == "resolved" {
		resolvedAtMs = sql.NullInt64{Int64: nowMs, Valid: true}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		insert into incidents (project_id, title, impact, status, published, started_at_ms, resolved_at_ms, created_at_ms,
			source_monitor_id)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?) returning id`,
		in.ProjectID, in.Title, in.Impact, in.Status, in.Published, in.StartedAtMs, resolvedAtMs, nowMs, sourceMonitorID).Scan(&id)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		"insert into incident_updates (incident_id, status, title, body, at_ms) values (?, ?, ?, ?, ?)",
		id, in.Status, firstUpdate.Title, firstUpdate.Body, nowMs); err != nil {
		return 0, err
	}
	if err := insertIncidentMonitors(ctx, tx, id, in.ProjectID, in.MonitorIDs); err != nil {
		return 0, err
	}
	if !in.Published {
		return id, nil
	}
	_, err = tx.ExecContext(ctx, `
		insert into events (project_id, kind, at_ms, incident_id, title, detail)
		values (?, 'incident_published', ?, ?, ?, ?)`, in.ProjectID, nowMs, id, in.Title, firstUpdate.Body)
	return id, err
}

func insertIncidentMonitors(ctx context.Context, tx *transaction, incidentID, projectID int64, monitorIDs []int64) error {
	for _, monitorID := range monitorIDs {
		// Only link monitors the page shows.
		if _, err := tx.ExecContext(ctx, `
			insert into incident_monitors (incident_id, monitor_id)
			select ?, monitor_id from page_monitors where monitor_id = ? and project_id = ?`,
			incidentID, monitorID, projectID); err != nil {
			return err
		}
	}
	return nil
}

// AddIncidentUpdate appends an update and moves the incident to its status.
func (s *Store) AddIncidentUpdate(ctx context.Context, incidentID int64, update IncidentUpdate) error {
	return s.write(ctx, func(tx *transaction) error {
		return addIncidentUpdate(ctx, tx, incidentID, update)
	})
}

func addIncidentUpdate(ctx context.Context, tx *transaction, incidentID int64, update IncidentUpdate) error {
	var exists bool
	err := tx.QueryRowContext(ctx, "select exists (select 1 from incidents where id = ?)", incidentID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	var resolvedAt sql.NullInt64
	if update.Status == "resolved" {
		resolvedAt = sql.NullInt64{Int64: update.AtMs, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, "update incidents set status = ?, resolved_at_ms = ? where id = ?",
		update.Status, resolvedAt, incidentID); err != nil {
		return err
	}
	return appendIncidentUpdate(ctx, tx, incidentID, update)
}

func appendIncidentUpdate(ctx context.Context, tx *transaction, incidentID int64, update IncidentUpdate) error {
	if _, err := tx.ExecContext(ctx,
		"insert into incident_updates (incident_id, status, title, body, at_ms) values (?, ?, ?, ?, ?)",
		incidentID, update.Status, update.Title, update.Body, update.AtMs); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		insert into events (project_id, kind, at_ms, incident_id, title, detail)
		select project_id, 'incident_update', ?, id, title, ? from incidents where id = ? and published = 1`,
		update.AtMs, update.Status+": "+update.Body, incidentID)
	return err
}

// PublishIncident makes a draft visible on the status page. Publishing an
// incident that is already public changes nothing.
func (s *Store) PublishIncident(ctx context.Context, incidentID, nowMs int64) error {
	return s.write(ctx, func(tx *transaction) error {
		return publishIncident(ctx, tx, incidentID, nowMs)
	})
}

func publishIncident(ctx context.Context, tx *transaction, incidentID, nowMs int64) error {
	var projectID int64
	var title string
	res, err := tx.ExecContext(ctx, "update incidents set published = 1 where id = ? and published = 0", incidentID)
	if err != nil {
		return err
	}
	updated, err := res.RowsAffected()
	if err != nil || updated == 0 {
		return err
	}
	if err := tx.QueryRowContext(ctx, "select project_id, title from incidents where id = ?", incidentID).Scan(&projectID, &title); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		insert into events (project_id, kind, at_ms, incident_id, title)
		values (?, 'incident_published', ?, ?, ?)`, projectID, nowMs, incidentID, title)
	return err
}

// DeleteIncident removes an incident and its updates.
func (s *Store) DeleteIncident(ctx context.Context, incidentID int64) error {
	res, err := s.db.ExecContext(ctx, "delete from incidents where id = ?", incidentID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// maxIncidentsPerPage bounds how many incidents one page load reads.
const maxIncidentsPerPage = 200

// Incident returns one incident with its updates and monitors, or ErrNotFound.
func (s *Store) Incident(ctx context.Context, incidentID int64) (Incident, error) {
	incidents, err := s.incidents(ctx, "i.id = ?", incidentID, 1)
	if err != nil {
		return Incident{}, err
	}
	if len(incidents) == 0 {
		return Incident{}, ErrNotFound
	}
	return incidents[0], nil
}

// PublishedIncidents returns a project's public incidents that started at or
// after sinceMs or are still open, newest first.
func (s *Store) PublishedIncidents(ctx context.Context, projectID, sinceMs int64) ([]Incident, error) {
	return s.incidents(ctx,
		"i.project_id = ? and i.published = 1 and (i.started_at_ms >= ? or i.resolved_at_ms is null)",
		projectID, sinceMs, maxIncidentsPerPage)
}

// IncidentsByProject returns the newest incidents of a project, drafts included.
func (s *Store) IncidentsByProject(ctx context.Context, projectID int64) ([]Incident, error) {
	return s.incidents(ctx, "i.project_id = ?", projectID, maxIncidentsPerPage)
}

// OpenIncidents returns the unresolved incidents of all projects, drafts
// included, newest first.
func (s *Store) OpenIncidents(ctx context.Context) ([]Incident, error) {
	return s.incidents(ctx, "i.resolved_at_ms is null", maxIncidentsPerPage)
}

const incidentSummaryColumns = `i.id, i.project_id, i.title, i.impact, i.status, i.published, i.started_at_ms, i.resolved_at_ms, i.source_monitor_id`

func scanIncident(row rowScanner, in *Incident, extra ...any) error {
	columns := []any{&in.ID, &in.ProjectID, &in.Title, &in.Impact, &in.Status, &in.Published, &in.StartedAtMs, &in.ResolvedAtMs, &in.SourceMonitorID}
	return row.Scan(append(columns, extra...)...)
}

// incidents loads the newest incidents matching the condition, then their
// updates and monitors with one query each. args holds the condition's
// values followed by the row limit.
func (s *Store) incidents(ctx context.Context, condition string, args ...any) ([]Incident, error) {
	const newestFirst = " order by i.started_at_ms desc, i.id desc"
	rows, err := s.db.QueryContext(ctx,
		"select "+incidentSummaryColumns+" from incidents i where "+condition+newestFirst+" limit ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var incidents []Incident
	for rows.Next() {
		var in Incident
		if err := scanIncident(rows, &in); err != nil {
			return nil, err
		}
		incidents = append(incidents, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	details := make([]*Incident, len(incidents))
	for i := range incidents {
		details[i] = &incidents[i]
	}
	return incidents, s.loadIncidentDetails(ctx, details)
}

// loadIncidentDetails fills in the updates, newest first, and the affected
// monitors of the given incidents, with one query each.
func (s *Store) loadIncidentDetails(ctx context.Context, incidents []*Incident) error {
	if len(incidents) == 0 {
		return nil
	}
	ids := make([]any, len(incidents))
	byID := make(map[int64]*Incident, len(incidents))
	for i, in := range incidents {
		ids[i] = in.ID
		byID[in.ID] = in
	}
	selected := strings.Repeat("?, ", len(ids)-1) + "?"

	updates, err := s.db.QueryContext(ctx, `
		select incident_id, id, status, title, body, at_ms from incident_updates
		where incident_id in (`+selected+`) order by at_ms desc, id desc`, ids...)
	if err != nil {
		return err
	}
	defer updates.Close()
	for updates.Next() {
		var incidentID int64
		var u IncidentUpdate
		if err := updates.Scan(&incidentID, &u.ID, &u.Status, &u.Title, &u.Body, &u.AtMs); err != nil {
			return err
		}
		in := byID[incidentID]
		in.Updates = append(in.Updates, u)
	}
	if err := updates.Err(); err != nil {
		return err
	}

	links, err := s.db.QueryContext(ctx, `
		select incident_id, monitor_id from incident_monitors
		where incident_id in (`+selected+`) order by monitor_id`, ids...)
	if err != nil {
		return err
	}
	defer links.Close()
	for links.Next() {
		var incidentID, monitorID int64
		if err := links.Scan(&incidentID, &monitorID); err != nil {
			return err
		}
		in := byID[incidentID]
		in.MonitorIDs = append(in.MonitorIDs, monitorID)
	}
	return links.Err()
}

// PublishedIncidentsBetween returns a project's public incidents that started
// in [fromMs, toMs), newest first.
func (s *Store) PublishedIncidentsBetween(ctx context.Context, projectID, fromMs, toMs int64) ([]Incident, error) {
	return s.incidents(ctx,
		"i.project_id = ? and i.published = 1 and i.started_at_ms >= ? and i.started_at_ms < ?",
		projectID, fromMs, toMs, maxIncidentsPerPage)
}
