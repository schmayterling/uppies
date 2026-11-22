package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"uppies/internal/health"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "uppies.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func newMonitor(projectID int64) Monitor {
	return Monitor{
		ProjectID: projectID, Name: "api", Kind: "http", Target: "https://example.com/health",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
}

func TestRecordCheckTracksAnOutage(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const day = int64(1_800_000_000_000) - int64(1_800_000_000_000)%dayMs
	projectID, err := s.CreateProject(ctx, "acme", "Acme", day)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.CreateMonitor(ctx, newMonitor(projectID), day)
	if err != nil {
		t.Fatal(err)
	}

	policy := health.Policy{Retries: 2, RecoveryChecks: 2}
	tracker := health.Tracker{Status: health.Up}
	failureTimes := []int64{day + 1_000, day + 21_000, day + 41_000}
	for i, atMs := range failureTimes {
		var elapsedMs int64
		if i > 0 {
			elapsedMs = atMs - failureTimes[i-1]
		}
		step := health.Advance(tracker, false, atMs, elapsedMs, policy)
		err := s.RecordCheck(ctx, CheckResult{
			MonitorID: monitorID, AtMs: atMs, LatencyMs: 30, Error: "connection refused",
			CoveredMs: elapsedMs, Step: step,
		})
		if err != nil {
			t.Fatal(err)
		}
		tracker = step.Tracker
	}

	m, err := s.Monitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Tracker.Status != health.Down || m.StatusSinceMs != day+41_000 || m.LastError != "connection refused" {
		t.Fatalf("monitor: got status=%s since=%d error=%q", m.Tracker.Status, m.StatusSinceMs, m.LastError)
	}

	rollups, err := s.DailyRollups(ctx, projectID, day)
	if err != nil {
		t.Fatal(err)
	}
	want := DayRollup{DayStartMs: day, Checks: 3, Failures: 3, CoveredMs: 40_000, DownMs: 40_000}
	if got := rollups[monitorID]; len(got) != 1 || got[0] != want {
		t.Fatalf("rollups: got %+v, want [%+v]", got, want)
	}

	events, err := s.Events(ctx, projectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "monitor_down" || events[0].Title != "api" || events[0].AtMs != day+41_000 {
		t.Fatalf("events: got %+v, want one monitor_down for api", events)
	}
}

func TestDeletingAProjectRemovesItsMonitors(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	projectID, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.CreateMonitor(ctx, newMonitor(projectID), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Monitor(ctx, monitorID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("monitor after project delete: got err=%v, want ErrNotFound", err)
	}
}

func TestSchemaRejectsOutOfRangeSettings(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	projectID, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	m := newMonitor(projectID)
	m.IntervalS = 19
	if _, err := s.CreateMonitor(ctx, m, 1); err == nil {
		t.Fatal("interval of 19 seconds was accepted, want a constraint error")
	}
	m.IntervalS = 20
	if _, err := s.CreateMonitor(ctx, m, 1); err != nil {
		t.Fatalf("interval of 20 seconds: %v", err)
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "uppies.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	newer := len(migrations) + 1
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("pragma user_version = %d", newer)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err == nil {
		_ = reopened.Close() // only reached when the test already failed
		t.Fatal("opened a database with a newer schema version, want an error")
	}
}

func TestEventsPageBackwards(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	projectID, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.CreateMonitor(ctx, newMonitor(projectID), 1)
	if err != nil {
		t.Fatal(err)
	}
	changes := []health.Change{health.WentDown, health.WentUp, health.WentDown}
	for i, change := range changes {
		step := health.Step{Tracker: health.Tracker{Status: health.Up}, Change: change}
		if err := s.RecordCheck(ctx, CheckResult{MonitorID: monitorID, AtMs: int64(1000 * (i + 1)), Step: step}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Events(ctx, projectID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].AtMs != 3000 || first[1].AtMs != 2000 {
		t.Fatalf("first page: got %+v, want events at 3000 then 2000", first)
	}
	second, err := s.Events(ctx, projectID, first[1].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].AtMs != 1000 {
		t.Fatalf("second page: got %+v, want one event at 1000", second)
	}
}
