package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

type apiMaintenance struct {
	ID                 string     `json:"id"`
	PageID             string     `json:"status_page_id"`
	Title              string     `json:"title"`
	StartsAt           time.Time  `json:"starts_at"`
	EndsAt             *time.Time `json:"ends_at"`
	UntilFurtherNotice bool       `json:"until_further_notice"`
	AllMonitors        bool       `json:"all_monitors"`
	MonitorIDs         []string   `json:"monitor_ids"`
}

func (m apiMaintenance) cursor() string { return m.ID }

func maintenanceJSON(m store.MaintenanceWindow) apiMaintenance {
	view := apiMaintenance{
		ID: strconv.FormatInt(m.ID, 10), PageID: strconv.FormatInt(m.ProjectID, 10),
		Title: m.Title, StartsAt: time.UnixMilli(m.StartsAtMs).UTC(),
		UntilFurtherNotice: m.UntilFurtherNotice, AllMonitors: !m.Scoped,
		MonitorIDs: make([]string, len(m.MonitorIDs)),
	}
	if !m.UntilFurtherNotice {
		at := time.UnixMilli(m.EndsAtMs).UTC()
		view.EndsAt = &at
	}
	for i, id := range m.MonitorIDs {
		view.MonitorIDs[i] = strconv.FormatInt(id, 10)
	}
	return view
}

type maintenancePatch struct {
	Title              *string    `json:"title"`
	StartsAt           *time.Time `json:"starts_at"`
	EndsAt             *time.Time `json:"ends_at"`
	UntilFurtherNotice *bool      `json:"until_further_notice"`
	MonitorIDs         *[]string  `json:"monitor_ids"`
}

func (input maintenancePatch) apply(m *store.MaintenanceWindow, nowMs int64) error {
	creating := m.ID == 0
	if input.Title != nil {
		m.Title = strings.TrimSpace(*input.Title)
	}
	if creating || input.Title != nil {
		if err := apiText("title", m.Title, 1, 200); err != nil {
			return err
		}
	}
	if creating && input.StartsAt == nil {
		return invalidAPIField("starts_at", "Supply a start time.")
	}
	if input.StartsAt != nil {
		m.StartsAtMs = input.StartsAt.UnixMilli()
	}
	wasOpen := m.UntilFurtherNotice
	if input.UntilFurtherNotice != nil {
		if *input.UntilFurtherNotice && input.EndsAt != nil {
			return invalidAPIField("ends_at", "Omit the end time for an open-ended window.")
		}
		m.UntilFurtherNotice = *input.UntilFurtherNotice
	}
	if input.EndsAt != nil {
		m.EndsAtMs = input.EndsAt.UnixMilli()
		m.UntilFurtherNotice = false
	}
	if !m.UntilFurtherNotice && input.EndsAt == nil && (creating || wasOpen) {
		return invalidAPIField("ends_at", "Supply an end time.")
	}
	if !m.UntilFurtherNotice && (input.StartsAt != nil || input.EndsAt != nil) {
		if m.EndsAtMs <= m.StartsAtMs {
			return invalidAPIField("ends_at", "The end must be after the start.")
		}
		if m.EndsAtMs <= nowMs {
			return invalidAPIField("ends_at", "The end must be in the future.")
		}
	}
	if input.MonitorIDs != nil {
		ids, err := apiIDs("monitor_ids", *input.MonitorIDs)
		if err != nil {
			return err
		}
		m.MonitorIDs, m.Scoped = ids, len(ids) > 0
	}
	return nil
}

func (s *Server) apiListMaintenance(w http.ResponseWriter, r *http.Request) {
	pageID, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if _, err := s.store.ProjectByID(r.Context(), pageID); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	query := func(ctx context.Context, after int64, limit int) ([]store.MaintenanceWindow, error) {
		return s.store.MaintenanceWindowsAfter(ctx, pageID, after, limit)
	}
	apiList(s, w, r, query, maintenanceJSON)
}

func (s *Server) apiReadMaintenance(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "window")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	window, err := s.store.MaintenanceWindow(r.Context(), pageID, id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiJSON(w, r, http.StatusOK, maintenanceJSON(window))
}

func (s *Server) apiCreateMaintenance(w http.ResponseWriter, r *http.Request) {
	pageID, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input maintenancePatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	window := store.MaintenanceWindow{ProjectID: pageID}
	nowMs := s.now().UnixMilli()
	if err := input.apply(&window, nowMs); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	id, err := s.store.CreateMaintenanceWindow(r.Context(), window, nowMs)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	s.apiCreated(w, r, fmt.Sprintf("/api/v1/status-pages/%d/maintenance", pageID), id)
}

func (s *Server) apiUpdateMaintenance(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "window")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input maintenancePatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	nowMs := s.now().UnixMilli()
	edit := func(m *store.MaintenanceWindow) error { return input.apply(m, nowMs) }
	if err := s.store.EditMaintenanceWindow(r.Context(), pageID, id, input.MonitorIDs != nil, edit); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiDeleteMaintenance(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "window")
	if err == nil {
		err = s.store.DeleteMaintenanceWindow(r.Context(), pageID, id)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiEndMaintenance(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "window")
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
	if err := s.store.EndMaintenanceWindow(r.Context(), pageID, id, s.now().UnixMilli()); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	w.WriteHeader(http.StatusNoContent)
}

type apiMaintenanceUpdate struct {
	ID   string    `json:"id"`
	Body string    `json:"body"`
	At   time.Time `json:"at"`
}

func (m apiMaintenanceUpdate) cursor() string { return m.ID }

func (s *Server) apiListMaintenanceUpdates(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "window")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if _, err := s.store.MaintenanceWindow(r.Context(), pageID, id); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	query := func(ctx context.Context, after int64, limit int) ([]store.MaintenanceUpdate, error) {
		return s.store.MaintenanceUpdatesAfter(ctx, id, after, limit)
	}
	apiList(s, w, r, query, func(update store.MaintenanceUpdate) apiMaintenanceUpdate {
		return apiMaintenanceUpdate{ID: strconv.FormatInt(update.ID, 10), Body: update.Body, At: time.UnixMilli(update.AtMs).UTC()}
	})
}

func (s *Server) apiAddMaintenanceUpdate(w http.ResponseWriter, r *http.Request) {
	pageID, id, err := apiPageResourceIDs(r, "window")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	input.Body = strings.TrimSpace(input.Body)
	if err := apiText("body", input.Body, 1, 5000); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if err := s.store.AddMaintenanceUpdate(r.Context(), pageID, id, input.Body, s.now().UnixMilli()); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
