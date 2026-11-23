package web

import (
	"fmt"
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
	Date      string
	Incidents []incidentView
	// Level is the worst impact of the day's incidents, 0 for a quiet day.
	Level int
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
	var groups []dayGroup
	for ; day.Year() == year; day = day.AddDate(0, 0, -1) {
		label := dayLabel(day)
		found := byDay[label]
		if hideQuiet && len(found) == 0 {
			continue
		}
		groups = append(groups, dayGroup{Date: label, Incidents: found, Level: worstLevel(found)})
	}
	return groups
}

// heatCell is one day of the incident map. Level is 0 for a day without
// incidents, then 1, 2 and 3 for the worst impact that day.
type heatCell struct {
	Level int
	Tip   string
	// Blank marks a cell outside the year or in the future. It keeps the grid's shape.
	Blank bool
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
	Total    int
	Days     []dayGroup
	// HideQuiet leaves days without incidents out of the timeline.
	HideQuiet bool
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
		groups = append(groups, dayGroup{Date: label, Incidents: byDay[label], Level: worstLevel(byDay[label])})
	}
	return groups
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

	first := time.Date(year, time.January, 1, 12, 0, 0, 0, zone)
	last := time.Date(year, time.December, 31, 12, 0, 0, 0, zone)
	day := first.AddDate(0, 0, -int(first.Weekday()))
	var weeks [][]heatCell
	for !day.After(last) {
		week := make([]heatCell, 0, 7)
		for range 7 {
			cell := heatCell{Blank: day.Year() != year || day.After(now)}
			if !cell.Blank {
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

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project, err := s.store.ProjectBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	zone, err := time.LoadLocation(project.Timezone)
	if err != nil {
		zone = time.UTC // the zone was valid when saved
	}
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
		Chrome:    chrome{Title: project.Name + " incident history", Unbranded: !project.ShowPoweredBy},
		Project:   project,
		Zone:      zone,
		Images:    images,
		Year:      year,
		PrevYear:  year - 1,
		Weeks:     incidentMap(incidents, year, now, zone),
		Total:     len(incidents),
		HideQuiet: r.URL.Query().Get("quiet") == "hide",
	}
	page.Days = yearDayGroups(views, year, now, zone, page.HideQuiet)
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
