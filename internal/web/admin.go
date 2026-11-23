package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"uppies/internal/check"
	"uppies/internal/store"
)

const timelinePageSize = 50

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

type projectSummary struct {
	Project  store.Project
	Monitors []monitorRow
}

// monitorRow is a monitor with its state in words, for admin tables.
type monitorRow struct {
	store.Monitor
	State string
	Word  string
}

type dashboardPage struct {
	Chrome   chrome
	Projects []projectSummary
	Name     string
	Slug     string
	Problems map[string]string
}

func monitorRows(monitors []store.Monitor) []monitorRow {
	rows := make([]monitorRow, 0, len(monitors))
	for _, m := range monitors {
		state, word := componentState(m)
		rows = append(rows, monitorRow{Monitor: m, State: state, Word: word})
	}
	return rows
}

func (s *Server) dashboard(r *http.Request) (dashboardPage, error) {
	page := dashboardPage{Chrome: chrome{Title: "projects", Admin: true}}
	projects, err := s.store.Projects(r.Context())
	if err != nil {
		return page, err
	}
	monitors, err := s.store.Monitors(r.Context())
	if err != nil {
		return page, err
	}
	byProject := make(map[int64][]store.Monitor)
	for _, m := range monitors {
		byProject[m.ProjectID] = append(byProject[m.ProjectID], m)
	}
	for _, p := range projects {
		page.Projects = append(page.Projects, projectSummary{Project: p, Monitors: monitorRows(byProject[p.ID])})
	}
	return page, nil
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	page, err := s.dashboard(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard.html", page)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	slug := strings.TrimSpace(r.PostForm.Get("slug"))
	problems := make(map[string]string)
	if name == "" || len(name) > 120 {
		problems["name"] = "give the project a name of 1 to 120 characters."
	}
	if !slugPattern.MatchString(slug) {
		problems["slug"] = "use lowercase letters, digits and hyphens, for example my-project."
	}

	var id int64
	if len(problems) == 0 {
		var err error
		id, err = s.store.CreateProject(r.Context(), slug, name, s.now().UnixMilli())
		if errors.Is(err, store.ErrSlugTaken) {
			problems["slug"] = "another project already uses this address."
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if len(problems) > 0 {
		page, err := s.dashboard(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.Name, page.Slug, page.Problems = name, slug, problems
		s.render(w, r, http.StatusUnprocessableEntity, "dashboard.html", page)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", id), http.StatusSeeOther)
}

type eventRow struct {
	store.Event
	Word string
}

type projectPage struct {
	Chrome    chrome
	Project   store.Project
	Monitors  []monitorRow
	Incidents []store.Incident
	Events    []eventRow
	OlderID   int64 // id to page the timeline from, 0 when there is nothing older
}

var eventWords = map[string]string{
	"monitor_down":       "went down",
	"monitor_up":         "recovered",
	"incident_published": "incident published",
	"incident_update":    "incident update",
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	project, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	monitors, err := s.store.MonitorsByProject(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	incidents, err := s.store.IncidentsByProject(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A missing or malformed "before" means the first page.
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	events, err := s.store.Events(ctx, id, max(beforeID, 0), timelinePageSize+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	page := projectPage{
		Chrome:    chrome{Title: project.Name, Admin: true},
		Project:   project,
		Monitors:  monitorRows(monitors),
		Incidents: incidents,
	}
	if len(events) > timelinePageSize {
		events = events[:timelinePageSize]
		page.OlderID = events[timelinePageSize-1].ID
	}
	for _, e := range events {
		page.Events = append(page.Events, eventRow{Event: e, Word: eventWords[e.Kind]})
	}
	s.render(w, r, http.StatusOK, "project.html", page)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteProject(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

type monitorFormPage struct {
	Chrome   chrome
	Project  store.Project
	Monitor  store.Monitor
	Editing  bool
	Problems map[string]string
}

// defaultMonitor holds the settings a new monitor starts with: a check every
// minute, down after the third failed check in a row, up after two good ones.
func defaultMonitor(projectID int64) store.Monitor {
	return store.Monitor{
		ProjectID: projectID, Kind: check.KindHTTP, IntervalS: 60, TimeoutMs: 10_000,
		Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2, Public: true,
	}
}

// formInt reads a whole number in [low, high] from a form field.
func formInt(form url.Values, field string, low, high int, problems map[string]string) int {
	n, err := strconv.Atoi(strings.TrimSpace(form.Get(field)))
	if err != nil || n < low || n > high {
		problems[field] = fmt.Sprintf("enter a whole number from %d to %d.", low, high)
		return 0
	}
	return n
}

// parseMonitorForm reads the monitor form into m and returns what is wrong
// with it, keyed by field name. The ranges match the constraints in the schema.
func parseMonitorForm(form url.Values, m store.Monitor) (store.Monitor, map[string]string) {
	problems := make(map[string]string)
	m.Name = strings.TrimSpace(form.Get("name"))
	m.Kind = form.Get("kind")
	m.Target = strings.TrimSpace(form.Get("target"))
	m.Keyword = form.Get("keyword")
	m.Paused = form.Get("paused") == "on"
	m.Public = form.Get("public") == "on"

	if m.Name == "" || len(m.Name) > 120 {
		problems["name"] = "give the monitor a name of 1 to 120 characters."
	}
	if len(m.Target) > 2048 {
		problems["target"] = "the target is too long."
	} else if err := check.ValidateTarget(m.Kind, m.Target); err != nil {
		problems["target"] = err.Error() + "."
	}
	if m.Kind != check.KindHTTP {
		m.Keyword = ""
	}
	m.IntervalS = formInt(form, "interval_s", 20, 86400, problems)
	timeoutS := formInt(form, "timeout_s", 1, 60, problems)
	m.TimeoutMs = timeoutS * 1000
	m.Retries = formInt(form, "retries", 0, 10, problems)
	m.RetryIntervalS = formInt(form, "retry_interval_s", 1, 3600, problems)
	m.RecoveryChecks = formInt(form, "recovery_checks", 1, 10, problems)
	return m, problems
}

func (s *Server) handleNewMonitorForm(w http.ResponseWriter, r *http.Request) {
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
	s.render(w, r, http.StatusOK, "monitor_form.html", monitorFormPage{
		Chrome: chrome{Title: "new monitor", Admin: true}, Project: project, Monitor: defaultMonitor(id),
	})
}

func (s *Server) handleCreateMonitor(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	m, problems := parseMonitorForm(r.PostForm, defaultMonitor(id))
	if len(problems) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "monitor_form.html", monitorFormPage{
			Chrome: chrome{Title: "new monitor", Admin: true}, Project: project, Monitor: m, Problems: problems,
		})
		return
	}
	if _, err := s.store.CreateMonitor(r.Context(), m, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", id), http.StatusSeeOther)
}

// monitorAndProject loads the monitor named by the path and its project.
func (s *Server) monitorAndProject(r *http.Request) (store.Monitor, store.Project, error) {
	id, err := pathID(r)
	if err != nil {
		return store.Monitor{}, store.Project{}, err
	}
	m, err := s.store.Monitor(r.Context(), id)
	if err != nil {
		return store.Monitor{}, store.Project{}, err
	}
	project, err := s.store.ProjectByID(r.Context(), m.ProjectID)
	return m, project, err
}

func (s *Server) handleEditMonitorForm(w http.ResponseWriter, r *http.Request) {
	m, project, err := s.monitorAndProject(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "monitor_form.html", monitorFormPage{
		Chrome: chrome{Title: m.Name, Admin: true}, Project: project, Monitor: m, Editing: true,
	})
}

func (s *Server) handleUpdateMonitor(w http.ResponseWriter, r *http.Request) {
	current, project, err := s.monitorAndProject(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	m, problems := parseMonitorForm(r.PostForm, current)
	if len(problems) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "monitor_form.html", monitorFormPage{
			Chrome: chrome{Title: current.Name, Admin: true}, Project: project, Monitor: m, Editing: true, Problems: problems,
		})
		return
	}
	if err := s.store.UpdateMonitor(r.Context(), m); err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", project.ID), http.StatusSeeOther)
}

func (s *Server) handleDeleteMonitor(w http.ResponseWriter, r *http.Request) {
	m, project, err := s.monitorAndProject(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteMonitor(r.Context(), m.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", project.ID), http.StatusSeeOther)
}

type incidentFormPage struct {
	Chrome   chrome
	Project  store.Project
	Monitors []store.Monitor
	Incident store.Incident
	Message  string
	Statuses []string
	Impacts  []string
	Problems map[string]string
}

func (s *Server) incidentForm(r *http.Request) (incidentFormPage, error) {
	page := incidentFormPage{
		Chrome:   chrome{Title: "new incident", Admin: true},
		Statuses: store.IncidentStatuses,
		Impacts:  store.IncidentImpacts,
		Incident: store.Incident{Impact: "minor", Status: "investigating"},
	}
	id, err := pathID(r)
	if err != nil {
		return page, err
	}
	page.Project, err = s.store.ProjectByID(r.Context(), id)
	if err != nil {
		return page, err
	}
	page.Monitors, err = s.store.MonitorsByProject(r.Context(), id)
	return page, err
}

func (s *Server) handleNewIncidentForm(w http.ResponseWriter, r *http.Request) {
	page, err := s.incidentForm(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "incident_new.html", page)
}

func (s *Server) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	page, err := s.incidentForm(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	form := r.PostForm
	now := s.now().UnixMilli()
	in := store.Incident{
		ProjectID:   page.Project.ID,
		Title:       strings.TrimSpace(form.Get("title")),
		Impact:      form.Get("impact"),
		Status:      form.Get("status"),
		Published:   form.Get("action") == "publish",
		StartedAtMs: now,
	}
	for _, raw := range form["monitor"] {
		if monitorID, err := strconv.ParseInt(raw, 10, 64); err == nil {
			in.MonitorIDs = append(in.MonitorIDs, monitorID)
		}
	}
	message := strings.TrimSpace(form.Get("message"))

	problems := make(map[string]string)
	if in.Title == "" || len(in.Title) > 200 {
		problems["title"] = "give the incident a title of 1 to 200 characters."
	}
	if !slices.Contains(store.IncidentImpacts, in.Impact) {
		problems["impact"] = "choose an impact."
	}
	if !slices.Contains(store.IncidentStatuses, in.Status) {
		problems["status"] = "choose a status."
	}
	if message == "" || len(message) > 5000 {
		problems["message"] = "write a first update of 1 to 5000 characters."
	}
	if len(problems) > 0 {
		page.Incident, page.Message, page.Problems = in, message, problems
		s.render(w, r, http.StatusUnprocessableEntity, "incident_new.html", page)
		return
	}
	id, err := s.store.CreateIncident(r.Context(), in, message, now)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", id), http.StatusSeeOther)
}

type incidentPage struct {
	Chrome   chrome
	Project  store.Project
	Incident store.Incident
	Affected []string
	Statuses []string
	Message  string
	Problems map[string]string
}

func (s *Server) incidentPage(r *http.Request) (incidentPage, error) {
	page := incidentPage{Statuses: store.IncidentStatuses}
	id, err := pathID(r)
	if err != nil {
		return page, err
	}
	page.Incident, err = s.store.Incident(r.Context(), id)
	if err != nil {
		return page, err
	}
	page.Chrome = chrome{Title: page.Incident.Title, Admin: true}
	page.Project, err = s.store.ProjectByID(r.Context(), page.Incident.ProjectID)
	if err != nil {
		return page, err
	}
	monitors, err := s.store.MonitorsByProject(r.Context(), page.Project.ID)
	if err != nil {
		return page, err
	}
	for _, m := range monitors {
		if slices.Contains(page.Incident.MonitorIDs, m.ID) {
			page.Affected = append(page.Affected, m.Name)
		}
	}
	return page, nil
}

func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
	page, err := s.incidentPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "incident.html", page)
}

func (s *Server) handleAddIncidentUpdate(w http.ResponseWriter, r *http.Request) {
	page, err := s.incidentPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	status := r.PostForm.Get("status")
	message := strings.TrimSpace(r.PostForm.Get("message"))
	problems := make(map[string]string)
	if !slices.Contains(store.IncidentStatuses, status) {
		problems["status"] = "choose a status."
	}
	if message == "" || len(message) > 5000 {
		problems["message"] = "write an update of 1 to 5000 characters."
	}
	if len(problems) > 0 {
		page.Message, page.Problems = message, problems
		s.render(w, r, http.StatusUnprocessableEntity, "incident.html", page)
		return
	}
	if err := s.store.AddIncidentUpdate(r.Context(), page.Incident.ID, status, message, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", page.Incident.ID), http.StatusSeeOther)
}

func (s *Server) handlePublishIncident(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.store.Incident(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.PublishIncident(r.Context(), id, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", id), http.StatusSeeOther)
}

func (s *Server) handleDeleteIncident(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in, err := s.store.Incident(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteIncident(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", in.ProjectID), http.StatusSeeOther)
}
