package store

import (
	"context"
	"database/sql"
	"errors"
)

// MaintenanceWindow is a planned period in which a project's monitors are
// not checked, so that expected downtime neither alerts nor counts.
type MaintenanceWindow struct {
	ID         int64
	ProjectID  int64
	Title      string
	StartsAtMs int64
	EndsAtMs   int64
}

// CreateMaintenanceWindow adds a window and returns its id.
func (s *Store) CreateMaintenanceWindow(ctx context.Context, w MaintenanceWindow, nowMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		insert into maintenance_windows (project_id, title, starts_at_ms, ends_at_ms, created_at_ms)
		select id, ?2, ?3, ?4, ?5 from projects where id = ?1`, w.ProjectID, w.Title, w.StartsAtMs, w.EndsAtMs, nowMs)
	if err != nil {
		return 0, err
	}
	if err := requireOneRow(res); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteMaintenanceWindow removes one window of a project.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, projectID, windowID int64) error {
	res, err := s.db.ExecContext(ctx,
		"delete from maintenance_windows where id = ? and project_id = ?", windowID, projectID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

const windowColumns = "id, project_id, title, starts_at_ms, ends_at_ms"
const maintenanceByPageID = "select " + windowColumns + " from maintenance_windows where project_id = ? and id = ?"

func scanMaintenanceWindow(row rowScanner) (MaintenanceWindow, error) {
	var w MaintenanceWindow
	err := row.Scan(&w.ID, &w.ProjectID, &w.Title, &w.StartsAtMs, &w.EndsAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceWindow{}, ErrNotFound
	}
	return w, err
}

func (s *Store) MaintenanceWindow(ctx context.Context, projectID, windowID int64) (MaintenanceWindow, error) {
	return scanMaintenanceWindow(s.db.QueryRowContext(ctx, maintenanceByPageID, projectID, windowID))
}

func (s *Store) EditMaintenanceWindow(ctx context.Context, projectID, windowID int64, edit func(*MaintenanceWindow) error) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		window, err := scanMaintenanceWindow(tx.QueryRowContext(ctx, maintenanceByPageID, projectID, windowID))
		if err != nil {
			return err
		}
		if err := edit(&window); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			update maintenance_windows set title = ?, starts_at_ms = ?, ends_at_ms = ?
			where project_id = ? and id = ?`, window.Title, window.StartsAtMs, window.EndsAtMs, projectID, windowID)
		if err != nil {
			return err
		}
		return requireOneRow(res)
	})
}

// MaintenanceWindows lists a project's windows that end after sinceMs: the
// running one and the upcoming ones, soonest first.
func (s *Store) MaintenanceWindows(ctx context.Context, projectID, sinceMs int64) ([]MaintenanceWindow, error) {
	return s.maintenanceWindows(ctx, "select "+windowColumns+` from maintenance_windows
		where project_id = ? and ends_at_ms > ? order by starts_at_ms, id limit 100`, projectID, sinceMs)
}

func (s *Store) MaintenanceWindowsAfter(ctx context.Context, projectID, after int64, limit int) ([]MaintenanceWindow, error) {
	return s.maintenanceWindows(ctx, "select "+windowColumns+` from maintenance_windows
		where project_id = ? and id > ? order by id limit ?`, projectID, after, limit)
}

func (s *Store) maintenanceWindows(ctx context.Context, query string, args ...any) ([]MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windows []MaintenanceWindow
	for rows.Next() {
		w, err := scanMaintenanceWindow(rows)
		if err != nil {
			return nil, err
		}
		windows = append(windows, w)
	}
	return windows, rows.Err()
}

// InMaintenance reports whether a window of any status page that shows the monitor covers atMs.
func (s *Store) InMaintenance(ctx context.Context, monitorID, atMs int64) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from maintenance_windows w
			join page_monitors p on p.project_id = w.project_id
			where p.monitor_id = ? and w.starts_at_ms <= ? and w.ends_at_ms > ?)`, monitorID, atMs, atMs).Scan(&active)
	return active, err
}
