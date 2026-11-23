package demo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"uppies/internal/store"
)

func TestSeedShowsEveryKindAndStateAndRefusesASecondRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if err := Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}

	monitors, err := st.Monitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, m := range monitors {
		// A day of checks every five minutes, so that a monitor's own page has something to chart.
		if m.Name == "api" {
			latencies, err := st.LatenciesSince(ctx, m.ID, now.Add(-24*time.Hour).UnixMilli())
			if err != nil || len(latencies) < 288 {
				t.Errorf("api: got %d checks in the last day err=%v, want at least 288", len(latencies), err)
			}
		}
		seen["kind "+m.Kind] = true
		seen["status "+string(m.Tracker.Status)] = true
		if m.Paused {
			seen["paused"] = true
		}
		if !m.Public {
			seen["hidden"] = true
		}
	}
	for _, want := range []string{
		"kind http", "kind tcp", "kind dns", "kind ping", "kind push",
		"status up", "status down", "status unknown", "paused", "hidden",
	} {
		if !seen[want] {
			t.Errorf("the demo data has no monitor with %s", want)
		}
	}

	hibi, err := st.ProjectBySlug(ctx, "hibi")
	if err != nil {
		t.Fatal(err)
	}
	incidents, err := st.IncidentsByProject(ctx, hibi.ID)
	if err != nil {
		t.Fatal(err)
	}
	var open, resolved, drafts int
	for _, in := range incidents {
		switch {
		case !in.Published:
			drafts++
		case in.ResolvedAtMs.Valid:
			resolved++
		default:
			open++
		}
	}
	if open != 1 || resolved != 2 || drafts != 2 {
		t.Errorf("incidents: got %d open, %d resolved, %d drafts, want 1, 2 and 2 (one draft per down monitor)", open, resolved, drafts)
	}
	channels, err := st.AlertChannels(ctx, hibi.ID)
	if err != nil || len(channels) != len(store.AlertChannelKinds) {
		t.Errorf("channels: got %d err=%v, want one of each of the %d kinds", len(channels), err, len(store.AlertChannelKinds))
	}
	billing, err := st.ProjectBySlug(ctx, "billing")
	if err != nil {
		t.Fatal(err)
	}
	if resting, err := st.InMaintenance(ctx, billing.ID, now.UnixMilli()); err != nil || !resting {
		t.Errorf("billing in maintenance: got %v err=%v, want a window running now", resting, err)
	}

	if err := Seed(ctx, st, now); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("second seed: got err=%v, want ErrNotEmpty", err)
	}
	after, err := st.Monitors(ctx)
	if err != nil || len(after) != len(monitors) {
		t.Fatalf("after the refused second seed: %d monitors err=%v, want the same %d", len(after), err, len(monitors))
	}
}
