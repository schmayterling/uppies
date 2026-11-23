package store

import (
	"context"
	"testing"
)

func TestScopedMaintenanceDoesNotBroadenAfterItsMonitorIsRemoved(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	page, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := createPlaced(ctx, s, page, 1)
	if err != nil {
		t.Fatal(err)
	}
	uncovered, err := createPlaced(ctx, s, page, 1)
	if err != nil {
		t.Fatal(err)
	}
	windowID, err := s.CreateMaintenanceWindow(ctx, MaintenanceWindow{
		ProjectID: page, Title: "upgrade", StartsAtMs: 10, UntilFurtherNotice: true, MonitorIDs: []int64{selected},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	isResting := func(monitorID, atMs int64, want bool) {
		t.Helper()
		got, err := s.InMaintenance(ctx, monitorID, atMs)
		if err != nil || got != want {
			t.Fatalf("maintenance for monitor %d at %d: got %v (%v), want %v", monitorID, atMs, got, err, want)
		}
	}

	isResting(selected, 9, false)
	isResting(selected, 100, true)
	isResting(uncovered, 100, false)
	if err := s.SetPlacements(ctx, page, []Placement{{MonitorID: uncovered}}); err != nil {
		t.Fatal(err)
	}
	isResting(selected, 100, false)
	if err := s.DeleteMonitor(ctx, selected); err != nil {
		t.Fatal(err)
	}
	window, err := s.MaintenanceWindow(ctx, page, windowID)
	if err != nil || !window.Scoped || len(window.MonitorIDs) != 0 {
		t.Fatalf("window after monitor deletion: %+v (%v), want an empty scoped window", window, err)
	}
	isResting(uncovered, 100, false)
}
