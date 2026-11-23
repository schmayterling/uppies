package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

type maintenanceHistoryPage struct {
	Chrome  chrome
	Project store.Project
	Zone    *time.Location
	Windows []store.MaintenanceWindow
	Before  int64
	OlderID int64
}

func (s *Server) handleMaintenanceHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	project, err := s.store.ProjectByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	before = max(before, 0)
	windows, err := s.store.MaintenanceWindowsBefore(r.Context(), id, before, timelinePageSize+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	zone, _ := pageZone(project, "")
	page := maintenanceHistoryPage{
		Chrome: s.adminChrome(r, "maintenance history"), Project: project, Zone: zone, Windows: windows, Before: before,
	}
	if len(windows) > timelinePageSize {
		page.Windows = windows[:timelinePageSize]
		page.OlderID = page.Windows[timelinePageSize-1].ID
	}
	s.render(w, r, http.StatusOK, "maintenance_history.html", page)
}

type maintenanceWindowPage struct {
	Chrome  chrome
	Project store.Project
	Zone    *time.Location
	Window  maintenanceView
	Ended   bool
	Before  int64
	OlderID int64
	Draft   string
	Problem string
}

func maintenanceAdminIDs(r *http.Request) (int64, int64, error) {
	projectID, err := pathID(r)
	if err != nil {
		return 0, 0, err
	}
	windowID, err := strconv.ParseInt(r.PathValue("window"), 10, 64)
	if err != nil || windowID < 1 {
		return 0, 0, store.ErrNotFound
	}
	return projectID, windowID, nil
}

func (s *Server) maintenanceWindowPage(r *http.Request) (maintenanceWindowPage, error) {
	projectID, windowID, err := maintenanceAdminIDs(r)
	if err != nil {
		return maintenanceWindowPage{}, err
	}
	ctx := r.Context()
	window, err := s.store.MaintenanceWindow(ctx, projectID, windowID)
	if err != nil {
		return maintenanceWindowPage{}, err
	}
	project, err := s.store.ProjectByID(ctx, projectID)
	if err != nil {
		return maintenanceWindowPage{}, err
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	before = max(before, 0)
	updates, err := s.store.MaintenanceUpdatesBefore(ctx, windowID, before, timelinePageSize+1)
	if err != nil {
		return maintenanceWindowPage{}, err
	}
	nowMs := s.now().UnixMilli()
	zone, _ := pageZone(project, "")
	page := maintenanceWindowPage{
		Chrome: s.adminChrome(r, window.Title), Project: project, Zone: zone, Before: before,
		Window: maintenanceView{MaintenanceWindow: window, Running: window.RunningAt(nowMs), Updates: updates},
		Ended:  !window.UntilFurtherNotice && window.EndsAtMs <= nowMs,
	}
	if len(updates) > timelinePageSize {
		page.Window.Updates = updates[:timelinePageSize]
		page.OlderID = updates[timelinePageSize-1].ID
	}
	if window.Scoped {
		placed, err := s.store.MonitorsByProject(ctx, projectID)
		if err != nil {
			return maintenanceWindowPage{}, err
		}
		for _, monitor := range placed {
			if slices.Contains(window.MonitorIDs, monitor.ID) {
				page.Window.Affected = append(page.Window.Affected, monitor.Name)
			}
		}
	}
	return page, nil
}

func (s *Server) handleMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	page, err := s.maintenanceWindowPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "maintenance.html", page)
}

func (s *Server) handleEndMaintenance(w http.ResponseWriter, r *http.Request) {
	projectID, windowID, err := maintenanceAdminIDs(r)
	if err == nil {
		err = s.store.EndMaintenanceWindow(r.Context(), projectID, windowID, s.now().UnixMilli())
	}
	if errors.Is(err, store.ErrMaintenanceNotStarted) {
		http.Error(w, "this window has not started.", http.StatusConflict)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d/maintenance/%d", projectID, windowID), http.StatusSeeOther)
}

func (s *Server) handleAddMaintenanceUpdate(w http.ResponseWriter, r *http.Request) {
	projectID, windowID, err := maintenanceAdminIDs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	body := strings.TrimSpace(r.PostForm.Get("body"))
	if apiText("body", body, 1, 5000) != nil {
		page, err := s.maintenanceWindowPage(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.Draft, page.Problem = body, "write an update of 1 to 5,000 characters."
		s.render(w, r, http.StatusUnprocessableEntity, "maintenance.html", page)
		return
	}
	if err := s.store.AddMaintenanceUpdate(r.Context(), projectID, windowID, body, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d/maintenance/%d#updates-heading", projectID, windowID), http.StatusSeeOther)
}
