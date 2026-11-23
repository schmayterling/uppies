package web

import (
	"testing"
	"time"

	"uppies/internal/store"
)

func incidentAt(title, impact string, at time.Time) store.Incident {
	return store.Incident{Title: title, Impact: impact, StartedAtMs: at.UnixMilli()}
}

func TestRecentDayGroupsFoldQuietRuns(t *testing.T) {
	manila, err := time.LoadLocation("Asia/Manila")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, manila)
	// 2026-11-21 20:00 UTC is already nov 22 in Manila, which is eight hours ahead.
	lateUTC := incidentView{Incident: incidentAt("slow search", "minor", time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC))}
	tooOld := incidentView{Incident: incidentAt("old outage", "major", now.AddDate(0, 0, -7))}

	groups := recentDayGroups([]incidentView{lateUTC, tooOld}, now, manila)
	// Seven days, newest first: today alone is quiet, nov 22 has the incident,
	// and the five days before it fold into one run.
	want := []struct {
		date      string
		quietDays int
		incidents int
	}{
		{"nov 23, 2026", 1, 0},
		{"nov 22, 2026", 0, 1},
		{"nov 17, 2026 to nov 21, 2026", 5, 0},
	}
	if len(groups) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(groups), len(want), groups)
	}
	for i, w := range want {
		g := groups[i]
		if g.Date != w.date || g.QuietDays != w.quietDays || len(g.Incidents) != w.incidents {
			t.Errorf("group %d: got %q with %d quiet days and %d incidents, want %q with %d and %d",
				i, g.Date, g.QuietDays, len(g.Incidents), w.date, w.quietDays, w.incidents)
		}
	}
}

func TestIncidentMap(t *testing.T) {
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC) // before noon: today must still count as a past day
	incidents := []store.Incident{
		incidentAt("a", "minor", time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)),
		incidentAt("b", "critical", time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC)),
		incidentAt("c", "major", time.Date(2026, 3, 10, 1, 0, 0, 0, time.UTC)),
	}
	weeks := incidentMap(incidents, 2026, now, time.UTC)

	// 2026 starts on a Thursday and ends on a Thursday: 53 columns of 7 days.
	if len(weeks) != 53 || len(weeks[0]) != 7 {
		t.Fatalf("got %d weeks of %d days, want 53 of 7", len(weeks), len(weeks[0]))
	}
	if cell := weeks[0][3]; !cell.Blank {
		t.Errorf("wednesday dec 31, 2025 belongs to another year: got %+v, want blank", cell)
	}
	jan1 := weeks[0][4]
	if jan1.Blank || jan1.Level != 3 || jan1.Tip != "jan 1, 2026: 2 incidents" {
		t.Errorf("jan 1: got %+v, want level 3 (the worst of minor and critical) with 2 incidents", jan1)
	}
	// March 10, 2026 is a Tuesday in the eleventh column.
	if cell := weeks[10][2]; cell.Level != 2 || cell.Tip != "mar 10, 2026: 1 incident" {
		t.Errorf("mar 10: got %+v, want level 2 with 1 incident", cell)
	}
	if cell := weeks[10][3]; !cell.Future || cell.Blank || cell.Tip != "" {
		t.Errorf("mar 11 is after today: got %+v, want a future day with no tooltip", cell)
	}
	if cell := weeks[1][0]; cell.Blank || cell.Level != 0 || cell.Tip != "jan 4, 2026: no incidents" {
		t.Errorf("jan 4: got %+v, want a quiet day", cell)
	}
}

func TestAgoLabel(t *testing.T) {
	cases := map[int]string{0: "today", 1: "yesterday", 16: "16 days ago", 59: "59 days ago", 60: "2 months ago", 400: "13 months ago", 730: "2 years ago"}
	for daysAgo, want := range cases {
		if got := agoLabel(daysAgo); got != want {
			t.Errorf("%d days ago: got %q, want %q", daysAgo, got, want)
		}
	}
}

func TestYearDayGroupsCanHideQuietDays(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	incidents := []incidentView{
		{Incident: incidentAt("a", "minor", time.Date(2026, 1, 3, 8, 0, 0, 0, time.UTC))},
		{Incident: incidentAt("b", "critical", time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC))},
	}
	all := yearDayGroups(incidents, 2026, now, time.UTC, false)
	// Nothing after today (jan 5). The quiet days fold into a run on each side of jan 3.
	if len(all) != 3 || all[0].Date != "jan 4, 2026 to jan 5, 2026" || all[0].QuietDays != 2 ||
		all[2].Date != "jan 1, 2026 to jan 2, 2026" || all[2].QuietDays != 2 {
		t.Fatalf("with quiet days: got %+v, want a two-day run, jan 3, and another two-day run", all)
	}
	if all[1].Date != "jan 3, 2026" || len(all[1].Incidents) != 2 || all[1].Level != 3 {
		t.Fatalf("jan 3: got %+v, want both incidents at the critical level", all[1])
	}
	busy := yearDayGroups(incidents, 2026, now, time.UTC, true)
	if len(busy) != 1 || busy[0].Date != "jan 3, 2026" {
		t.Fatalf("without quiet days: got %+v, want only jan 3", busy)
	}
}

func TestMonthLabels(t *testing.T) {
	labels := monthLabels(2026, time.UTC)
	// 2026 has 53 week columns. January starts in the first one, and February
	// 1 is a Sunday, which opens the sixth column.
	if len(labels) != 53 || labels[0] != "jan" || labels[1] != "" || labels[5] != "feb" {
		t.Fatalf("got %d labels starting %q, want 53 with jan first and feb sixth", len(labels), labels[:6])
	}
	named := 0
	for _, label := range labels {
		if label != "" {
			named++
		}
	}
	if named != 12 {
		t.Fatalf("got %d month names, want 12", named)
	}
}

func TestPaginate(t *testing.T) {
	groups := make([]dayGroup, 45)
	for i := range groups {
		groups[i].Date = string(rune('a' + i%26))
	}
	tests := []struct {
		name                   string
		requested              int
		wantLen                int
		wantPage, older, newer int
	}{
		{"the first page", 1, 20, 1, 2, 0},
		{"a middle page", 2, 20, 2, 3, 1},
		{"the last page holds the rest", 3, 5, 3, 0, 2},
		{"a page past the end gives the first", 9, 20, 1, 2, 0},
		{"no page given gives the first", 0, 20, 1, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shown, page, older, newer := paginate(groups, tt.requested, 20)
			if len(shown) != tt.wantLen || page != tt.wantPage || older != tt.older || newer != tt.newer {
				t.Fatalf("got %d entries on page %d (older %d, newer %d), want %d on page %d (older %d, newer %d)",
					len(shown), page, older, newer, tt.wantLen, tt.wantPage, tt.older, tt.newer)
			}
		})
	}
	if shown, page, older, newer := paginate(nil, 1, 20); len(shown) != 0 || page != 1 || older != 0 || newer != 0 {
		t.Fatalf("an empty list: got %d entries, page %d, older %d, newer %d", len(shown), page, older, newer)
	}
}
