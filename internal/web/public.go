package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // timezone names must resolve in containers that ship no zone database

	"uppies/internal/health"
	"uppies/internal/store"
)

const (
	// A day with this much downtime or more is drawn as an outage, less as a disruption.
	outageDayMs = int64(time.Hour / time.Millisecond)
	dayMs       = int64(24 * time.Hour / time.Millisecond)
)

// State classes shared by the banner, components and day cells. Each maps to
// a colour and a shape in app.css and always comes with a word.
const (
	stateOK   = "ok"
	stateWarn = "warn"
	stateDown = "down"
	stateNone = "none"
)

// dayCell is one day of a component's uptime strip.
type dayCell struct {
	State string
	Label string
}

// componentView is one monitor as the public sees it: no target, no error text.
type componentView struct {
	ID     int64
	Name   string
	State  string
	Word   string
	Days   []dayCell
	Uptime string
	// Display picks what is drawn under the name: "bars", "latency", "summary" or "minimal".
	Display string
	// Category is set on the first component of each category, for its heading.
	Category string
	// Figures are the uptime numbers of the "summary" display.
	Figures []figure
	// Spark is the point list of the "latency" display's line, and Latency its caption.
	Spark   string
	Latency string
	// SparkDays has one label per day under the line, oldest first, for the tooltips.
	SparkDays []string
	// Area is the shape under the line, filled the way the settings say.
	Area graphArea
}

// figure is one labelled number.
type figure struct {
	Label string
	Value string
}

type incidentView struct {
	store.Incident
	Affected []string
}

type statusPage struct {
	Chrome   chrome
	Project  store.Project
	instance store.InstanceSettings
	// Zone is the timezone that incident times are shown in.
	Zone *time.Location
	// Images maps each image kind the project has to its version.
	Images map[string]int64
	// ZoneName is the timezone times are shown in, Zones the ones offered, and
	// TZ the visitor's own choice when it differs from the project's.
	ZoneName string
	Zones    []string
	TZ       string
	// NavLinks are the project's own links for the bar at the top.
	NavLinks []navLink
	// Calm is true when none of the recent days had an incident, so the page
	// says so in a sentence instead of drawing a timeline of nothing.
	Calm        bool
	Overall     overallStatus
	BannerState string
	// BannerIcon names the round icon beside the sentence: check, warning, cross, dash, wrench or slash.
	BannerIcon string
	BannerText string
	// LastCheckAtMs is the newest check of a monitor shown on this page.
	LastCheckAtMs int64
	Components    []componentView
	Active        []incidentView
	// Recent lists the last few days, today first, with the resolved incidents of each.
	Recent []dayGroup
	// Maintenance holds the running and upcoming windows, soonest first.
	Maintenance []maintenanceView
}

type maintenanceView struct {
	store.MaintenanceWindow
	Running  bool
	Affected []string
	Updates  []store.MaintenanceUpdate
}

// indexSite is the public branding shared by the status list and incident archive.
type indexSite struct {
	Chrome   chrome
	NavLinks []navLink
	// Listed is false when the instance keeps its status pages off the front page.
	Listed            bool
	StatusIcon        string
	Announcement      string
	AnnouncementColor string
	FooterText        string
	ShowPoweredBy     bool
}

type indexPage struct {
	indexSite
	Projects []indexProject
	// Layout is the instance's choice of how the list is drawn.
	Layout          string
	IncidentHistory indexIncidentHistory
	// State is the worst state among the listed pages and Summary says it in a sentence.
	State   string
	Summary string
}

// indexProject is one status page in the public list, with its state in one word.
type indexProject struct {
	store.Project
	State string
	Word  string
	// Count says how many of the page's public monitors are up.
	Count string
	// LogoVersion is set when the project has a logo to show.
	LogoVersion int64
	Days        []dayCell
	Uptime      string
}

// indexSummary sums the list up: the worst state, and a sentence that counts
// the pages that are not fine.
func indexSummary(projects []indexProject) (state, sentence string) {
	troubled := 0
	state = stateOK
	for _, p := range projects {
		switch p.State {
		case stateDown:
			troubled++
			state = stateDown
		case stateWarn:
			troubled++
			if state != stateDown {
				state = stateWarn
			}
		}
	}
	switch {
	case troubled == 0:
		return state, "every status page is fine."
	case troubled == 1:
		return state, fmt.Sprintf("1 of %d status pages has a problem.", len(projects))
	default:
		return state, fmt.Sprintf("%d of %d status pages have a problem.", troubled, len(projects))
	}
}

