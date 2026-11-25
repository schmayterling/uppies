package store

import "context"

// IncidentCursor marks a position in the published incident timeline. A zero
// ID starts at the newest incident.
type IncidentCursor struct {
	StartedAtMs int64
	ID          int64
}

// PublishedIncidentSummary pairs a public incident, with its updates and
// affected monitors, with its page.
type PublishedIncidentSummary struct {
	Incident
	ProjectName     string
	ProjectSlug     string
	ProjectTimezone string
}

const incidentStartsBetween = " and i.started_at_ms >= ? and i.started_at_ms < ?"

// PublishedIncidentSummaries lists public incidents across all pages by start
// time and ID, newest first. A cursor returns only incidents older than it.
func (s *Store) PublishedIncidentSummaries(ctx context.Context, before IncidentCursor, limit int) ([]PublishedIncidentSummary, error) {
	return s.publishedIncidentSummaries(ctx, before, limit, "")
}

// PublishedIncidentSummariesBetween limits the same timeline to incidents
// that started in [fromMs, toMs).
func (s *Store) PublishedIncidentSummariesBetween(ctx context.Context, before IncidentCursor, fromMs, toMs int64, limit int) ([]PublishedIncidentSummary, error) {
	return s.publishedIncidentSummaries(ctx, before, limit, incidentStartsBetween, fromMs, toMs)
}

func (s *Store) publishedIncidentSummaries(ctx context.Context, before IncidentCursor, limit int, condition string, args ...any) ([]PublishedIncidentSummary, error) {
	query := `
		select ` + incidentSummaryColumns + `, p.name, p.slug, p.timezone
		from incidents i join projects p on p.id = i.project_id
		where i.published = 1` + condition
	if before.ID != 0 {
		query += " and (i.started_at_ms < ? or (i.started_at_ms = ? and i.id < ?))"
		args = append(args, before.StartedAtMs, before.StartedAtMs, before.ID)
	}
	query += " order by i.started_at_ms desc, i.id desc limit ?"
	args = append(args, min(max(limit, 1), maxIncidentsPerPage))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var incidents []PublishedIncidentSummary
	for rows.Next() {
		var in PublishedIncidentSummary
		if err := scanIncident(rows, &in.Incident, &in.ProjectName, &in.ProjectSlug, &in.ProjectTimezone); err != nil {
			return nil, err
		}
		incidents = append(incidents, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	details := make([]*Incident, len(incidents))
	for i := range incidents {
		details[i] = &incidents[i].Incident
	}
	return incidents, s.loadIncidentDetails(ctx, details)
}

// VisitPublishedIncidentStartsBetween visits public incident starts in
// [fromMs, toMs) in no particular order, without loading incident bodies.
func (s *Store) VisitPublishedIncidentStartsBetween(ctx context.Context, fromMs, toMs int64, visit func(startedAtMs int64, impact string)) error {
	// The project filter lets the query use the project and start time index.
	rows, err := s.db.QueryContext(ctx, `
		select i.started_at_ms, i.impact
		from incidents i
		where i.published = 1 and i.project_id in (select id from projects)`+incidentStartsBetween, fromMs, toMs)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var startedAtMs int64
		var impact string
		if err := rows.Scan(&startedAtMs, &impact); err != nil {
			return err
		}
		visit(startedAtMs, impact)
	}
	return rows.Err()
}
