package web

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/check"
	"uppies/internal/store"
)

const timelinePageSize = 50

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// monitorRow is a monitor with its state in words, for admin tables.
type monitorRow struct {
	store.Monitor
	State string
	Word  string
}

// overviewDays is the length of the uptime bar in the overview table.
const overviewDays = 30

const recentEventCount = 10

// overviewRow is one monitor in the overview table.
type overviewRow struct {
	monitorRow
	Project store.Project
	Days    []dayCell
	// Uptime is the percentage alone, since the column heading already says what it is.
	Uptime string
}

// attentionItem is something the admin should look at now.
type attentionItem struct {
	State string
	Text  string
	Note  string
	Href  string
}

type overviewEvent struct {
	eventRow
	Project string
}

type dashboardPage struct {
	Chrome chrome
	// Counts of monitors by state, for the strip at the top.
	Up, Down, Paused, Waiting, Total int
	OpenIncidents                    int
	Attention                        []attentionItem
	Rows                             []overviewRow
	Events                           []overviewEvent
	HasProjects                      bool
	Name                             string
	Slug                             string
	Problems                         map[string]string
}

// adminChrome builds the frame of an admin page: its title and the sidebar.
// A sidebar that cannot be loaded is logged and left empty, so the page
// itself still renders.
func (s *Server) adminChrome(r *http.Request, title string) chrome {
	c := chrome{Title: title, Admin: true, Nav: nav{OnOverview: r.URL.Path == "/admin"}}
	projects, err := s.store.Projects(r.Context())
	if err != nil {
		s.log.Error("load sidebar projects", "path", r.URL.Path, "error", err)
		return c
	}
	monitors, err := s.store.Monitors(r.Context())
	if err != nil {
		s.log.Error("load sidebar monitors", "path", r.URL.Path, "error", err)
		return c
	}
	position := make(map[int64]int, len(projects))
	for i, p := range projects {
		position[p.ID] = i
		prefix := fmt.Sprintf("/admin/projects/%d", p.ID)
		current := r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/")
		c.Nav.Projects = append(c.Nav.Projects, navProject{Project: p, State: stateNone, Current: current})
	}
	for _, m := range monitors {
		entry := &c.Nav.Projects[position[m.ProjectID]]
		switch state, _ := componentState(m); {
		case state == stateDown:
			entry.Down++
			entry.State = stateDown
		case state == stateOK && entry.State == stateNone:
			entry.State = stateOK
		}
	}
	return c
}

func monitorRows(monitors []store.Monitor) []monitorRow {
	rows := make([]monitorRow, 0, len(monitors))
	for _, m := range monitors {
		state, word := componentState(m)
		rows = append(rows, monitorRow{Monitor: m, State: state, Word: word})
	}
	return rows
}

// newOverviewRow adds the 30-day bar and the uptime figure to a monitor row.
func newOverviewRow(row monitorRow, project store.Project, rollups []store.DayRollup, todayStartMs int64) overviewRow {
	days, uptime := uptimeStrip(rollups, todayStartMs, overviewDays)
	return overviewRow{monitorRow: row, Project: project, Days: days, Uptime: strings.TrimSuffix(uptime, " uptime")}
}

