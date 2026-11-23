package web

import (
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
	Chrome  chrome
	Project store.Project
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
	BannerText  string
	Components  []componentView
	Active      []incidentView
	// Recent lists the last few days, today first, with the resolved incidents of each.
	Recent []dayGroup
	// Maintenance holds the running and upcoming windows, soonest first.
	Maintenance []maintenanceView
}

type maintenanceView struct {
	store.MaintenanceWindow
	Running bool
}

type indexPage struct {
	Chrome   chrome
	Projects []indexProject
	// Listed is false when the instance keeps its status pages off the front page.
	Listed bool
	// Layout is the instance's choice of how the list is drawn.
	Layout string
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
	monitors, err := s.store.Monitors(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Only what the public may see counts: hidden monitors are left out.
	visible := make(map[int64][]store.Monitor)
	for _, m := range monitors {
		if m.Public {
			visible[m.ProjectID] = append(visible[m.ProjectID], m)
		}
	}
	instance, err := s.store.Instance(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	logos, err := s.store.LogoVersions(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := indexPage{
		Chrome: chrome{Title: "status", Auth: instance.ListLayout == "compact", SignInLink: true, Brand: instance.Name},
		Listed: instance.PublicList, Layout: instance.ListLayout,
	}
	if !instance.PublicList {
		projects = nil
	}
	for _, p := range projects {
		state, _ := banner(visible[p.ID], 0)
		page.Projects = append(page.Projects, indexProject{
			Project: p, State: state, Word: indexWords[state], Count: upCount(visible[p.ID]), LogoVersion: logos[p.ID],
		})
	}
	page.State, page.Summary = indexSummary(page.Projects)
	s.render(w, r, http.StatusOK, "index.html", page)
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

// statusPage gathers everything the public sees about a project.
func (s *Server) statusPage(ctx context.Context, project store.Project, askedZone string) (statusPage, error) {
	monitors, err := s.store.MonitorsByProject(ctx, project.ID)
	if err != nil {
		return statusPage{}, err
	}
	todayStartMs := store.DayStartMs(s.now().UnixMilli())
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
	zone, zoneName := pageZone(project, askedZone)

	page := statusPage{
		Chrome:  chrome{Title: project.Name + " status", Unbranded: !project.ShowPoweredBy},
		Project: project,
		Zone:    zone,
		Images:  images,
	}
	page.ZoneName, page.Zones = zoneName, zoneChoices(project, zoneName)
	// TZ carries a visitor's own choice into links. It is empty when the page shows the project's zone.
	if zoneName != project.Timezone {
		page.TZ = zoneName
	}
	names := make(map[int64]string, len(monitors))
	var shown []store.Monitor
	lastCategory := ""
	for _, m := range monitors {
		if !m.Public {
			continue
		}
		shown = append(shown, m)
		names[m.ID] = m.Name
		state, word := componentState(m)
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
		}
		page.Components = append(page.Components, view)
	}
	var resolved []incidentView
	for _, in := range incidents {
		view := newIncidentView(in, names)
		if in.ResolvedAtMs.Valid {
			resolved = append(resolved, view)
		} else {
			page.Active = append(page.Active, view)
		}
	}
	page.Recent = recentDayGroups(resolved, s.now(), zone)
	if project.HideQuietDays {
		page.Recent = slices.DeleteFunc(page.Recent, func(g dayGroup) bool { return g.QuietDays > 0 })
	}
	page.Calm = !slices.ContainsFunc(page.Recent, func(g dayGroup) bool { return g.QuietDays == 0 })
	// Saved links were checked when they were saved, so a problem here is not expected and shows none.
	page.NavLinks, _ = parseNavLinks(project.NavLinks)
	nowMs := s.now().UnixMilli()
	windows, err := s.store.MaintenanceWindows(ctx, project.ID, nowMs)
	if err != nil {
		return statusPage{}, err
	}
	resting := false
	for _, w := range windows {
		running := w.StartsAtMs <= nowMs
		resting = resting || running
		page.Maintenance = append(page.Maintenance, maintenanceView{MaintenanceWindow: w, Running: running})
	}
	page.Overall = overall(shown, len(page.Active))
	if resting {
		page.Overall = overallMaintenance
		for i := range page.Components {
			page.Components[i].State, page.Components[i].Word = stateNone, "under maintenance"
		}
	}
	page.BannerState, page.BannerText = overallViews[page.Overall].state, overallViews[page.Overall].text
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
// Days without checks leave a gap in time but not in the line. The labels
// name every day's average, oldest first.
func latencyLine(rollups []store.DayRollup, todayStartMs int64, historyDays int) (points string, labels []string, caption string) {
	firstDayMs := todayStartMs - int64(historyDays-1)*dayMs
	var peak, sum, days int64
	for _, d := range rollups {
		if d.DayStartMs >= firstDayMs && d.Checks > 0 {
			peak = max(peak, d.LatencyAvgMs)
			sum += d.LatencyAvgMs
			days++
		}
	}
	if days == 0 {
		return "", nil, "no response times yet"
	}
	labels = make([]string, historyDays)
	for i := range labels {
		date := time.UnixMilli(firstDayMs + int64(i)*dayMs).UTC().Format("2006-01-02")
		labels[i] = date + ": no data"
	}
	peak = max(peak, 1)
	var b strings.Builder
	for _, d := range rollups {
		if d.DayStartMs < firstDayMs || d.Checks == 0 {
			continue
		}
		x := float64(d.DayStartMs-firstDayMs) / float64(dayMs) / float64(max(historyDays-1, 1)) * sparkWidth
		// The line keeps 2 units clear of the top and bottom edges.
		y := float64(sparkHeight-2) - float64(d.LatencyAvgMs)/float64(peak)*float64(sparkHeight-4)
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
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
