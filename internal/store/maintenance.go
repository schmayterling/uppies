package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// MaintenanceWindow rests the selected monitors of a status page, or all its
// monitors when Scoped is false.
type MaintenanceWindow struct {
	ID                 int64
	ProjectID          int64
	Title              string
	StartsAtMs         int64
	EndsAtMs           int64
	UntilFurtherNotice bool
	Scoped             bool
	MonitorIDs         []int64
}

func (w MaintenanceWindow) RunningAt(atMs int64) bool {
	return w.StartsAtMs <= atMs && (w.UntilFurtherNotice || w.EndsAtMs > atMs)
}

func (w MaintenanceWindow) storedEnd() int64 {
	if w.UntilFurtherNotice {
		// Keep the scheduled end constraint on old databases. The flag controls
		// an open-ended window until its real end is recorded.
		return w.StartsAtMs + 1
	}
	return w.EndsAtMs
}

// CreateMaintenanceWindow adds a window and returns its id.
func (s *Store) CreateMaintenanceWindow(ctx context.Context, w MaintenanceWindow, nowMs int64) (int64, error) {
	w.Scoped = len(w.MonitorIDs) > 0
	var id int64
	err := s.write(ctx, func(tx *transaction) error {
		err := tx.QueryRowContext(ctx, `
			insert into maintenance_windows (project_id, title, starts_at_ms, ends_at_ms, created_at_ms, scoped, until_further_notice)
			select id, ?, ?, ?, ?, ?, ? from projects where id = ? returning id`,
			w.Title, w.StartsAtMs, w.storedEnd(), nowMs, w.Scoped, w.UntilFurtherNotice, w.ProjectID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if w.Scoped {
			w.ID = id
			return replaceMaintenanceMonitorScope(ctx, tx, w)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func replaceMaintenanceMonitorScope(ctx context.Context, tx *transaction, w MaintenanceWindow) error {
	if err := requirePageMonitors(ctx, tx, w.ProjectID, w.MonitorIDs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from maintenance_window_monitors where window_id = ?", w.ID); err != nil {
		return err
	}
	for _, monitorID := range w.MonitorIDs {
		if _, err := tx.ExecContext(ctx,
			"insert into maintenance_window_monitors (window_id, monitor_id) values (?, ?)", w.ID, monitorID); err != nil {
			return err
		}
	}
	return nil
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

// ErrMaintenanceNotStarted means an end was requested before the window starts.
var ErrMaintenanceNotStarted = errors.New("maintenance window has not started")

// EndMaintenanceWindow records an early end and leaves the window's history.
func (s *Store) EndMaintenanceWindow(ctx context.Context, projectID, windowID, nowMs int64) error {
	return s.write(ctx, func(tx *transaction) error {
		var startsAtMs int64
		err := tx.QueryRowContext(ctx, "select starts_at_ms from maintenance_windows where project_id = ? and id = ?",
			projectID, windowID).Scan(&startsAtMs)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if startsAtMs > nowMs {
			return ErrMaintenanceNotStarted
		}
		end := max(nowMs, startsAtMs+1)
		_, err = tx.ExecContext(ctx, `
			update maintenance_windows set ends_at_ms = ?, until_further_notice = 0
			where project_id = ? and id = ? and (until_further_notice = 1 or ends_at_ms > ?)`,
			end, projectID, windowID, end)
		return err
	})
}

const windowColumns = "id, project_id, title, starts_at_ms, ends_at_ms, until_further_notice, scoped"
const maintenanceByPageID = "select " + windowColumns + " from maintenance_windows where project_id = ? and id = ?"

func (s *Store) MaintenanceWindow(ctx context.Context, projectID, windowID int64) (MaintenanceWindow, error) {
	return readMaintenanceWindow(ctx, s.db, projectID, windowID)
}

func readMaintenanceWindow(ctx context.Context, q queries, projectID, windowID int64) (MaintenanceWindow, error) {
	windows, err := readMaintenanceWindows(ctx, q, maintenanceByPageID, projectID, windowID)
	if err != nil {
		return MaintenanceWindow{}, err
	}
	if len(windows) == 0 {
		return MaintenanceWindow{}, ErrNotFound
	}
	return windows[0], nil
}

func (s *Store) EditMaintenanceWindow(ctx context.Context, projectID, windowID int64, replaceScope bool, edit func(*MaintenanceWindow) error) error {
	return s.write(ctx, func(tx *transaction) error {
		window, err := readMaintenanceWindow(ctx, tx.queries, projectID, windowID)
		if err != nil {
			return err
		}
		if err := edit(&window); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			update maintenance_windows set title = ?, starts_at_ms = ?, ends_at_ms = ?, until_further_notice = ?, scoped = ?
			where project_id = ? and id = ?`,
			window.Title, window.StartsAtMs, window.storedEnd(), window.UntilFurtherNotice, window.Scoped, projectID, windowID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		if replaceScope {
			return replaceMaintenanceMonitorScope(ctx, tx, window)
		}
		return nil
	})
}

// MaintenanceWindows lists a page's running and upcoming windows, soonest first.
func (s *Store) MaintenanceWindows(ctx context.Context, projectID, sinceMs int64) ([]MaintenanceWindow, error) {
	return readMaintenanceWindows(ctx, s.db, "select "+windowColumns+` from maintenance_windows
		where project_id = ? and (until_further_notice = 1 or ends_at_ms > ?) order by starts_at_ms, id limit 100`, projectID, sinceMs)
}

func (s *Store) MaintenanceWindowsAfter(ctx context.Context, projectID, after int64, limit int) ([]MaintenanceWindow, error) {
	return readMaintenanceWindows(ctx, s.db, "select "+windowColumns+` from maintenance_windows
		where project_id = ? and id > ? order by id limit ?`, projectID, after, limit)
}

func (s *Store) MaintenanceWindowsBefore(ctx context.Context, projectID, before int64, limit int) ([]MaintenanceWindow, error) {
	return readMaintenanceWindows(ctx, s.db, "select "+windowColumns+` from maintenance_windows
		where project_id = ? and (? = 0 or id < ?) order by id desc limit ?`, projectID, before, before, limit)
}

func readMaintenanceWindows(ctx context.Context, q queries, query string, args ...any) ([]MaintenanceWindow, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var windows []MaintenanceWindow
	for rows.Next() {
		var w MaintenanceWindow
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Title, &w.StartsAtMs, &w.EndsAtMs, &w.UntilFurtherNotice, &w.Scoped); err != nil {
			return nil, err
		}
		windows = append(windows, w)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}

	var scopedIDs []any
	scoped := make(map[int64]*MaintenanceWindow)
	for i := range windows {
		window := &windows[i]
		if window.Scoped {
			scopedIDs = append(scopedIDs, window.ID)
			scoped[window.ID] = window
		}
	}
	if len(scopedIDs) == 0 {
		return windows, nil
	}
	params := strings.Repeat("?, ", len(scopedIDs)-1) + "?"
	monitors, err := q.QueryContext(ctx, `
		select window_id, monitor_id from maintenance_window_monitors
		where window_id in (`+params+`) order by window_id, monitor_id`, scopedIDs...)
	if err != nil {
		return nil, err
	}
	defer monitors.Close()
	for monitors.Next() {
		var windowID, monitorID int64
		if err := monitors.Scan(&windowID, &monitorID); err != nil {
			return nil, err
		}
		window := scoped[windowID]
		window.MonitorIDs = append(window.MonitorIDs, monitorID)
	}
	return windows, monitors.Err()
}

// InMaintenance reports whether an active window covers a monitor on one of its pages.
func (s *Store) InMaintenance(ctx context.Context, monitorID, atMs int64) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from maintenance_windows w
			join page_monitors p on p.project_id = w.project_id
			where p.monitor_id = ? and w.starts_at_ms <= ? and (w.until_further_notice = 1 or w.ends_at_ms > ?)
				and (w.scoped = 0 or exists (select 1 from maintenance_window_monitors m
					where m.window_id = w.id and m.monitor_id = p.monitor_id)))`, monitorID, atMs, atMs).Scan(&active)
	return active, err
}

// MaintenanceUpdate is a dated message on a maintenance window.
type MaintenanceUpdate struct {
	ID   int64
	Body string
	AtMs int64
}

// AddMaintenanceUpdate adds an update only when the window belongs to the page.
func (s *Store) AddMaintenanceUpdate(ctx context.Context, projectID, windowID int64, body string, nowMs int64) error {
	res, err := s.db.ExecContext(ctx, `
		insert into maintenance_updates (window_id, body, at_ms)
		select id, ?, ? from maintenance_windows where project_id = ? and id = ?`,
		body, nowMs, projectID, windowID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

func (s *Store) MaintenanceUpdatesAfter(ctx context.Context, windowID, after int64, limit int) ([]MaintenanceUpdate, error) {
	return s.maintenanceUpdates(ctx, `
		select id, body, at_ms from maintenance_updates
		where window_id = ? and id > ? order by id limit ?`, windowID, after, limit)
}

func (s *Store) MaintenanceUpdatesBefore(ctx context.Context, windowID, before int64, limit int) ([]MaintenanceUpdate, error) {
	return s.maintenanceUpdates(ctx, `
		select id, body, at_ms from maintenance_updates
		where window_id = ? and (? = 0 or id < ?) order by id desc limit ?`, windowID, before, before, limit)
}

func (s *Store) maintenanceUpdates(ctx context.Context, query string, args ...any) ([]MaintenanceUpdate, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []MaintenanceUpdate
	for rows.Next() {
		var update MaintenanceUpdate
		if err := rows.Scan(&update.ID, &update.Body, &update.AtMs); err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}

// RecentMaintenanceUpdates returns up to 20 updates per public notice.
func (s *Store) RecentMaintenanceUpdates(ctx context.Context, windows []MaintenanceWindow) (map[int64][]MaintenanceUpdate, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	ids := make([]any, len(windows))
	for i, window := range windows {
		ids[i] = window.ID
	}
	params := strings.Repeat("?, ", len(ids)-1) + "?"
	// Project from the table so TursoPG keeps the bigint types of IDs and timestamps.
	rows, err := s.db.QueryContext(ctx, `
		select window_id, id, body, at_ms from maintenance_updates
		where id in (
			select id from (
				select id, row_number() over (partition by window_id order by id desc) as update_rank
				from maintenance_updates where window_id in (`+params+`)
			) recent where update_rank <= 20
		) order by window_id, id desc`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	updates := make(map[int64][]MaintenanceUpdate)
	for rows.Next() {
		var windowID int64
		var update MaintenanceUpdate
		if err := rows.Scan(&windowID, &update.ID, &update.Body, &update.AtMs); err != nil {
			return nil, err
		}
		updates[windowID] = append(updates[windowID], update)
	}
	return updates, rows.Err()
}