func (s *Server) dashboard(r *http.Request) (dashboardPage, error) {
	ctx := r.Context()
	page := dashboardPage{Chrome: s.adminChrome(r, "overview")}
	projects, err := s.store.Projects(ctx)
	if err != nil {
		return page, err
	}
	monitors, err := s.store.Monitors(ctx)
	if err != nil {
		return page, err
	}
	todayStartMs := store.DayStartMs(s.now().UnixMilli())
	rollups, err := s.store.AllDailyRollups(ctx, todayStartMs-(overviewDays-1)*dayMs)
	if err != nil {
		return page, err
	}
	incidents, err := s.store.OpenIncidents(ctx)
	if err != nil {
		return page, err
	}
	events, err := s.store.RecentEvents(ctx, recentEventCount)
	if err != nil {
		return page, err
	}

	byID := make(map[int64]store.Project, len(projects))
	for _, p := range projects {
		byID[p.ID] = p
	}
	page.HasProjects = len(projects) > 0
	page.Total = len(monitors)
	// An outage opens a draft incident. The two are one thing to look at, so
	// the draft is reached through its monitor's line and not listed again.
	draftOf := make(map[int64]int64)
	for _, in := range incidents {
		if in.Published {
			continue
		}
		for _, monitorID := range in.MonitorIDs {
			draftOf[monitorID] = in.ID
		}
	}
	listed := make(map[int64]bool)
	for _, row := range monitorRows(monitors) {
		switch {
		case row.Paused:
			page.Paused++
		case row.State == stateDown:
			page.Down++
			item := attentionItem{
				State: stateDown, Text: row.Name + " is down", Note: row.LastError,
				Href: fmt.Sprintf("/admin/monitors/%d", row.ID),
			}
			if draftID, drafted := draftOf[row.ID]; drafted {
				listed[draftID] = true
				item.Note = "a draft incident is ready to publish"
				item.Href = fmt.Sprintf("/admin/incidents/%d", draftID)
			}
			page.Attention = append(page.Attention, item)
		case row.State == stateOK:
			page.Up++
		default:
			page.Waiting++
		}
		page.Rows = append(page.Rows, newOverviewRow(row, byID[row.ProjectID], rollups[row.ID], todayStartMs))
	}
	for _, in := range incidents {
		if listed[in.ID] {
			continue
		}
		item := attentionItem{State: stateWarn, Text: in.Title, Href: fmt.Sprintf("/admin/incidents/%d", in.ID)}
		if in.Published {
			page.OpenIncidents++
			item.Note = "open incident, " + in.Status
		} else {
			item.Note = "draft incident, not on the status page"
		}
		page.Attention = append(page.Attention, item)
	}
	for _, e := range events {
		row := eventRow{Event: e, Word: eventWords[e.Kind]}
		page.Events = append(page.Events, overviewEvent{eventRow: row, Project: byID[e.ProjectID].Name})
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
	Monitors  []overviewRow
	Incidents []store.Incident
	Events    []eventRow
	// Maintenance holds the running and upcoming windows.
	Maintenance []store.MaintenanceWindow
	Zone        *time.Location
	// Window holds the new-window form's values after a failed submit.
	Window   maintenanceForm
	Problems map[string]string
	OlderID  int64 // id to page the timeline from, 0 when there is nothing older
}

var eventWords = map[string]string{
	"monitor_down":       "went down",
	"monitor_up":         "recovered",
	"incident_published": "incident published",
	"incident_update":    "incident update",
}

// maintenanceForm holds the new-window form as typed.
type maintenanceForm struct {
	Title  string
	Starts string
	Ends   string
}

// dateTimeLayout is what a datetime-local input submits.
const dateTimeLayout = "2006-01-02T15:04"

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	page, err := s.projectPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "project.html", page)
}

