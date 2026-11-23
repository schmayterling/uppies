package web

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

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
	Chrome      chrome
	Project     store.Project
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

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	project, err := s.store.ProjectBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	monitors, err := s.store.MonitorsByProject(ctx, project.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	todayStartMs := store.DayStartMs(s.now().UnixMilli())
	sinceMs := todayStartMs - int64(project.HistoryDays-1)*dayMs
	rollups, err := s.store.DailyRollups(ctx, project.ID, sinceMs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	incidents, err := s.store.PublishedIncidents(ctx, project.ID, sinceMs)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	page := statusPage{Chrome: chrome{Title: project.Name + " status"}, Project: project}
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
	page.BannerState, page.BannerText = banner(shown, len(page.Active))
	s.render(w, r, http.StatusOK, "status.html", page)
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

// banner sums the page up in one sentence.
func banner(monitors []store.Monitor, activeIncidents int) (state, text string) {
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
		return stateDown, "everything is down."
	case down > 0:
		return stateDown, "some things are down."
	case activeIncidents > 0:
		return stateWarn, "we're looking into a problem."
	case checked == 0:
		return stateNone, "waiting for the first checks."
	default:
		return stateOK, "everything is up."
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
