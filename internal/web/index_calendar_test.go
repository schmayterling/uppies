package web

import (
	"bytes"
	"html/template"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uppies/internal/store"
)

func TestLoadIndexCalendarCountsEveryPublishedIncident(t *testing.T) {
	grid := template.Must(template.ParseFS(templateFiles, "templates/partials/incident_calendar.html"))
	for _, backend := range []store.Backend{store.SQLite, store.TursoSQLite} {
		t.Run(string(backend), func(t *testing.T) {
			ctx := t.Context()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			st, err := store.OpenBackend(ctx, backend, filepath.Join(t.TempDir(), "test.db"), "")
			check(err)
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})

			jan1 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			jan2 := jan1.AddDate(0, 0, 1)
			manila := time.FixedZone("Manila", 8*60*60)
			now := jan2.Add(20 * time.Hour).In(manila) // Jan 3 locally, still Jan 2 UTC.
			core, err := st.CreateProject(ctx, "core", "Core", jan1.UnixMilli())
			check(err)
			edge, err := st.CreateProject(ctx, "edge", "Edge", jan1.UnixMilli())
			check(err)

			create := func(projectID int64, impact, status string, at time.Time, published bool) {
				t.Helper()
				_, err := st.CreateIncident(ctx, store.Incident{
					ProjectID: projectID, Title: "Incident", Impact: impact, Status: status,
					StartedAtMs: at.UnixMilli(), Published: published,
				}, "Incident details", at.UnixMilli())
				check(err)
			}
			// The oldest incident of the day has the highest impact. It would
			// be omitted by either the 20-row archive or 200-row store limit.
			create(edge, "critical", "resolved", jan1, true)
			lateJan1 := jan2.Add(-30 * time.Minute)
			create(edge, "minor", "investigating", lateJan1, true)
			for range 200 {
				create(core, "minor", "resolved", lateJan1, true)
			}
			create(core, "major", "monitoring", jan2, true)
			create(edge, "critical", "investigating", jan2, false)
			create(edge, "critical", "resolved", jan1.Add(-time.Millisecond), true)
			create(core, "critical", "resolved", jan1.AddDate(1, 0, 0), true)

			s := &Server{store: st}
			calendar, err := s.loadIndexCalendar(ctx, 2026, now)
			check(err)
			if calendar.Total != 203 {
				t.Errorf("total = %d, want 203 public incidents in 2026 UTC", calendar.Total)
			}

			// Jan 1–3, 2026 fall on Thursday–Saturday in the first week.
			want := []heatCell{
				{Level: 3, Tip: "jan 1, 2026: 202 incidents"},
				{Level: 2, Tip: "jan 2, 2026: 1 incident"},
				{Future: true},
			}
			firstWeek := calendar.Weeks[0]
			for i, cell := range want {
				if got := firstWeek[4+i]; got != cell {
					t.Errorf("jan %d: got %+v, want %+v", i+1, got, cell)
				}
			}

			history := historyPage{Year: calendar.Year, Months: calendar.Months, Weeks: calendar.Weeks, Total: calendar.Total}
			for _, data := range []any{calendar, history} {
				var html bytes.Buffer
				check(grid.ExecuteTemplate(&html, "incident-calendar", data))
				if !strings.Contains(html.String(), `role="img" aria-label="203 incidents in 2026. each square is a day."`) {
					t.Errorf("%T: calendar image has no accessible yearly total", data)
				}
			}
		})
	}
}
