package store

import "context"

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
		values (?, ?, ?, ?, ?)`, w.ProjectID, w.Title, w.StartsAtMs, w.EndsAtMs, nowMs)
	if err != nil {
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

// MaintenanceWindows lists a project's windows that end after sinceMs: the
// running one and the upcoming ones, soonest first.
func (s *Store) MaintenanceWindows(ctx context.Context, projectID, sinceMs int64) ([]MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, project_id, title, starts_at_ms, ends_at_ms from maintenance_windows
		where project_id = ? and ends_at_ms > ? order by starts_at_ms, id limit 100`, projectID, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windows []MaintenanceWindow
	for rows.Next() {
		var w MaintenanceWindow
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Title, &w.StartsAtMs, &w.EndsAtMs); err != nil {
			return nil, err
		}
		windows = append(windows, w)
	}
	return windows, rows.Err()
}

// InMaintenance reports whether a window of the project covers atMs.
func (s *Store) InMaintenance(ctx context.Context, projectID, atMs int64) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from maintenance_windows
			where project_id = ? and starts_at_ms <= ? and ends_at_ms > ?)`, projectID, atMs, atMs).Scan(&active)
	return active, err
}
