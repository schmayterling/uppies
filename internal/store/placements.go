package store

import (
	"context"
	"database/sql"
)

// Placement puts a monitor on a status page: where in the page's list it
// stands and how it is drawn there. A monitor can be
// on several pages and look different on each.
type Placement struct {
	ProjectID int64
	MonitorID int64
	// Position orders the page's monitors, smallest first.
	Position int
	// Display is how the page draws the monitor: one of MonitorDisplays.
	Display string
	// GraphFill is how the monitor's graphs are filled on this page: one of
	// GraphFills, or empty to follow the page.
	GraphFill string
}

// PlacedMonitor is a monitor together with its place on one status page.
type PlacedMonitor struct {
	Monitor
	Placement
}

const placedColumns = `p.project_id, p.monitor_id, p.position, p.display, p.graph_fill`

func (s *Store) placed(ctx context.Context, query string, args ...any) ([]PlacedMonitor, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var placed []PlacedMonitor
	for rows.Next() {
		var pm PlacedMonitor
		p, m := &pm.Placement, &pm.Monitor
		err := rows.Scan(&p.ProjectID, &p.MonitorID, &p.Position, &p.Display, &p.GraphFill,
			&m.ID, &m.Name, &m.Kind, &m.Target, &m.Keyword, &m.IntervalS, &m.TimeoutMs,
			&m.Retries, &m.RetryIntervalS, &m.RecoveryChecks, &m.Paused, &m.Tracker.Status, &m.StatusSinceMs,
			&m.Tracker.FailStreak, &m.Tracker.OKStreak, &m.Tracker.FailingSinceMs, &m.LastCheckAtMs,
			&m.LastLatencyMs, &m.LastError, &m.PushToken, &m.LastPushAtMs, &m.CertExpiresAtMs, &m.CertWarnedDays,
			&m.Category)
		if err != nil {
			return nil, err
		}
		placed = append(placed, pm)
	}
	return placed, rows.Err()
}

// monitorColumnsOf is monitorColumns with every column read from the alias m.
const monitorColumnsOf = `m.id, m.name, m.kind, m.target, m.keyword, m.interval_s, m.timeout_ms, m.retries,
	m.retry_interval_s, m.recovery_checks, m.paused, m.status, m.status_since_ms, m.fail_streak, m.ok_streak,
	m.failing_since_ms, m.last_check_at_ms, m.last_latency_ms, m.last_error, m.push_token, m.last_push_at_ms,
	m.cert_expires_at_ms, m.cert_warned_days, m.category`

// MonitorsByProject lists the monitors a status page shows, in the page's order.
func (s *Store) MonitorsByProject(ctx context.Context, projectID int64) ([]PlacedMonitor, error) {
	return s.placed(ctx, `
		select `+placedColumns+`, `+monitorColumnsOf+`
		from page_monitors p join monitors m on m.id = p.monitor_id
		where p.project_id = ? order by p.position, m.id`, projectID)
}

// PlacedMonitors lists every monitor on every status page, page by page in each page's order.
func (s *Store) PlacedMonitors(ctx context.Context) ([]PlacedMonitor, error) {
	return s.placed(ctx, `
		select `+placedColumns+`, `+monitorColumnsOf+`
		from page_monitors p join monitors m on m.id = p.monitor_id
		order by p.project_id, p.position, m.id`)
}

// PlaceMonitor adds a monitor to the end of a status page, drawn as bars. A
// monitor that is on the page already stays where it is.
func (s *Store) PlaceMonitor(ctx context.Context, projectID, monitorID int64) error {
	_, err := s.db.ExecContext(ctx, `
		insert into page_monitors (project_id, monitor_id, position)
		values (?1, ?2, (select coalesce(max(position), 0) + 1 from page_monitors where project_id = ?1))
		on conflict (project_id, monitor_id) do nothing`, projectID, monitorID)
	return err
}

// SetPlacements replaces the list of monitors a status page shows. The order
// of the list becomes the order on the page.
func (s *Store) SetPlacements(ctx context.Context, projectID int64, placements []Placement) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "delete from page_monitors where project_id = ?", projectID); err != nil {
			return err
		}
		insert, err := tx.PrepareContext(ctx, `
			insert into page_monitors (project_id, monitor_id, position, display, graph_fill)
			values (?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for i, p := range placements {
			if _, err := insert.ExecContext(ctx, projectID, p.MonitorID, i+1, displayOrDefault(p.Display), p.GraphFill); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetProjectOrder puts the status pages in the order of ids. A page that is
// not named keeps its place behind the named ones.
func (s *Store) SetProjectOrder(ctx context.Context, ids []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "update projects set position = position + ?", len(ids)); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, "update projects set position = ? where id = ?", i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Categories lists the categories a monitor can be put in, in their order.
func (s *Store) Categories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "select name from categories order by position, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// CreateCategory adds a category at the end of the list. One that exists already stays as it is.
func (s *Store) CreateCategory(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `
		insert into categories (name, position) values (?, (select coalesce(max(position), 0) + 1 from categories))
		on conflict (name) do nothing`, name)
	return err
}

// DeleteCategory removes a category. Its monitors stay and have no category afterwards.
func (s *Store) DeleteCategory(ctx context.Context, name string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "update monitors set category = '' where category = ?", name); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "delete from categories where name = ?", name)
		return err
	})
}

// PageNames lists the names of the status pages that show a monitor, in the pages' order.
func (s *Store) PageNames(ctx context.Context, monitorID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		select pr.name from projects pr join page_monitors p on p.project_id = pr.id
		where p.monitor_id = ? order by pr.position, pr.id`, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
