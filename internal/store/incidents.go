package store

import (
	"context"
	"database/sql"
	"errors"
)

// Incident statuses, in the order an incident usually moves through them.
var IncidentStatuses = []string{"investigating", "identified", "monitoring", "resolved"}

// Incident impacts, mildest first.
var IncidentImpacts = []string{"minor", "major", "critical"}

// Incident is a problem communicated on a status page. An unpublished
// incident is a private draft that only the admin sees.
type Incident struct {
	ID           int64
	ProjectID    int64
	Title        string
	Impact       string
	Status       string
	Published    bool
	StartedAtMs  int64
	ResolvedAtMs sql.NullInt64
	Updates      []IncidentUpdate // newest first
	MonitorIDs   []int64
}

// IncidentUpdate is one dated message in an incident's timeline.
type IncidentUpdate struct {
	ID     int64
	Status string
	Body   string
	AtMs   int64
}

// CreateIncident stores an incident with its first update and affected monitors.
func (s *Store) CreateIncident(ctx context.Context, in Incident, firstUpdate string, nowMs int64) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = insertIncident(ctx, tx, in, firstUpdate, nowMs)
		return err
	})
	return id, err
}

func insertIncident(ctx context.Context, tx *sql.Tx, in Incident, firstUpdate string, nowMs int64) (int64, error) {
	var resolvedAtMs sql.NullInt64
	if in.Status == "resolved" {
		resolvedAtMs = sql.NullInt64{Int64: nowMs, Valid: true}
	}
	res, err := tx.ExecContext(ctx, `
		insert into incidents (project_id, title, impact, status, published, started_at_ms, resolved_at_ms, created_at_ms)
		values (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, in.Title, in.Impact, in.Status, in.Published, in.StartedAtMs, resolvedAtMs, nowMs)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		"insert into incident_updates (incident_id, status, body, at_ms) values (?, ?, ?, ?)",
		id, in.Status, firstUpdate, nowMs); err != nil {
		return 0, err
	}
	for _, monitorID := range in.MonitorIDs {
		// The join on monitors keeps a monitor of another project from being linked.
		if _, err := tx.ExecContext(ctx, `
			insert into incident_monitors (incident_id, monitor_id)
			select ?, id from monitors where id = ? and project_id = ?`, id, monitorID, in.ProjectID); err != nil {
			return 0, err
		}
	}
	if !in.Published {
		return id, nil
	}
	_, err = tx.ExecContext(ctx, `
		insert into events (project_id, kind, at_ms, incident_id, title, detail)
		values (?, 'incident_published', ?, ?, ?, ?)`, in.ProjectID, nowMs, id, in.Title, firstUpdate)
	return id, err
}

// AddIncidentUpdate appends an update and moves the incident to its status.
func (s *Store) AddIncidentUpdate(ctx context.Context, incidentID int64, status, body string, nowMs int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		return addIncidentUpdate(ctx, tx, incidentID, status, body, nowMs)
	})
}

func addIncidentUpdate(ctx context.Context, tx *sql.Tx, incidentID int64, status, body string, nowMs int64) error {
	var projectID int64
	var title string
	var published bool
	err := tx.QueryRowContext(ctx, "select project_id, title, published from incidents where id = ?", incidentID).
		Scan(&projectID, &title, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"insert into incident_updates (incident_id, status, body, at_ms) values (?, ?, ?, ?)",
		incidentID, status, body, nowMs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update incidents set status = ?, resolved_at_ms = case when ? = 'resolved' then ? else null end
		where id = ?`, status, status, nowMs, incidentID); err != nil {
		return err
	}
	if !published {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		insert into events (project_id, kind, at_ms, incident_id, title, detail)
		values (?, 'incident_update', ?, ?, ?, ?)`, projectID, nowMs, incidentID, title, status+": "+body)
	return err
}

// PublishIncident makes a draft visible on the status page. Publishing an
// incident that is already public changes nothing.
func (s *Store) PublishIncident(ctx context.Context, incidentID, nowMs int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var projectID int64
		var title string
		err := tx.QueryRowContext(ctx,
			"update incidents set published = 1 where id = ? and published = 0 returning project_id, title",
			incidentID).Scan(&projectID, &title)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			insert into events (project_id, kind, at_ms, incident_id, title)
			values (?, 'incident_published', ?, ?, ?)`, projectID, nowMs, incidentID, title)
		return err
	})
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

// incidents loads the newest incidents matching the condition, then their
// updates and monitors with one query each. args holds the condition's
// values followed by the row limit.
func (s *Store) incidents(ctx context.Context, condition string, args ...any) ([]Incident, error) {
	const newestFirst = " order by i.started_at_ms desc, i.id desc"
	selected := "select i.id from incidents i where " + condition + newestFirst + " limit ?"

	rows, err := s.db.QueryContext(ctx, `
		select i.id, i.project_id, i.title, i.impact, i.status, i.published, i.started_at_ms, i.resolved_at_ms
		from incidents i where i.id in (`+selected+`)`+newestFirst, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var incidents []Incident
	position := make(map[int64]int)
	for rows.Next() {
		var in Incident
		if err := rows.Scan(&in.ID, &in.ProjectID, &in.Title, &in.Impact, &in.Status, &in.Published,
			&in.StartedAtMs, &in.ResolvedAtMs); err != nil {
			return nil, err
		}
		position[in.ID] = len(incidents)
		incidents = append(incidents, in)
	}
	if err := rows.Err(); err != nil || len(incidents) == 0 {
		return incidents, err
	}

	updates, err := s.db.QueryContext(ctx, `
		select incident_id, id, status, body, at_ms from incident_updates
		where incident_id in (`+selected+`) order by at_ms desc, id desc`, args...)
	if err != nil {
		return nil, err
	}
	defer updates.Close()
	for updates.Next() {
		var incidentID int64
		var u IncidentUpdate
		if err := updates.Scan(&incidentID, &u.ID, &u.Status, &u.Body, &u.AtMs); err != nil {
			return nil, err
		}
		in := &incidents[position[incidentID]]
		in.Updates = append(in.Updates, u)
	}
	if err := updates.Err(); err != nil {
		return nil, err
	}

	links, err := s.db.QueryContext(ctx, `
		select incident_id, monitor_id from incident_monitors
		where incident_id in (`+selected+`) order by monitor_id`, args...)
	if err != nil {
		return nil, err
	}
	defer links.Close()
	for links.Next() {
		var incidentID, monitorID int64
		if err := links.Scan(&incidentID, &monitorID); err != nil {
			return nil, err
		}
		in := &incidents[position[incidentID]]
		in.MonitorIDs = append(in.MonitorIDs, monitorID)
	}
	return incidents, links.Err()
}
