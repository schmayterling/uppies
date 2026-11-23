package store

import (
	"context"
	"errors"
)

// Placement puts a monitor on a status page: where in the page's list it
// stands, in which of the page's categories, and how it is drawn there. A monitor can be
// on several pages and look different on each.
type Placement struct {
	ProjectID int64
	MonitorID int64
	// Position orders the page's monitors, smallest first.
	Position int
	// Category is the page's category the monitor is listed under. Empty for none.
	Category string
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

const placedColumns = `p.project_id, p.monitor_id, p.position, p.category, p.display, p.graph_fill`

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
		err := rows.Scan(&p.ProjectID, &p.MonitorID, &p.Position, &p.Category, &p.Display, &p.GraphFill,
			&m.ID, &m.Name, &m.Kind, &m.Target, &m.Keyword, &m.IntervalS, &m.TimeoutMs,
			&m.Retries, &m.RetryIntervalS, &m.RecoveryChecks, &m.Paused, &m.Tracker.Status, &m.StatusSinceMs,
			&m.Tracker.FailStreak, &m.Tracker.OKStreak, &m.Tracker.FailingSinceMs, &m.LastCheckAtMs,
			&m.LastLatencyMs, &m.LastError, &m.PushToken, &m.LastPushAtMs, &m.CertExpiresAtMs, &m.CertWarnedDays, &m.Tags,
			&m.AutoCreateIncident, &m.AutoResolveIncident)
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
	m.cert_expires_at_ms, m.cert_warned_days, m.tags, m.auto_create_incident, m.auto_resolve_incident`

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
	conflict := "on conflict (project_id, monitor_id) do nothing"
	if s.Backend() == MariaDB {
		conflict = "on duplicate key update position = page_monitors.position"
	}
	_, err := s.db.ExecContext(ctx, `
		insert into page_monitors (project_id, monitor_id, position)
		values (?, ?, (select coalesce(max(position), 0) + 1 from page_monitors where project_id = ?)) `+conflict,
		projectID, monitorID, projectID)
	return err
}

// SetPlacements replaces the list of monitors a status page shows. The order
// of the list becomes the order on the page. The page's categories stay, and
// a category that a placement names for the first time is added at the end.
func (s *Store) SetPlacements(ctx context.Context, projectID int64, placements []Placement) error {
	categories, err := s.PageCategories(ctx, projectID)
	if err != nil {
		return err
	}
	return s.SetPageLayout(ctx, projectID, categories, placements)
}

// SetPageLayout replaces what a status page shows: its categories in their
// order, and its monitors in theirs. A category that a placement names and
// the list leaves out is added at the end.
func (s *Store) SetPageLayout(ctx context.Context, projectID int64, categories []string, placements []Placement) error {
	return s.write(ctx, func(tx *transaction) error {
		if err := requireProject(ctx, tx, projectID); err != nil {
			return err
		}
		for _, table := range []string{"page_monitors", "page_categories"} {
			// The table names are the two literals above, not input.
			if _, err := tx.ExecContext(ctx, "delete from "+table+" where project_id = ?", projectID); err != nil {
				return err
			}
		}
		names := append([]string(nil), categories...)
		for _, p := range placements {
			names = append(names, p.Category)
		}
		categoryConflict := "on conflict (project_id, name) do nothing"
		if s.Backend() == MariaDB {
			categoryConflict = "on duplicate key update position = page_categories.position"
		}
		position := 0
		for _, name := range names {
			if name == "" {
				continue
			}
			position++
			if _, err := tx.ExecContext(ctx,
				"insert into page_categories (project_id, name, position) values (?, ?, ?) "+categoryConflict,
				projectID, name, position); err != nil {
				return err
			}
		}
		for i, p := range placements {
			_, err := tx.ExecContext(ctx, `
				insert into page_monitors (project_id, monitor_id, position, category, display, graph_fill)
				values (?, ?, ?, ?, ?, ?)`,
				projectID, p.MonitorID, i+1, p.Category, displayOrDefault(p.Display), p.GraphFill)
			if isForeignKey(err) {
				return ErrUnknownMonitor
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ErrUnknownMonitor means a monitor reference did not match an available monitor.
var ErrUnknownMonitor = errors.New("unknown monitor")

func requireProject(ctx context.Context, tx *transaction, projectID int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "select exists (select 1 from projects where id = ?)", projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// PageLayout reads the ordered categories and placements in one snapshot.
func (s *Store) PageLayout(ctx context.Context, projectID int64) ([]string, []Placement, error) {
	rows, err := s.db.QueryContext(ctx, `
		select 0 as kind, 0 as position, '' as name, 0 as monitor_id, '' as display, '' as graph_fill from projects where id = ?
		union all
		select 1, position, name, 0, '', '' from page_categories where project_id = ?
		union all
		select 2, position, category, monitor_id, display, graph_fill from page_monitors where project_id = ?
		order by kind, position, name, monitor_id`, projectID, projectID, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrNotFound
	}
	var categories []string
	var placements []Placement
	for rows.Next() {
		var kind int
		p := Placement{ProjectID: projectID}
		if err := rows.Scan(&kind, &p.Position, &p.Category, &p.MonitorID, &p.Display, &p.GraphFill); err != nil {
			return nil, nil, err
		}
		if kind == 1 {
			categories = append(categories, p.Category)
		} else {
			placements = append(placements, p)
		}
	}
	return categories, placements, rows.Err()
}

// PageCategories lists a status page's categories in their order.
func (s *Store) PageCategories(ctx context.Context, projectID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "select name from page_categories where project_id = ? order by position, name", projectID)
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

// SetProjectOrder puts the status pages in the order of ids. A page that is
// not named keeps its place behind the named ones.
func (s *Store) SetProjectOrder(ctx context.Context, ids []int64) error {
	return s.write(ctx, func(tx *transaction) error {
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
