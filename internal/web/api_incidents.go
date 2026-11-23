package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

type apiIncident struct {
	ID         string     `json:"id"`
	PageID     string     `json:"status_page_id"`
	Title      string     `json:"title"`
	Impact     string     `json:"impact"`
	Status     string     `json:"status"`
	Published  bool       `json:"published"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

func (in apiIncident) cursor() string { return in.ID }

func incidentJSON(in store.Incident) apiIncident {
	var resolved *time.Time
	if in.ResolvedAtMs.Valid {
		at := time.UnixMilli(in.ResolvedAtMs.Int64).UTC()
		resolved = &at
	}
	return apiIncident{
		ID: strconv.FormatInt(in.ID, 10), PageID: strconv.FormatInt(in.ProjectID, 10),
		Title: in.Title, Impact: in.Impact, Status: in.Status, Published: in.Published,
		StartedAt: time.UnixMilli(in.StartedAtMs).UTC(), ResolvedAt: resolved,
	}
}

type apiIncidentNote struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
}

func (u apiIncidentNote) cursor() string { return u.ID }

func incidentNoteJSON(u store.IncidentUpdate) apiIncidentNote {
	return apiIncidentNote{strconv.FormatInt(u.ID, 10), u.Status, u.Body, time.UnixMilli(u.AtMs).UTC()}
}

func (s *Server) apiListIncidents(w http.ResponseWriter, r *http.Request) {
	pageID, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if _, err := s.store.ProjectByID(r.Context(), pageID); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	query := func(ctx context.Context, after int64, limit int) ([]store.Incident, error) {
		return s.store.PageIncidentsAfter(ctx, pageID, after, limit)
	}
	apiList(s, w, r, query, incidentJSON)
}

func (s *Server) apiReadIncident(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "incident")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	in, err := s.store.PageIncident(r.Context(), pageID, id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	monitors := make([]string, len(in.MonitorIDs))
	for i, monitorID := range in.MonitorIDs {
		monitors[i] = strconv.FormatInt(monitorID, 10)
	}
	s.apiJSON(w, r, http.StatusOK, struct {
		apiIncident
		MonitorIDs []string `json:"monitor_ids"`
	}{incidentJSON(in), monitors})
}

func (s *Server) apiCreateIncident(w http.ResponseWriter, r *http.Request) {
	pageID, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input struct {
		Title      string   `json:"title"`
		Impact     *string  `json:"impact"`
		Status     *string  `json:"status"`
		Message    string   `json:"message"`
		MonitorIDs []string `json:"monitor_ids"`
	}
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	nowMs := s.now().UnixMilli()
	in := store.Incident{
		ProjectID: pageID, Title: strings.TrimSpace(input.Title), Impact: "minor", Status: "investigating", StartedAtMs: nowMs,
	}
	if input.Impact != nil {
		in.Impact = *input.Impact
	}
	if input.Status != nil {
		in.Status = *input.Status
	}
	firstUpdate := strings.TrimSpace(input.Message)
	if err := apiText("title", in.Title, 1, 200); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if err := apiText("message", firstUpdate, 1, 5000); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if !slices.Contains(store.IncidentImpacts, in.Impact) {
		s.apiFailure(w, r, invalidAPIField("impact", "Use a supported impact."))
		return
	}
	if !slices.Contains(store.IncidentStatuses, in.Status) {
		s.apiFailure(w, r, invalidAPIField("status", "Use a supported incident status."))
		return
	}
	in.MonitorIDs, err = apiIDs("monitor_ids", input.MonitorIDs)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	id, err := s.store.CreatePageIncident(r.Context(), in, firstUpdate, nowMs)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiCreated(w, r, fmt.Sprintf("/api/v1/status-pages/%d/incidents", pageID), id)
}

func (s *Server) apiUpdateIncident(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "incident")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input struct {
		Title      *string   `json:"title"`
		Impact     *string   `json:"impact"`
		MonitorIDs *[]string `json:"monitor_ids"`
	}
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	edit := store.IncidentEdit{Title: input.Title, Impact: input.Impact}
	if edit.Title != nil {
		title := strings.TrimSpace(*edit.Title)
		if err := apiText("title", title, 1, 200); err != nil {
			s.apiFailure(w, r, err)
			return
		}
		edit.Title = &title
	}
	if edit.Impact != nil && !slices.Contains(store.IncidentImpacts, *edit.Impact) {
		s.apiFailure(w, r, invalidAPIField("impact", "Use a supported impact."))
		return
	}
	if input.MonitorIDs != nil {
		ids, err := apiIDs("monitor_ids", *input.MonitorIDs)
		if err != nil {
			s.apiFailure(w, r, err)
			return
		}
		edit.MonitorIDs = &ids
	}
	if err := s.store.EditPageIncident(r.Context(), pageID, id, edit); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiDeleteIncident(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "incident")
	if err == nil {
		err = s.store.DeletePageIncident(r.Context(), pageID, id)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiListIncidentUpdates(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "incident")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	query := func(ctx context.Context, after int64, limit int) ([]store.IncidentUpdate, error) {
		return s.store.PageIncidentUpdatesAfter(ctx, pageID, id, after, limit)
	}
	apiList(s, w, r, query, incidentNoteJSON)
}

func (s *Server) apiAddIncidentUpdate(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "incident")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input struct {
		Status string `json:"status"`
		Body   string `json:"body"`
	}
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	input.Body = strings.TrimSpace(input.Body)
	if !slices.Contains(store.IncidentStatuses, input.Status) {
		s.apiFailure(w, r, invalidAPIField("status", "Use a supported incident status."))
		return
	}
	if err := apiText("body", input.Body, 1, 5000); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if err := s.store.AddPageIncidentUpdate(r.Context(), pageID, id, input.Status, input.Body, s.now().UnixMilli()); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiPublishIncident(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "incident")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if r.ContentLength != 0 {
		var empty struct{}
		if !s.decodeAPIJSON(w, r, &empty) {
			return
		}
	}
	if err := s.store.PublishPageIncident(r.Context(), pageID, id, s.now().UnixMilli()); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
