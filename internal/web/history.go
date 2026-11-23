package web

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

// recentDays is how many days the status page lists one by one.
const recentDays = 7

// dayGroup is one calendar day and the incidents that started on it.
type dayGroup struct {
	Date string
	// Ago says how long ago the day was, such as "today" or "16 days ago".
	Ago       string
	Incidents []incidentView
	// Level is the worst impact of the day's incidents, 0 for a quiet day.
	Level int
	// QuietDays is set on a group that stands for a run of days without
	// incidents: how many days it covers. Date then names the whole run.
	QuietDays int
}

// mergeQuietRuns folds each run of consecutive quiet days into one group,
// so that a calm month reads as one line and not as thirty. The groups must
// be newest first, as the lists build them.
func mergeQuietRuns(groups []dayGroup) []dayGroup {
	var merged []dayGroup
	for _, g := range groups {
		last := len(merged) - 1
		if len(g.Incidents) > 0 || last < 0 || merged[last].QuietDays == 0 {
			if len(g.Incidents) == 0 {
				g.QuietDays = 1
			}
			merged = append(merged, g)
			continue
		}
		run := &merged[last]
		run.QuietDays++
		// The run started as its newest day. Each older day moves its start back.
		newest := run.Date
		if before, _, found := strings.Cut(run.Date, " to "); found {
			newest = run.Date[len(before)+len(" to "):]
		}
		run.Date = g.Date + " to " + newest
	}
	return merged
}

// agoLabel says how long ago a day was, in the largest unit that fits.
func agoLabel(daysAgo int) string {
	switch {
	case daysAgo <= 0:
		return "today"
	case daysAgo == 1:
		return "yesterday"
	case daysAgo < 60:
		return fmt.Sprintf("%d days ago", daysAgo)
	case daysAgo < 730:
		return fmt.Sprintf("%d months ago", daysAgo/30)
	default:
		return fmt.Sprintf("%d years ago", daysAgo/365)
	}
}

// worstLevel returns the highest impact level among incidents.
func worstLevel(incidents []incidentView) int {
	level := 0
	for _, in := range incidents {
		level = max(level, impactLevels[in.Impact])
	}
	return level
}

// yearDayGroups lists the days of a year, newest first and never past today,
// each with the incidents that started on it. With hideQuiet, days without
// incidents are left out.
func yearDayGroups(incidents []incidentView, year int, now time.Time, zone *time.Location, hideQuiet bool) []dayGroup {
	byDay := make(map[string][]incidentView)
	for _, in := range incidents {
		label := dayLabel(time.UnixMilli(in.StartedAtMs).In(zone))
		byDay[label] = append(byDay[label], in)
	}
	day := time.Date(year, time.December, 31, 12, 0, 0, 0, zone)
	if day.After(now) {
		day = time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, zone)
	}
	today := time.Date(now.In(zone).Year(), now.In(zone).Month(), now.In(zone).Day(), 12, 0, 0, 0, zone)
	var groups []dayGroup
	for ; day.Year() == year; day = day.AddDate(0, 0, -1) {
		label := dayLabel(day)
		found := byDay[label]
		if hideQuiet && len(found) == 0 {
			continue
		}
		// Both are noon, so rounding absorbs the hour a clock change adds or removes.
		daysAgo := int(math.Round(today.Sub(day).Hours() / 24))
		groups = append(groups, dayGroup{Date: label, Ago: agoLabel(daysAgo), Incidents: found, Level: worstLevel(found)})
	}
	return mergeQuietRuns(groups)
}

// heatCell is one day of the incident map. Level is 0 for a day without
// incidents, then 1, 2 and 3 for the worst impact that day.
type heatCell struct {
	Level int
	Tip   string
	// Blank marks a cell outside the year. It keeps the grid's shape.
	Blank bool
	// Future marks a day of the year that has not happened yet. It is drawn as an outline.
	Future bool
}

type historyPage struct {
	Chrome   chrome
	Project  store.Project
	Zone     *time.Location
	Images   map[string]int64
	Year     int
	PrevYear int
	NextYear int // 0 when the page shows the current year
	Weeks    [][]heatCell
	// Months holds one label per week: the month's short name on the week in
	// which the month starts, and empty otherwise.
	Months []string
	Total  int
	Days   []dayGroup
	// TZ is the visitor's own timezone choice, empty when the project's zone is shown.
	TZ string
	// Page is the page of the timeline shown, from 1. Older and Newer are the
	// neighbouring pages, 0 when there is none.
	Page, Older, Newer int
}

var impactLevels = map[string]int{"minor": 1, "major": 2, "critical": 3}

// dayLabel names a day the way the lists show it, such as "nov 23, 2026".
func dayLabel(day time.Time) string {
	return strings.ToLower(day.Format("Jan 2, 2006"))
}

// recentDayGroups lists the last recentDays days, today first, each with the
// resolved incidents that started on it. A day without incidents stays in the
// list with none, so the page can say that nothing happened.
func recentDayGroups(resolved []incidentView, now time.Time, zone *time.Location) []dayGroup {
	byDay := make(map[string][]incidentView)
	for _, in := range resolved {
		label := dayLabel(time.UnixMilli(in.StartedAtMs).In(zone))
		byDay[label] = append(byDay[label], in)
	}
	groups := make([]dayGroup, 0, recentDays)
	today := now.In(zone)
	for daysAgo := range recentDays {
		label := dayLabel(today.AddDate(0, 0, -daysAgo))
		groups = append(groups, dayGroup{Date: label, Ago: agoLabel(daysAgo), Incidents: byDay[label], Level: worstLevel(byDay[label])})
	}
	return mergeQuietRuns(groups)
}

