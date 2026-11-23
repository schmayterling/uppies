package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

const (
	indexIncidentPreviewLimit = 5
	indexIncidentArchiveLimit = 20
)

type indexIncident struct {
	store.PublishedIncidentSummary
	ProjectYear int
}

type indexIncidentHistory struct {
	Incidents []indexIncident
	Older     string
}

type indexIncidentsArchivePage struct {
	Chrome          chrome
	IncidentHistory indexIncidentHistory
	HasCursor       bool
}

func (s *Server) loadIndexIncidentHistory(r *http.Request) (indexIncidentHistory, error) {
	return s.loadIndexIncidentPage(r, store.IncidentCursor{}, indexIncidentPreviewLimit)
}

func (s *Server) loadIndexIncidentPage(r *http.Request, before store.IncidentCursor, limit int) (indexIncidentHistory, error) {
	var history indexIncidentHistory
	incidents, err := s.store.PublishedIncidentSummaries(r.Context(), before, limit+1)
	if err != nil {
		return history, err
	}
	if len(incidents) > limit {
		last := incidents[limit-1]
		history.Older = indexIncidentCursorValue(store.IncidentCursor{StartedAtMs: last.StartedAtMs, ID: last.ID})
		incidents = incidents[:limit]
	}
	for _, in := range incidents {
		zone, _ := pageZone(store.Project{Timezone: in.ProjectTimezone}, "")
		year := time.UnixMilli(in.StartedAtMs).In(zone).Year()
		history.Incidents = append(history.Incidents, indexIncident{PublishedIncidentSummary: in, ProjectYear: year})
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

	before := parseIndexIncidentCursor(r.URL.Query().Get("before"))
	history, err := s.loadIndexIncidentPage(r, before, indexIncidentArchiveLimit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := indexIncidentsArchivePage{
		Chrome: chrome{
			Title: instance.Name + " incident history", Unbranded: true,
			Favicon:     s.faviconAddress(ctx),
			Description: "Incident history across the status pages for " + instance.Name + ".",
		},
		IncidentHistory: history,
		HasCursor:       before.ID != 0,
	}
	canonical := publicOrigin(r, instance.PublicURL)
	canonical.Path = "/incidents"
	if page.HasCursor {
		canonical.RawQuery = "before=" + indexIncidentCursorValue(before)
	}
	page.Chrome.setPublicMetadata(canonical, instance.Name, false)
	s.render(w, r, http.StatusOK, "index_incidents_archive.html", page)
}