// upCount says how many of the monitors are up, in the words of the list.
func upCount(monitors []store.Monitor) string {
	if len(monitors) == 0 {
		return "no monitors"
	}
	up := 0
	for _, m := range monitors {
		if !m.Paused && m.Tracker.Status == health.Up {
			up++
		}
	}
	return fmt.Sprintf("%d of %d up", up, len(monitors))
}

// indexWords names a status page's state in the public list.
var indexWords = map[string]string{stateOK: "all up", stateDown: "problems", stateWarn: "incident", stateNone: "waiting"}

// requestHost returns the hostname a request was sent to, lowercase and without a port.
func requestHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return strings.ToLower(host)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectByDomain(r.Context(), requestHost(r))
	if err == nil {
		s.renderStatusPage(w, r, project)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	s.handlePublicList(w, r)
}

func (s *Server) handlePublicList(w http.ResponseWriter, r *http.Request) {
	exists, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}
	projects, err := s.store.Projects(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	instance, err := s.store.Instance(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := indexPage{Layout: instance.ListLayout}
	if instance.PublicList {
		page.Projects, err = s.indexRows(ctx, projects, s.now().UnixMilli(), page.Layout == "rows")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		logos, err := s.store.LogoVersions(ctx)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for i := range page.Projects {
			page.Projects[i].LogoVersion = logos[page.Projects[i].ID]
		}
		page.IncidentHistory, err = s.loadIndexIncidentHistory(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	page.State, page.Summary = indexSummary(page.Projects)
	page.indexSite = s.newIndexSite(ctx, instance, page.State, instance.Name+" status",
		"Service status and incident history for "+instance.Name+".")
	page.Chrome.Auth = page.Layout == "compact"
	page.Chrome.SignInLink = true

	canonical := publicOrigin(r, instance.PublicURL)
	canonical.Path = "/"
	for _, p := range projects {
		if p.Domain != "" && p.Domain == canonical.Hostname() {
			canonical.Path = "/status-pages"
			break
		}
	}
	noIndex := !instance.PublicList || (canonical.Path != "/" && instance.PublicURL == "")
	page.Chrome.setPublicMetadata(canonical, instance.Name, noIndex)
	s.render(w, r, http.StatusOK, "index.html", page)
}

func (s *Server) newIndexSite(ctx context.Context, instance store.InstanceSettings, state, title, description string) indexSite {
	site := indexSite{
		Chrome: chrome{Title: title, Description: description, Unbranded: true, Brand: instance.Name, Favicon: s.faviconAddress(ctx)},
		Listed: instance.PublicList, StatusIcon: listIcons[state],
		Announcement: instance.ListAnnouncement, AnnouncementColor: instance.ListAnnouncementColor,
		FooterText: instance.ListFooterText, ShowPoweredBy: instance.ListShowPoweredBy,
	}
	if site.Listed {
		site.NavLinks, _ = parseNavLinks(instance.NavLinks)
	}
	return site
}

// indexRows uses the same monitor coverage for the homepage list and the archive's status icon.
func (s *Server) indexRows(ctx context.Context, projects []store.Project, nowMs int64, showUptime bool) ([]indexProject, error) {
	if len(projects) == 0 {
		return nil, nil
	}
	placed, err := s.store.PlacedMonitors(ctx)
	if err != nil {
		return nil, err
	}
	counted := make(map[int64][]store.Monitor)
	for _, m := range placed {
		if !m.ExcludeFromStatus {
			counted[m.ProjectID] = append(counted[m.ProjectID], m.Monitor)
		}
	}
	maintenance, err := s.store.ActiveMaintenanceCoverage(ctx, nowMs, 0)
	if err != nil {
		return nil, err
	}
	var rollups map[int64][]store.DayRollup
	todayStartMs := store.DayStartMs(nowMs)
	if showUptime {
		var longest int
		for _, p := range projects {
			longest = max(longest, p.HistoryDays)
		}
		rollups, err = s.store.AllDailyRollups(ctx, todayStartMs-int64(longest-1)*dayMs)
		if err != nil {
			return nil, err
		}
	}
	rows := make([]indexProject, 0, len(projects))
	for _, p := range projects {
		monitors := counted[p.ID]
		status, current := currentPageStatus(monitors, maintenance[p.ID], 0)
		state := overallViews[status].state
		row := indexProject{Project: p, State: state, Word: indexWords[state], Count: upCount(current)}
		if status == overallMaintenance {
			row.Word, row.Count = "under maintenance", ""
		}
		if showUptime {
			pageRollups := make(map[int64][]store.DayRollup, len(monitors))
			for _, monitor := range monitors {
				pageRollups[monitor.ID] = rollups[monitor.ID]
			}
			row.Days, row.Uptime = projectUptimeStrip(pageRollups, todayStartMs, p.HistoryDays)
			backfillDays(row.Days, p.Backfill)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleTheme serves a project's look as a stylesheet. A separate file keeps
// the strict content security policy, which allows no inline styles.
func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	if _, err := io.WriteString(w, themeCSS(project)); err != nil {
		s.log.Error("write theme", "path", r.URL.Path, "error", err)
	}
}

// themeCSS turns a project's settings into CSS. The lightness and chroma of
// the accent are fixed so that any hue keeps its contrast in both themes.
func themeCSS(p store.Project) string {
	scheme := "light dark"
	if p.Theme == "light" || p.Theme == "dark" {
		scheme = p.Theme
	}
	css := fmt.Sprintf(":root {\n  color-scheme: %s;\n  --accent: light-dark(oklch(0.45 0.075 %d), oklch(0.8 0.075 %d));\n}\n",
		scheme, p.AccentHue, p.AccentHue)
	// The easy look settings each add one block. The default choices add nothing.
	css += lookCSS[p.Surface] + lookCSS[p.Corners] + lookCSS[p.Font]
	return css + p.CustomCSS + "\n"
}

// lookCSS holds the stylesheet block of each non-default look choice. The
// paper colours are the house paper layer from tokens.css.
var lookCSS = map[string]string{
	"paper": `:root {
  --background: light-dark(oklch(0.985 0.022 93), oklch(0.19 0 0));
  --surface: light-dark(oklch(0.975 0.014 78), oklch(0.22 0 0));
  --ink: light-dark(oklch(0.225 0.011 73), oklch(0.94 0 0));
  --muted: light-dark(oklch(0.47 0.025 74), oklch(0.76 0 0));
  --border: light-dark(rgb(31 27 22 / 0.2), oklch(0.36 0 0));
}
`,
	"sharp":   ":root { --radius-inner: 0px; --radius-control: 2px; --radius-card: 4px; --radius-surface: 4px; }\n",
	"round":   ":root { --radius-inner: 6px; --radius-control: 10px; --radius-card: 18px; --radius-surface: 22px; }\n",
	"rounded": ":root { --font-ui: var(--font-rounded); }\n",
	"serif":   ":root { --font-ui: var(--font-serif); }\n",
	"mono":    ":root { --font-ui: var(--font-mono); }\n",
}

func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderStatusPage(w, r, project)
}

func (s *Server) renderStatusPage(w http.ResponseWriter, r *http.Request, project store.Project) {
	page, err := s.statusPage(r.Context(), project, r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	origin := publicOrigin(r, page.instance.PublicURL)
	page.Chrome.BrandLink = navbarDestination(project, page.TZ, page.instance.PublicURL)
	page.Chrome.setPublicMetadata(projectURL(origin, project, "", nil), project.Name, project.NoIndex)
	s.render(w, r, http.StatusOK, "status.html", page)
}

// badgeColours are the status colours as fixed values, because a badge is
// shown inside other sites where this page's stylesheet does not apply.
var badgeColours = map[string]string{stateOK: "#236032", stateWarn: "#704600", stateDown: "#aa2233", stateNone: "#5b6166"}

var badgeWords = map[overallStatus]string{
	overallUp: "up", overallPartial: "partly down", overallDown: "down",
	overallIncident: "incident", overallWaiting: "unknown", overallMaintenance: "maintenance",
}

// handleBadge serves a project's current state as a small image for readmes and other sites.
func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := s.statusPage(r.Context(), project, r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	word := badgeWords[page.Overall]
	const labelWidth, height = 48, 20
	// 6.5 units per character is close enough for the 11-unit sans-serif used below.
	wordWidth := 14 + len(word)*13/2
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=60")
	_, err = fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" role="img" aria-label="status: %s">`+
		`<title>status: %s</title><clipPath id="r"><rect width="%d" height="%d" rx="4"/></clipPath>`+
		`<g clip-path="url(#r)"><rect width="%d" height="%d" fill="#2b2f33"/><rect x="%d" width="%d" height="%d" fill="%s"/></g>`+
		`<g fill="#fff" font-family="Verdana,Geneva,sans-serif" font-size="11" text-anchor="middle">`+
		`<text x="%d" y="14">status</text><text x="%d" y="14">%s</text></g></svg>`,
		labelWidth+wordWidth, height, word, word, labelWidth+wordWidth, height,
		labelWidth, height, labelWidth, wordWidth, height, badgeColours[page.BannerState],
		labelWidth/2, labelWidth+wordWidth/2, word)
	if err != nil {
		s.log.Error("write badge", "path", r.URL.Path, "error", err)
	}
}

// groupByCategory keeps a status page's order and pulls the monitors of one
// category together, at the place of the first of them. A category's heading
// then appears once, however the page's list was arranged. Monitors without a
// category come first, so that none of them reads as part of the category above it.
func groupByCategory(placed []store.PlacedMonitor) []store.PlacedMonitor {
	order := []string{""}
	byCategory := map[string][]store.PlacedMonitor{"": nil}
	for _, m := range placed {
		if _, seen := byCategory[m.Category]; !seen {
			order = append(order, m.Category)
		}
		byCategory[m.Category] = append(byCategory[m.Category], m)
	}
	grouped := make([]store.PlacedMonitor, 0, len(placed))
	for _, category := range order {
		grouped = append(grouped, byCategory[category]...)
	}
	return grouped
}

// statusPage gathers everything the public sees about a project.
func (s *Server) statusPage(ctx context.Context, project store.Project, askedZone string) (statusPage, error) {
	placed, err := s.store.MonitorsByProject(ctx, project.ID)
	if err != nil {
		return statusPage{}, err
	}
	monitors := groupByCategory(placed)
	now := s.now()
	nowMs := now.UnixMilli()
	todayStartMs := store.DayStartMs(nowMs)
	sinceMs := todayStartMs - int64(project.HistoryDays-1)*dayMs
	rollups, err := s.store.DailyRollups(ctx, project.ID, sinceMs)
	if err != nil {
		return statusPage{}, err
	}
	incidents, err := s.store.PublishedIncidents(ctx, project.ID, sinceMs)
	if err != nil {
		return statusPage{}, err
	}
	images, err := s.store.ProjectImageVersions(ctx, project.ID)
	if err != nil {
		return statusPage{}, err
	}
	instance, err := s.store.Instance(ctx)
	if err != nil {
		return statusPage{}, err
	}
	zone, zoneName := pageZone(project, askedZone)
	maintenance, err := s.store.ActiveMaintenanceCoverage(ctx, nowMs, project.ID)
	if err != nil {
		return statusPage{}, err
	}
	coverage := maintenance[project.ID]
	windows, err := s.store.MaintenanceWindows(ctx, project.ID, nowMs)
	if err != nil {
		return statusPage{}, err
	}
	updates, err := s.store.RecentMaintenanceUpdates(ctx, windows)
	if err != nil {
		return statusPage{}, err
	}

	page := statusPage{
		Chrome: chrome{
			Title: project.Name + " status", Unbranded: !project.ShowPoweredBy, Favicon: s.projectFaviconAddress(ctx, project.Slug, images),
			Description: cmp.Or(project.Tagline, "the current status of "+project.Name+"."),
		},
		Project:  project,
		instance: instance,
		Zone:     zone,
		Images:   images,
	}
	page.ZoneName, page.Zones = zoneName, zoneChoices(project, zoneName)
	// TZ carries a visitor's own choice into links. It is empty when the page shows the project's zone.
	if zoneName != project.Timezone {
		page.TZ = zoneName
	}
	names := make(map[int64]string, len(monitors))
	for _, m := range monitors {
		names[m.ID] = m.Name
	}
	for _, w := range windows {
		view := maintenanceView{MaintenanceWindow: w, Running: w.RunningAt(nowMs), Updates: updates[w.ID]}
		for _, id := range w.MonitorIDs {
			if name, placed := names[id]; placed {
				view.Affected = append(view.Affected, name)
			}
		}
		page.Maintenance = append(page.Maintenance, view)
	}
	var contributors []store.Monitor
	excluded := make(map[int64]bool)
	lastCategory := ""
	for _, m := range monitors {
		underMaintenance := coverage.Covers(m.ID)
		if m.ExcludeFromStatus || underMaintenance {
			excluded[m.ID] = true
		}
		if !m.ExcludeFromStatus {
			contributors = append(contributors, m.Monitor)
		}
		page.LastCheckAtMs = max(page.LastCheckAtMs, m.LastCheckAtMs)
		state, word := componentState(m.Monitor)
		if underMaintenance {
			state, word = stateNone, "under maintenance"
		}
		days, uptime := uptimeStrip(rollups[m.ID], todayStartMs, project.HistoryDays)
		backfillDays(days, project.Backfill)
		view := componentView{ID: m.ID, Name: m.Name, State: state, Word: word, Days: days, Uptime: uptime, Display: m.Display}
		if m.Category != lastCategory {
			view.Category, lastCategory = m.Category, m.Category
		}
		switch m.Display {
		case "summary":
			for _, span := range []int{7, 30, project.HistoryDays} {
				_, figureUptime := uptimeStrip(rollups[m.ID], todayStartMs, span)
				view.Figures = append(view.Figures, figure{
					Label: fmt.Sprintf("last %d days", span), Value: strings.TrimSuffix(figureUptime, " uptime"),
				})
			}
		case "latency":
			view.Spark, view.SparkDays, view.Latency = latencyLine(rollups[m.ID], todayStartMs, project.HistoryDays)
			view.Area = newGraphArea(graphFill(instance, project, m.GraphFill), view.Spark, sparkHeight, fmt.Sprintf("area-%d", m.ID))
		}
		page.Components = append(page.Components, view)
	}
	var resolved []incidentView
	countedIncidents := 0
	for _, in := range incidents {
		view := newIncidentView(in, names)
		if in.ResolvedAtMs.Valid {
			resolved = append(resolved, view)
		} else {
			page.Active = append(page.Active, view)
			if !in.SourceMonitorID.Valid || !excluded[in.SourceMonitorID.Int64] {
				countedIncidents++
			}
		}
	}
	page.Recent = recentDayGroups(resolved, now, zone)
	if project.HideQuietDays {
		page.Recent = slices.DeleteFunc(page.Recent, func(g dayGroup) bool { return g.QuietDays > 0 })
	}
	page.Calm = !slices.ContainsFunc(page.Recent, func(g dayGroup) bool { return g.QuietDays == 0 })
	// Saved links were checked when they were saved, so a problem here is not expected and shows none.
	page.NavLinks, _ = parseNavLinks(project.NavLinks)
	status, counted := currentPageStatus(contributors, coverage, countedIncidents)
	page.Overall = status
	page.BannerState, page.BannerText = overallViews[status].state, overallViews[status].text
	page.BannerIcon = overallIcons[page.Overall]
	if page.Overall == overallWaiting && len(counted) == 0 && len(monitors) > 0 {
		page.BannerText = "no components affect overall status."
	}
	if len(counted) > 0 && !slices.ContainsFunc(counted, func(m store.Monitor) bool { return !m.Paused }) {
		// Nothing is being checked, which is neither up nor waiting.
		page.BannerIcon = "slash"
	}
	return page, nil
}

func componentState(m store.Monitor) (state, word string) {
	switch {
	case m.Paused:
		return stateNone, "paused"
	case m.Tracker.Status == health.Down:
		return stateDown, "down"
	case m.Tracker.Status == health.Up:
		return stateOK, "operational"
	default:
		return stateNone, "waiting for the first check"
	}
}

// overallStatus sums a status page up in one word.
type overallStatus string

const (
	overallUp       overallStatus = "up"
	overallPartial  overallStatus = "partial"
	overallDown     overallStatus = "down"
	overallIncident overallStatus = "incident"
	overallWaiting  overallStatus = "waiting"
	// overallMaintenance is set by the page, not by overall: checks rest during a window.
	overallMaintenance overallStatus = "maintenance"
)

// overallIcons names the round icon of each overall status.
var overallIcons = map[overallStatus]string{
	overallUp: "check", overallPartial: "cross", overallDown: "cross", overallIncident: "warning",
	overallWaiting: "dash", overallMaintenance: "wrench",
}

// listIcons names the round icon of the public list for the worst state among its status pages.
var listIcons = map[string]string{stateOK: "check", stateWarn: "warning", stateDown: "cross", stateNone: "dash"}

var overallViews = map[overallStatus]struct{ state, text string }{
	overallUp:          {stateOK, "everything is up."},
	overallPartial:     {stateDown, "some things are down."},
	overallDown:        {stateDown, "everything is down."},
	overallIncident:    {stateWarn, "we're looking into a problem."},
	overallWaiting:     {stateNone, "waiting for the first checks."},
	overallMaintenance: {stateNone, "planned maintenance is under way."},
}

// banner sums the page up in one sentence.
func banner(monitors []store.Monitor, activeIncidents int) (state, text string) {
	view := overallViews[overall(monitors, activeIncidents)]
	return view.state, view.text
}

// currentPageStatus counts contributors outside this page's active maintenance windows.
func currentPageStatus(contributors []store.Monitor, coverage store.MaintenanceCoverage, activeIncidents int) (overallStatus, []store.Monitor) {
	if coverage.PageWide {
		return overallMaintenance, nil
	}
	current := make([]store.Monitor, 0, len(contributors))
	for _, m := range contributors {
		if !coverage.Covers(m.ID) {
			current = append(current, m)
		}
	}
	if len(contributors) > 0 && len(current) == 0 {
		return overallMaintenance, nil
	}
	return overall(current, activeIncidents), current
}

func overall(monitors []store.Monitor, activeIncidents int) overallStatus {
	var checked, down int
	for _, m := range monitors {
		if m.Paused || m.Tracker.Status == health.Unknown {
			continue
		}
		checked++
		if m.Tracker.Status == health.Down {
			down++
		}
	}
	switch {
	case down > 0 && down == checked:
		return overallDown
	case down > 0:
		return overallPartial
	case activeIncidents > 0:
		return overallIncident
	case checked == 0:
		return overallWaiting
	default:
		return overallUp
	}
}

// projectUptimeStrip combines measured monitor time for each day in the page's window.
func projectUptimeStrip(rollups map[int64][]store.DayRollup, todayStartMs int64, historyDays int) ([]dayCell, string) {
	firstDayMs := todayStartMs - int64(historyDays-1)*dayMs
	combined := make(map[int64]store.DayRollup, historyDays)
	for _, monitorDays := range rollups {
		for _, d := range monitorDays {
			if d.DayStartMs < firstDayMs || d.DayStartMs > todayStartMs {
				continue
			}
			day := combined[d.DayStartMs]
			day.DayStartMs = d.DayStartMs
			day.CoveredMs += d.CoveredMs
			day.DownMs += d.DownMs
			combined[d.DayStartMs] = day
		}
	}
	daily := make([]store.DayRollup, 0, len(combined))
	for _, day := range combined {
		daily = append(daily, day)
	}
	return uptimeStrip(daily, todayStartMs, historyDays)
}

// uptimeStrip turns a monitor's daily rollups (oldest first) into one cell per
// day for the last historyDays days, and the uptime over those days.
func uptimeStrip(rollups []store.DayRollup, todayStartMs int64, historyDays int) ([]dayCell, string) {
	byDay := make(map[int64]store.DayRollup, len(rollups))
	var coveredMs, downMs int64
	for _, d := range rollups {
		byDay[d.DayStartMs] = d
		coveredMs += d.CoveredMs
		downMs += d.DownMs
	}
	cells := make([]dayCell, 0, historyDays)
	for i := historyDays - 1; i >= 0; i-- {
		dayStartMs := todayStartMs - int64(i)*dayMs
		date := time.UnixMilli(dayStartMs).UTC().Format("2006-01-02")
		d, checked := byDay[dayStartMs]
		switch {
		case !checked:
			cells = append(cells, dayCell{stateNone, date + ": no data"})
		case d.DownMs == 0:
			cells = append(cells, dayCell{stateOK, date + ": no downtime"})
		case d.DownMs < outageDayMs:
			cells = append(cells, dayCell{stateWarn, date + ": " + downtimeWords(d.DownMs)})
		default:
			cells = append(cells, dayCell{stateDown, date + ": " + downtimeWords(d.DownMs)})
		}
	}
	return cells, uptimePercent(coveredMs, downMs)
}

// The latency line is drawn in a box of this size, in SVG units.
const (
	sparkWidth  = 300
	sparkHeight = 32
)

// latencyLine turns daily average response times into the points of a line
// across the last historyDays days, and a caption with the overall average.
// The labels name every day's average, oldest first. A single measured day
// is drawn across its own interval so it is visible without a second sample.
func latencyLine(rollups []store.DayRollup, todayStartMs int64, historyDays int) (points string, labels []string, caption string) {
	firstDayMs := todayStartMs - int64(historyDays-1)*dayMs
	var peak, sum, days int64
	for _, d := range rollups {
		if d.DayStartMs >= firstDayMs && d.DayStartMs <= todayStartMs && d.Checks > 0 {
			peak = max(peak, d.LatencyAvgMs)
			sum += d.LatencyAvgMs
			days++
		}
	}
	labels = make([]string, historyDays)
	for i := range labels {
		date := time.UnixMilli(firstDayMs + int64(i)*dayMs).UTC().Format("2006-01-02")
		labels[i] = date + ": no data"
	}
	if days == 0 {
		return "", labels, "no response times yet"
	}
	scale := max(peak, 1)
	var b strings.Builder
	for _, d := range rollups {
		if d.DayStartMs < firstDayMs || d.DayStartMs > todayStartMs || d.Checks == 0 {
			continue
		}
		x := float64(d.DayStartMs-firstDayMs) / float64(dayMs) / float64(max(historyDays-1, 1)) * sparkWidth
		// The line keeps 2 units clear of the top and bottom edges.
		y := float64(sparkHeight-2) - float64(d.LatencyAvgMs)/float64(scale)*float64(sparkHeight-4)
		if days == 1 {
			width := float64(sparkWidth) / float64(historyDays)
			left := min(max(x-width/2, 0), sparkWidth-width)
			fmt.Fprintf(&b, "%.1f,%.1f %.1f,%.1f ", left, y, left+width, y)
		} else {
			fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
		}
		date := time.UnixMilli(d.DayStartMs).UTC().Format("2006-01-02")
		labels[(d.DayStartMs-firstDayMs)/dayMs] = fmt.Sprintf("%s: %d ms on average", date, d.LatencyAvgMs)
	}
	return strings.TrimSpace(b.String()), labels, fmt.Sprintf("%d ms on average, %d ms at most", sum/days, peak)
}

// backfillStates maps a project's backfill choice to the state its unmeasured days are drawn in.
var backfillStates = map[string]string{"up": stateOK, "degraded": stateWarn, "down": stateDown}

// backfillDays redraws the days a monitor has no data for in the state the
// project chose. The label keeps saying that nothing was measured, and the
// uptime figure is untouched: it only ever counts measured time.
func backfillDays(days []dayCell, backfill string) {
	state, chosen := backfillStates[backfill]
	if !chosen {
		return
	}
	for i := range days {
		if days[i].State == stateNone {
			days[i].State = state
			days[i].Label += ", shown as " + backfill
		}
	}
}

// uptimePercent formats uptime to two decimals, rounded down so that any
// downtime keeps the figure below 100%.
func uptimePercent(coveredMs, downMs int64) string {
	switch {
	case coveredMs <= 0:
		return "no data yet"
	case downMs <= 0:
		return "100% uptime"
	}
	upShare := 1 - float64(min(downMs, coveredMs))/float64(coveredMs)
	return fmt.Sprintf("%.2f%% uptime", math.Floor(upShare*10000)/100)
}

func downtimeWords(downMs int64) string {
	minutes := int64(math.Ceil(float64(downMs) / 60000))
	if minutes < 60 {
		return fmt.Sprintf("%d min down", minutes)
	}
	return fmt.Sprintf("%d h %d min down", minutes/60, minutes%60)
}
