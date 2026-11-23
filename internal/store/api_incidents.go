package store

import (
	"context"
	"database/sql"
)

// IncidentEdit holds the fields that can change without a timeline update.
// Nil fields keep their existing values.
type IncidentEdit struct {
	Title, Impact *string
	MonitorIDs    *[]int64
}

func requirePageIncident(ctx context.Context, tx *transaction, projectID, incidentID int64) error {
	var exists bool
	err := tx.QueryRowContext(ctx, "select exists (select 1 from incidents where project_id = ? and id = ?)",
		projectID, incidentID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func requirePageMonitors(ctx context.Context, tx *transaction, projectID int64, ids []int64) error {
	for _, id := range ids {
		var exists bool
		err := tx.QueryRowContext(ctx, "select exists (select 1 from page_monitors where project_id = ? and monitor_id = ?)",
			projectID, id).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return ErrUnknownMonitor
		}
	}
	return nil
}

// CreatePageIncident rejects monitor IDs that the incident's page does not show.
func (s *Store) CreatePageIncident(ctx context.Context, in Incident, firstUpdate string, nowMs int64) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *transaction) error {
		if err := requireProject(ctx, tx, in.ProjectID); err != nil {
			return err
		}
		if err := requirePageMonitors(ctx, tx, in.ProjectID, in.MonitorIDs); err != nil {
			return err
		}
		var err error
		id, err = insertIncident(ctx, tx, in, firstUpdate, nowMs, sql.NullInt64{})
		return err
	})
	return id, err
}

func (s *Store) EditPageIncident(ctx context.Context, projectID, incidentID int64, edit IncidentEdit) error {
	return s.write(ctx, func(tx *transaction) error {
		res, err := tx.ExecContext(ctx, `
			update incidents set title = coalesce(?, title), impact = coalesce(?, impact)
			where project_id = ? and id = ?`, edit.Title, edit.Impact, projectID, incidentID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		if edit.MonitorIDs == nil {
			return nil
		}
		if err := requirePageMonitors(ctx, tx, projectID, *edit.MonitorIDs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "delete from incident_monitors where incident_id = ?", incidentID); err != nil {
			return err
		}
		return insertIncidentMonitors(ctx, tx, incidentID, projectID, *edit.MonitorIDs)
	})
}

func (s *Store) AddPageIncidentUpdate(ctx context.Context, projectID, incidentID int64, status, body string, nowMs int64) error {
	return s.write(ctx, func(tx *transaction) error {
		if err := requirePageIncident(ctx, tx, projectID, incidentID); err != nil {
			return err
		}
		return addIncidentUpdate(ctx, tx, incidentID, status, body, nowMs)
	})
}

func (s *Store) PublishPageIncident(ctx context.Context, projectID, incidentID, nowMs int64) error {
	return s.write(ctx, func(tx *transaction) error {
		if err := requirePageIncident(ctx, tx, projectID, incidentID); err != nil {
			return err
		}
		return publishIncident(ctx, tx, incidentID, nowMs)
	})
}

func (s *Store) DeletePageIncident(ctx context.Context, projectID, incidentID int64) error {
	res, err := s.db.ExecContext(ctx, "delete from incidents where project_id = ? and id = ?", projectID, incidentID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// PageIncident returns the incident and affected monitors in one snapshot.
func (s *Store) PageIncident(ctx context.Context, projectID, incidentID int64) (Incident, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+incidentSummaryColumns+`, im.monitor_id
		from incidents i left join incident_monitors im on im.incident_id = i.id
		where i.project_id = ? and i.id = ? order by im.monitor_id`, projectID, incidentID)
	if err != nil {
		return Incident{}, err
	}
	defer rows.Close()
	var in Incident
	for rows.Next() {
		var monitorID sql.NullInt64
		if err := scanIncident(rows, &in, &monitorID); err != nil {
			return Incident{}, err
		}
		if monitorID.Valid {
			in.MonitorIDs = append(in.MonitorIDs, monitorID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return Incident{}, err
	}
	if in.ID == 0 {
		return Incident{}, ErrNotFound
	}
	return in, nil
}

// PageIncidentsAfter lists incident summaries in ID order, including drafts.
func (s *Store) PageIncidentsAfter(ctx context.Context, projectID, after int64, limit int) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, "select "+incidentSummaryColumns+`
		from incidents i where i.project_id = ? and i.id > ? order by i.id limit ?`, projectID, after, limit)
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
	return incidents, rows.Err()
}

func (s *Store) PageIncidentUpdatesAfter(ctx context.Context, projectID, incidentID, after int64, limit int) ([]IncidentUpdate, error) {
	// An existing incident with no further updates returns one NULL row.
	rows, err := s.db.QueryContext(ctx, `
		select u.id, u.status, u.body, u.at_ms from incidents i
		left join incident_updates u on u.incident_id = i.id and u.id > ?
		where i.project_id = ? and i.id = ? order by u.id limit ?`, after, projectID, incidentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	var updates []IncidentUpdate
	for {
		var id, at sql.NullInt64
		var status, body sql.NullString
		if err := rows.Scan(&id, &status, &body, &at); err != nil {
			return nil, err
		}
		if id.Valid {
			updates = append(updates, IncidentUpdate{ID: id.Int64, Status: status.String, Body: body.String, AtMs: at.Int64})
		}
		if !rows.Next() {
			return updates, rows.Err()
		}
	}
}
