package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

const (
	indexIncidentPreviewLimit = 5
	indexIncidentArchiveLimit = 20
)

type indexIncidentHistory struct {
	Days  []dayGroup
	Zone  *time.Location
	Older string
}

type indexIncidentsArchivePage struct {
	indexSite
	IncidentHistory indexIncidentHistory
	Calendar        indexIncidentCalendar
	PrevYear        int
	NextYear        int
	Filtered        bool
	HasCursor       bool
}

func (s *Server) loadIndexIncidentPage(ctx context.Context, before store.IncidentCursor, year, limit int, now time.Time, zone *time.Location) (indexIncidentHistory, error) {
	history := indexIncidentHistory{Zone: zone}
	var incidents []store.PublishedIncidentSummary
	var err error
	if year == 0 {
		incidents, err = s.store.PublishedIncidentSummaries(ctx, before, limit+1)
	} else {
		from := time.Date(year, time.January, 1, 0, 0, 0, 0, zone)
		incidents, err = s.store.PublishedIncidentSummariesBetween(ctx, before, from.UnixMilli(), from.AddDate(1, 0, 0).UnixMilli(), limit+1)
	}
	if err != nil {
		return history, err
	}
	if len(incidents) > limit {
		last := incidents[limit-1]
		history.Older = indexIncidentCursorValue(store.IncidentCursor{StartedAtMs: last.StartedAtMs, ID: last.ID})
		incidents = incidents[:limit]
	}
	if len(incidents) == 0 {
		return history, nil
	}
	// An incident names the monitors its own page shows, as on that page.
	placed, err := s.store.PlacedMonitors(ctx)
	if err != nil {
		return history, err
	}
	publicNames := make(map[int64]map[int64]string)
	for _, m := range placed {
		if publicNames[m.ProjectID] == nil {
			publicNames[m.ProjectID] = make(map[int64]string)
		}
		publicNames[m.ProjectID][m.ID] = m.Name
	}
	today := noonOf(now.In(zone))
	for _, in := range incidents {
		started := time.UnixMilli(in.StartedAtMs).In(zone)
		date := dayLabel(started)
		if n := len(history.Days); n == 0 || history.Days[n-1].Date != date {
			// Both are noon, so rounding absorbs the hour a clock change adds or removes.
			daysAgo := int(math.Round(today.Sub(noonOf(started)).Hours() / 24))
			history.Days = append(history.Days, dayGroup{Date: date, Ago: agoLabel(daysAgo)})
		}
		// The link opens the year of the page's own history, which its timezone bounds.
		projectYear := started.Year()
		if projectZone, ok := loadZone(in.ProjectTimezone); ok {
			projectYear = started.In(projectZone).Year()
		}
		day := &history.Days[len(history.Days)-1]
		view := newIncidentView(in.Incident, publicNames[in.ProjectID])
		view.ProjectName = in.ProjectName
		view.ProjectHistory = fmt.Sprintf("/s/%s/history?year=%d", in.ProjectSlug, projectYear)
		day.Incidents = append(day.Incidents, view)
		day.Level = max(day.Level, impactLevels[in.Impact])
	}
	return history, nil
}

func parseIndexIncidentCursor(raw string) store.IncidentCursor {
	startedAt, id, ok := strings.Cut(raw, ".")
	if !ok {
		return store.IncidentCursor{}
	}
	startedAtMs, timeErr := strconv.ParseInt(startedAt, 10, 64)
	incidentID, idErr := strconv.ParseInt(id, 10, 64)
	if timeErr != nil || idErr != nil || incidentID <= 0 {
		return store.IncidentCursor{}
	}
	return store.IncidentCursor{StartedAtMs: startedAtMs, ID: incidentID}
}

func indexIncidentCursorValue(cursor store.IncidentCursor) string {
	return fmt.Sprintf("%d.%d", cursor.StartedAtMs, cursor.ID)
}

func (s *Server) handleIndexIncidents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	instance, err := s.store.Instance(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !instance.PublicList {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	exists, err := s.store.HasUsers(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}

	now := s.now().UTC()
	query := r.URL.Query()
	before := parseIndexIncidentCursor(query.Get("before"))
	year, _ := strconv.Atoi(query.Get("year"))
	if year < 2000 || year > now.Year() {
		year = 0
	}
	calendarYear := year
	if calendarYear == 0 {
		calendarYear = now.Year()
	}
	choice := visitorZone(r, instance)
	history, err := s.loadIndexIncidentPage(ctx, before, year, indexIncidentArchiveLimit, now, choice.Zone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	calendar, err := s.loadIndexCalendar(ctx, calendarYear, now, choice.Zone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	projects, err := s.store.Projects(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.indexRows(ctx, projects, now.UnixMilli(), false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state, _ := indexSummary(rows)
	page := indexIncidentsArchivePage{
		indexSite: s.newIndexSite(ctx, instance, state, instance.Name+" incident history",
			"Incident history across the status pages for "+instance.Name+"."),
		IncidentHistory: history,
		Calendar:        calendar,
		Filtered:        year != 0,
		HasCursor:       before.ID != 0,
	}
	page.Chrome.Zone = choice.Name
	if calendarYear > 2000 {
		page.PrevYear = calendarYear - 1
	}
	if calendarYear < now.Year() {
		page.NextYear = calendarYear + 1
	}
	canonical := publicOrigin(r, instance.PublicURL)
	canonical.Path = "/incidents"
	params := make(url.Values)
	if page.Filtered {
		params.Set("year", strconv.Itoa(year))
		page.Chrome.Title += " " + strconv.Itoa(year)
	}
	if page.HasCursor {
		params.Set("before", indexIncidentCursorValue(before))
	}
	canonical.RawQuery = params.Encode()
	page.Chrome.setPublicMetadata(canonical, instance.Name, false)
	s.render(w, r, http.StatusOK, "index_incidents_archive.html", page)
}