// incidentMap builds the year's grid: one column per week, Sunday on top,
// one cell per day. Days after today are blank.
func incidentMap(incidents []store.Incident, year int, now time.Time, zone *time.Location) [][]heatCell {
	type dayTotal struct{ count, level int }
	totals := make(map[string]dayTotal)
	for _, in := range incidents {
		key := time.UnixMilli(in.StartedAtMs).In(zone).Format(time.DateOnly)
		t := totals[key]
		t.count++
		t.level = max(t.level, impactLevels[in.Impact])
		totals[key] = t
	}

	// Days are stepped at noon, so "after today" must compare against the end of today.
	endOfToday := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, zone)
	first := time.Date(year, time.January, 1, 12, 0, 0, 0, zone)
	last := time.Date(year, time.December, 31, 12, 0, 0, 0, zone)
	day := first.AddDate(0, 0, -int(first.Weekday()))
	var weeks [][]heatCell
	for !day.After(last) {
		week := make([]heatCell, 0, 7)
		for range 7 {
			cell := heatCell{Blank: day.Year() != year, Future: day.Year() == year && day.After(endOfToday)}
			if !cell.Blank && !cell.Future {
				t := totals[day.Format(time.DateOnly)]
				cell.Level = t.level
				switch t.count {
				case 0:
					cell.Tip = dayLabel(day) + ": no incidents"
				case 1:
					cell.Tip = dayLabel(day) + ": 1 incident"
				default:
					cell.Tip = fmt.Sprintf("%s: %d incidents", dayLabel(day), t.count)
				}
			}
			week = append(week, cell)
			day = day.AddDate(0, 0, 1)
		}
		weeks = append(weeks, week)
	}
	return weeks
}

// timelinePerPage is how many entries of the history timeline one page shows.
const timelinePerPage = 20

// paginate cuts a list into pages of perPage and returns the requested page
// with the numbers of its neighbours, 0 for none. A page number outside the
// list gives the first page.
func paginate(groups []dayGroup, requested, perPage int) (shown []dayGroup, page, older, newer int) {
	pages := max((len(groups)+perPage-1)/perPage, 1)
	page = requested
	if page < 1 || page > pages {
		page = 1
	}
	start := (page - 1) * perPage
	shown = groups[min(start, len(groups)):min(start+perPage, len(groups))]
	if page < pages {
		older = page + 1
	}
	if page > 1 {
		newer = page - 1
	}
	return shown, page, older, newer
}

// monthLabels names the months above the year's grid, one entry per week
// column: the short name where a month begins, empty for the other weeks.
func monthLabels(year int, zone *time.Location) []string {
	first := time.Date(year, time.January, 1, 12, 0, 0, 0, zone)
	last := time.Date(year, time.December, 31, 12, 0, 0, 0, zone)
	var labels []string
	for week := first.AddDate(0, 0, -int(first.Weekday())); !week.After(last); week = week.AddDate(0, 0, 7) {
		label := ""
		for offset := range 7 {
			day := week.AddDate(0, 0, offset)
			if day.Year() == year && day.Day() == 1 {
				label = strings.ToLower(day.Format("Jan"))
			}
		}
		labels = append(labels, label)
	}
	return labels
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project, err := s.store.ProjectBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	zone, zoneName := pageZone(project, r.URL.Query().Get("tz"))
	now := s.now().In(zone)
	// An absent or impossible year means the current one.
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil || year < 2000 || year > now.Year() {
		year = now.Year()
	}
	from := time.Date(year, time.January, 1, 0, 0, 0, 0, zone)
	incidents, err := s.store.PublishedIncidentsBetween(ctx, project.ID, from.UnixMilli(), from.AddDate(1, 0, 0).UnixMilli())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	monitors, err := s.store.MonitorsByProject(ctx, project.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	images, err := s.store.ProjectImageVersions(ctx, project.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names := make(map[int64]string, len(monitors))
	for _, m := range monitors {
		if m.Public {
			names[m.ID] = m.Name
		}
	}
	views := make([]incidentView, 0, len(incidents))
	for _, in := range incidents {
		views = append(views, newIncidentView(in, names))
	}

	page := historyPage{
		Chrome:   chrome{Title: project.Name + " incident history", Unbranded: !project.ShowPoweredBy},
		Project:  project,
		Zone:     zone,
		Images:   images,
		Year:     year,
		PrevYear: year - 1,
		Weeks:    incidentMap(incidents, year, now, zone),
		Months:   monthLabels(year, zone),
		Total:    len(incidents),
	}
	if zoneName != project.Timezone {
		page.TZ = zoneName
	}
	groups := yearDayGroups(views, year, now, zone, project.HideQuietDays)
	// A missing or impossible page number means the first page.
	requested, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page.Days, page.Page, page.Older, page.Newer = paginate(groups, requested, timelinePerPage)
	if year < now.Year() {
		page.NextYear = year + 1
	}
	s.render(w, r, http.StatusOK, "history.html", page)
}

// newIncidentView names the monitors an incident affects, leaving out hidden ones.
func newIncidentView(in store.Incident, publicNames map[int64]string) incidentView {
	view := incidentView{Incident: in}
	for _, id := range in.MonitorIDs {
		if name, public := publicNames[id]; public {
			view.Affected = append(view.Affected, name)
		}
	}
	return view
}
