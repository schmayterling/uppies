package web

import (
	"context"
	"time"
)

type indexIncidentCalendar struct {
	Year   int
	Months []string
	Weeks  [][]heatCell
	Total  int
}

func (s *Server) loadIndexCalendar(ctx context.Context, year int, now time.Time, zone *time.Location) (indexIncidentCalendar, error) {
	from := time.Date(year, time.January, 1, 0, 0, 0, 0, zone)
	to := from.AddDate(1, 0, 0)
	totals := make(incidentDayTotals)
	total := 0
	err := s.store.VisitPublishedIncidentStartsBetween(ctx, from.UnixMilli(), to.UnixMilli(), func(startedAtMs int64, impact string) {
		totals.add(startedAtMs, impact, zone)
		total++
	})
	if err != nil {
		return indexIncidentCalendar{}, err
	}
	return indexIncidentCalendar{
		Year:   year,
		Months: monthLabels(year, zone),
		Weeks:  incidentCalendarWeeks(totals, year, now.In(zone), zone),
		Total:  total,
	}, nil
}
