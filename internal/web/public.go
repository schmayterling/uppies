package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
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
	Name   string
	State  string
	Word   string
	Days   []dayCell
	Uptime string
}

type incidentView struct {
	store.Incident
	Affected []string
}

type statusPage struct {
	Chrome  chrome
	Project store.Project
	// Zone is the timezone that incident times are shown in.
	Zone        *time.Location
	Overall     overallStatus
	BannerState string
	BannerText  string
	Components  []componentView
	Active      []incidentView
	Past        []incidentView
}

type indexPage struct {
	Chrome   chrome
	Projects []store.Project
}

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
	s.render(w, r, http.StatusOK, "index.html", indexPage{Chrome: chrome{Title: "status"}, Projects: projects})
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
	return fmt.Sprintf(":root {\n  color-scheme: %s;\n  --accent: light-dark(oklch(0.45 0.075 %d), oklch(0.8 0.075 %d));\n}\n%s\n",
		scheme, p.AccentHue, p.AccentHue, p.CustomCSS)
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
	page, err := s.statusPage(r.Context(), project)
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
	overallIncident: "incident", overallWaiting: "unknown",
}

// handleBadge serves a project's current state as a small image for readmes and other sites.
func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := s.statusPage(r.Context(), project)
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
func (s *Server) statusPage(ctx context.Context, project store.Project) (statusPage, error) {
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
	zone, err := time.LoadLocation(project.Timezone)
	if err != nil {
		// The zone was valid when it was saved. A system without that zone falls back to UTC.
		zone = time.UTC
	}

	page := statusPage{
		Chrome:  chrome{Title: project.Name + " status", Unbranded: !project.ShowPoweredBy},
		Project: project,
		Zone:    zone,
	}
	names := make(map[int64]string, len(monitors))
	var shown []store.Monitor
	for _, m := range monitors {
		if !m.Public {
			continue
		}
		shown = append(shown, m)
		names[m.ID] = m.Name
		state, word := componentState(m)
		days, uptime := uptimeStrip(rollups[m.ID], todayStartMs, project.HistoryDays)
		page.Components = append(page.Components, componentView{Name: m.Name, State: state, Word: word, Days: days, Uptime: uptime})
	}
	for _, in := range incidents {
		view := incidentView{Incident: in}
		for _, id := range in.MonitorIDs {
			if name, public := names[id]; public {
				view.Affected = append(view.Affected, name)
			}
		}
		if in.ResolvedAtMs.Valid {
			page.Past = append(page.Past, view)
		} else {
			page.Active = append(page.Active, view)
		}
	}
	page.Overall = overall(shown, len(page.Active))
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
)

var overallViews = map[overallStatus]struct{ state, text string }{
	overallUp:       {stateOK, "everything is up."},
	overallPartial:  {stateDown, "some things are down."},
	overallDown:     {stateDown, "everything is down."},
	overallIncident: {stateWarn, "we're looking into a problem."},
	overallWaiting:  {stateNone, "waiting for the first checks."},
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
