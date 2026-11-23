package store

import "context"

// IncidentCursor marks a position in the published incident timeline. A zero
// ID starts at the newest incident.
type IncidentCursor struct {
	StartedAtMs int64
	ID          int64
}

// PublishedIncidentSummary pairs a public incident's summary with its page.
type PublishedIncidentSummary struct {
	Incident
	ProjectName     string
	ProjectSlug     string
	ProjectTimezone string
}

// PublishedIncidentSummaries lists public incidents across all pages by start
// time and ID, newest first. A cursor returns only incidents older than it.
func (s *Store) PublishedIncidentSummaries(ctx context.Context, before IncidentCursor, limit int) ([]PublishedIncidentSummary, error) {
	query := `
		select ` + incidentSummaryColumns + `, p.name, p.slug, p.timezone
		from incidents i join projects p on p.id = i.project_id
		where i.published = 1`
	var args []any
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
	return incidents, rows.Err()
}