func (s *Server) handleCreateMaintenance(w http.ResponseWriter, r *http.Request) {
	page, err := s.projectPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	form := maintenanceForm{
		Title:  strings.TrimSpace(r.PostForm.Get("title")),
		Starts: r.PostForm.Get("starts"),
		Ends:   r.PostForm.Get("ends"),
	}
	problems := make(map[string]string)
	if form.Title == "" || len(form.Title) > 200 {
		problems["title"] = "give the window a title of 1 to 200 characters."
	}
	starts, err := time.ParseInLocation(dateTimeLayout, form.Starts, page.Zone)
	if err != nil {
		problems["starts"] = "choose when it starts."
	}
	ends, err := time.ParseInLocation(dateTimeLayout, form.Ends, page.Zone)
	if err != nil {
		problems["ends"] = "choose when it ends."
	} else if !ends.After(starts) {
		problems["ends"] = "the end must be after the start."
	} else if !ends.After(s.now()) {
		problems["ends"] = "the end is already in the past."
	}
	if len(problems) > 0 {
		page.Window, page.Problems = form, problems
		s.render(w, r, http.StatusUnprocessableEntity, "project.html", page)
		return
	}
	window := store.MaintenanceWindow{
		ProjectID: page.Project.ID, Title: form.Title, StartsAtMs: starts.UnixMilli(), EndsAtMs: ends.UnixMilli(),
	}
	if _, err := s.store.CreateMaintenanceWindow(r.Context(), window, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d#maintenance-heading", page.Project.ID), http.StatusSeeOther)
}

func (s *Server) handleDeleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	windowID, err := strconv.ParseInt(r.PathValue("window"), 10, 64)
	if err != nil {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	if err := s.store.DeleteMaintenanceWindow(r.Context(), id, windowID); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d#maintenance-heading", id), http.StatusSeeOther)
}

func (s *Server) projectPage(r *http.Request) (projectPage, error) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		return projectPage{}, err
	}
	project, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		return projectPage{}, err
	}
	monitors, err := s.store.MonitorsByProject(ctx, id)
	if err != nil {
		return projectPage{}, err
	}
	incidents, err := s.store.IncidentsByProject(ctx, id)
	if err != nil {
		return projectPage{}, err
	}
	windows, err := s.store.MaintenanceWindows(ctx, id, s.now().UnixMilli())
	if err != nil {
		return projectPage{}, err
	}
	zone, err := time.LoadLocation(project.Timezone)
	if err != nil {
		zone = time.UTC // the zone was valid when saved, so only a system without zone data lands here
	}
	// A missing or malformed "before" means the first page.
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	events, err := s.store.Events(ctx, id, max(beforeID, 0), timelinePageSize+1)
	if err != nil {
		return projectPage{}, err
	}

	todayStartMs := store.DayStartMs(s.now().UnixMilli())
	rollups, err := s.store.DailyRollups(ctx, id, todayStartMs-(overviewDays-1)*dayMs)
	if err != nil {
		return projectPage{}, err
	}
	var rows []overviewRow
	for _, row := range monitorRows(monitors) {
		rows = append(rows, newOverviewRow(row, project, rollups[row.ID], todayStartMs))
	}

	page := projectPage{
		Chrome:      s.adminChrome(r, project.Name),
		Project:     project,
		Monitors:    rows,
		Incidents:   incidents,
		Maintenance: windows,
		Zone:        zone,
	}
	if len(events) > timelinePageSize {
		events = events[:timelinePageSize]
		page.OlderID = events[timelinePageSize-1].ID
	}
	for _, e := range events {
		page.Events = append(page.Events, eventRow{Event: e, Word: eventWords[e.Kind]})
	}
	return page, nil
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
	Kinds    []string
	Editing  bool
	Problems map[string]string
	// PushURL is where a push monitor's job reports in, empty for other kinds.
	PushURL string
}

// kindLabels names each monitor kind in the form.
var kindLabels = map[string]string{
	check.KindHTTP: "http or https request",
	check.KindTCP:  "tcp port",
	check.KindDNS:  "dns name",
	check.KindPing: "ping",
	check.KindPush: "push, for scheduled jobs that report in",
}

// kindIcons and channelIcons name the icon in static/icons.svg for each kind.
var kindIcons = map[string]string{
	check.KindHTTP: "globe", check.KindTCP: "plug", check.KindDNS: "network",
	check.KindPing: "activity", check.KindPush: "timer",
}

var channelIcons = map[string]string{"webhook": "webhook", "discord": "discord", "slack": "hash", "email": "mail"}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	err := s.store.RecordPush(r.Context(), r.PathValue("token"), s.now().UnixMilli())
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pushURL builds the address a push monitor's job must request.
func pushURL(r *http.Request, m store.Monitor) string {
	if m.PushToken == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/push/" + m.PushToken
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
	if m.Kind == check.KindPush {
		// The schema wants a target. A push monitor has none, so it stores its kind.
		m.Target = check.KindPush
		delete(problems, "target")
		if m.PushToken == "" {
			m.PushToken = rand.Text()
		}
	} else {
		m.PushToken = ""
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
		Chrome: s.adminChrome(r, "new monitor"), Project: project, Monitor: defaultMonitor(id), Kinds: check.Kinds,
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
			Chrome: s.adminChrome(r, "new monitor"), Project: project, Monitor: m, Kinds: check.Kinds, Problems: problems,
		})
		return
	}
	monitorID, err := s.store.CreateMonitor(r.Context(), m, s.now().UnixMilli())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	if m.Kind == check.KindPush {
		// Go to the monitor's page, which shows the address the job must call.
		http.Redirect(w, r, fmt.Sprintf("/admin/monitors/%d", monitorID), http.StatusSeeOther)
		return
	}
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
		Chrome: s.adminChrome(r, m.Name), Project: project, Monitor: m, Kinds: check.Kinds, Editing: true,
		PushURL: pushURL(r, m),
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
			Chrome: s.adminChrome(r, current.Name), Project: project, Monitor: m, Kinds: check.Kinds, Editing: true, Problems: problems,
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
		Chrome:   s.adminChrome(r, "new incident"),
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
	page.Chrome = s.adminChrome(r, page.Incident.Title)
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
