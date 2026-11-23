package web

import (
	"testing"
	"time"

	"uppies/internal/store"
)

func incidentAt(title, impact string, at time.Time) store.Incident {
	return store.Incident{Title: title, Impact: impact, StartedAtMs: at.UnixMilli()}
}

func TestRecentDayGroupsKeepsQuietDays(t *testing.T) {
	manila, err := time.LoadLocation("Asia/Manila")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, manila)
	// 2026-11-21 20:00 UTC is already nov 22 in Manila, which is eight hours ahead.
	lateUTC := incidentView{Incident: incidentAt("slow search", "minor", time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC))}
	tooOld := incidentView{Incident: incidentAt("old outage", "major", now.AddDate(0, 0, -7))}

	groups := recentDayGroups([]incidentView{lateUTC, tooOld}, now, manila)
	if len(groups) != 7 || groups[0].Date != "nov 23, 2026" || groups[6].Date != "nov 17, 2026" {
		t.Fatalf("got %d groups from %q to %q, want 7 from nov 23 back to nov 17", len(groups), groups[0].Date, groups[len(groups)-1].Date)
	}
	for i, g := range groups {
		want := 0
		if g.Date == "nov 22, 2026" {
			want = 1
		}
		if len(g.Incidents) != want {
			t.Errorf("group %d (%s): got %d incidents, want %d", i, g.Date, len(g.Incidents), want)
		}
	}
}

func TestIncidentMap(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
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
	if cell := weeks[10][3]; !cell.Blank {
		t.Errorf("mar 11 is after today: got %+v, want blank", cell)
	}
	if cell := weeks[1][0]; cell.Blank || cell.Level != 0 || cell.Tip != "jan 4, 2026: no incidents" {
		t.Errorf("jan 4: got %+v, want a quiet day", cell)
	}
}

func TestYearDayGroupsCanHideQuietDays(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	incidents := []incidentView{
		{Incident: incidentAt("a", "minor", time.Date(2026, 1, 3, 8, 0, 0, 0, time.UTC))},
		{Incident: incidentAt("b", "critical", time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC))},
	}
	all := yearDayGroups(incidents, 2026, now, time.UTC, false)
	if len(all) != 5 || all[0].Date != "jan 5, 2026" || all[4].Date != "jan 1, 2026" {
		t.Fatalf("with quiet days: got %d groups, want jan 5 back to jan 1 and nothing after today", len(all))
	}
	if all[2].Date != "jan 3, 2026" || len(all[2].Incidents) != 2 || all[2].Level != 3 || all[1].Level != 0 {
		t.Fatalf("jan 3: got %+v, want both incidents at the critical level, and a quiet jan 4", all[2])
	}
	busy := yearDayGroups(incidents, 2026, now, time.UTC, true)
	if len(busy) != 1 || busy[0].Date != "jan 3, 2026" {
		t.Fatalf("without quiet days: got %+v, want only jan 3", busy)
	}
}
