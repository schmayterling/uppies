package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"uppies/internal/health"
	"uppies/internal/store"
)

func TestPageAnnouncementIsPinnedAndClearedThroughSettings(t *testing.T) {
	s := newSite(t)
	ctx := t.Context()
	id, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf("/admin/projects/%d/settings", id)
	form := url.Values{
		"name": {"Acme"}, "theme": {"light"}, "accent_hue": {"200"},
		"history_days": {"90"}, "timezone": {"UTC"},
		"announcement": {"Planned update <today>\nCheck back shortly."}, "announcement_color": {"amber"},
	}
	if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save announcement: %d", resp.StatusCode)
	}
	for _, route := range []string{"/s/acme", "/s/acme/history"} {
		_, page := s.get(t, s.anonymous, route)
		if !strings.Contains(page, `class="page-announcement announcement-amber"`) ||
			!strings.Contains(page, "Planned update &lt;today&gt;\nCheck back shortly.") {
			t.Errorf("%s does not render the escaped announcement", route)
		}
	}
	_, page := s.get(t, s.anonymous, "/s/acme")
	announcement := strings.Index(page, `class="page-announcement announcement-amber"`)
	status := strings.Index(page, `aria-label="current status"`)
	if announcement < 0 || status < 0 || announcement >= status {
		t.Error("the pinned message must appear above the overall status")
	}
	form.Set("announcement_color", "unknown")
	if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid announcement color: %d", resp.StatusCode)
	}
	form.Set("announcement_color", "amber")
	form.Set("announcement", "")
	s.post(t, s.admin, settings, form, nil)
	_, page = s.get(t, s.anonymous, "/s/acme")
	if strings.Contains(page, "page-announcement") {
		t.Error("clearing the message must remove the announcement")
	}
}

func TestUptimePercent(t *testing.T) {
	const day = 86_400_000
	tests := []struct {
		name      string
		coveredMs int64
		downMs    int64
		want      string
	}{
		{"no checks yet", 0, 0, "no data yet"},
		{"no downtime", day, 0, "100% uptime"},
		{"one percent down", day, 864_000, "99.00% uptime"},
		{"one millisecond down in thirty days stays below 100", 30 * day, 1, "99.99% uptime"},
		{"down the whole time", day, day, "0.00% uptime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uptimePercent(tt.coveredMs, tt.downMs); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUptimeStrip(t *testing.T) {
	const today = int64(20_000) * dayMs
	rollups := []store.DayRollup{
		{DayStartMs: today - 2*dayMs, CoveredMs: dayMs, DownMs: 3_600_000},
		{DayStartMs: today - dayMs, CoveredMs: dayMs, DownMs: 3_599_999},
		{DayStartMs: today, CoveredMs: dayMs, DownMs: 0},
	}
	if cells, _ := uptimeStrip(rollups, today, 30); len(cells) != 30 {
		t.Fatalf("got %d cells for 30 days of history, want 30", len(cells))
	}
	cells, _ := uptimeStrip(rollups, today, 90)
	if len(cells) != 90 {
		t.Fatalf("got %d cells, want 90", len(cells))
	}
	want := []dayCell{
		{stateNone, "2024-10-01: no data"},
		{stateDown, "2024-10-02: 1 h 0 min down"},
		{stateWarn, "2024-10-03: 1 h 0 min down"},
		{stateOK, "2024-10-04: no downtime"},
	}
	for i, w := range want {
		if got := cells[86+i]; got != w {
			t.Fatalf("cell %d: got %+v, want %+v", 86+i, got, w)
		}
	}
}

func TestBanner(t *testing.T) {
	up := store.Monitor{Tracker: health.Tracker{Status: health.Up}}
	down := store.Monitor{Tracker: health.Tracker{Status: health.Down}}
	pausedDown := store.Monitor{Paused: true, Tracker: health.Tracker{Status: health.Down}}
	unchecked := store.Monitor{Tracker: health.Tracker{Status: health.Unknown}}
	tests := []struct {
		name            string
		monitors        []store.Monitor
		activeIncidents int
		wantState       string
		wantText        string
	}{
		{"all up", []store.Monitor{up, up}, 0, stateOK, "everything is up."},
		{"one of two down", []store.Monitor{up, down}, 0, stateDown, "some things are down."},
		{"all down", []store.Monitor{down, down}, 0, stateDown, "everything is down."},
		{"a paused monitor does not count", []store.Monitor{up, pausedDown}, 0, stateOK, "everything is up."},
		{"up with an open incident", []store.Monitor{up}, 1, stateWarn, "we're looking into a problem."},
		{"nothing checked yet", []store.Monitor{unchecked}, 0, stateNone, "waiting for the first checks."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, text := banner(tt.monitors, tt.activeIncidents)
			if state != tt.wantState || text != tt.wantText {
				t.Fatalf("got %s %q, want %s %q", state, text, tt.wantState, tt.wantText)
			}
		})
	}
}

func TestEveryOfferedZoneLoads(t *testing.T) {
	if len(offeredZones) < 400 {
		t.Fatalf("got %d zones, want the whole database", len(offeredZones))
	}
	for _, name := range offeredZones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestIndexSummaryCountsThePagesWithAProblem(t *testing.T) {
	cases := []struct {
		states        []string
		state, phrase string
	}{
		{[]string{stateOK, stateNone}, stateOK, "every status page is fine."},
		{[]string{stateOK, stateWarn}, stateWarn, "1 of 2 status pages has a problem."},
		{[]string{stateDown, stateWarn, stateOK}, stateDown, "2 of 3 status pages have a problem."},
	}
	for _, c := range cases {
		var projects []indexProject
		for _, state := range c.states {
			projects = append(projects, indexProject{State: state})
		}
		if state, phrase := indexSummary(projects); state != c.state || phrase != c.phrase {
			t.Errorf("%v: got %q %q, want %q %q", c.states, state, phrase, c.state, c.phrase)
		}
	}
}

func TestLatencyLine(t *testing.T) {
	const today = int64(20_000) * dayMs
	rollups := []store.DayRollup{
		{DayStartMs: today - 2*dayMs, Checks: 10, LatencyAvgMs: 100},
		{DayStartMs: today - dayMs, Checks: 0},
		{DayStartMs: today, Checks: 10, LatencyAvgMs: 50},
	}
	points, labels, caption := latencyLine(rollups, today, 3)
	// Three days span the box of 300 by 32. The slowest day touches the top
	// margin (y 2) and a day at half that speed sits halfway down (y 16).
	if points != "0.0,2.0 300.0,16.0" || caption != "75 ms on average, 100 ms at most" {
		t.Fatalf("got points %q caption %q", points, caption)
	}
	// One label per day, oldest first. The day without checks says so.
	wantLabels := []string{"2024-10-02: 100 ms on average", "2024-10-03: no data", "2024-10-04: 50 ms on average"}
	if !slices.Equal(labels, wantLabels) {
		t.Fatalf("got labels %q, want %q", labels, wantLabels)
	}
	if points, labels, caption := latencyLine(nil, today, 3); points != "" || len(labels) != 3 || caption != "no response times yet" {
		t.Fatalf("no data: got points %q, labels %q, caption %q", points, labels, caption)
	}
	points, _, caption = latencyLine(rollups[2:], today, 3)
	if points != "200.0,2.0 300.0,2.0" || caption != "50 ms on average, 50 ms at most" {
		t.Fatalf("one data point: got points %q, caption %q", points, caption)
	}
}
