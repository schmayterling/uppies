package store

import (
	"context"
	"testing"

	"uppies/internal/health"
)

func recordChange(t *testing.T, s *Store, monitorID, atMs int64, change health.Change, errorText string) {
	t.Helper()
	tracker := health.Tracker{Status: health.Up}
	if change == health.WentDown {
		tracker = health.Tracker{Status: health.Down, FailingSinceMs: atMs - 40_000}
	}
	step := health.Step{Tracker: tracker, Change: change}
	err := s.RecordCheck(context.Background(), CheckResult{MonitorID: monitorID, AtMs: atMs, Error: errorText, Step: step})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnOutageOpensADraftThatRecoveryCloses(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	projectID, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := createPlaced(ctx, s, projectID, 1)
	if err != nil {
		t.Fatal(err)
	}

	recordChange(t, s, monitorID, 100_000, health.WentDown, "status 503")
	incidents, err := s.IncidentsByProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 1 {
		t.Fatalf("got %d incidents after an outage, want 1 draft", len(incidents))
	}
	draft := incidents[0]
	if draft.Published || draft.Title != "api is down" || draft.Status != "investigating" || draft.StartedAtMs != 60_000 {
		t.Fatalf("draft: got %+v, want an unpublished \"api is down\" investigating since 60000", draft)
	}
	if len(draft.Updates) != 1 || draft.Updates[0].Body != "we are looking into a problem with api." {
		t.Fatalf("draft updates: got %+v, want one update that names the monitor and not the check error", draft.Updates)
	}
	if len(draft.MonitorIDs) != 1 || draft.MonitorIDs[0] != monitorID {
		t.Fatalf("draft monitors: got %v, want [%d]", draft.MonitorIDs, monitorID)
	}
	public, err := s.PublishedIncidents(ctx, projectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(public) != 0 {
		t.Fatalf("a draft is visible to the public: %+v", public)
	}

	recordChange(t, s, monitorID, 200_000, health.WentUp, "")
	closed, err := s.Incident(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != "resolved" || closed.ResolvedAtMs.Int64 != 200_000 || len(closed.Updates) != 2 {
		t.Fatalf("after recovery: got status=%s resolved_at=%v updates=%d, want resolved at 200000 with 2 updates",
			closed.Status, closed.ResolvedAtMs, len(closed.Updates))
	}
}

func TestRecoveryLeavesAPublishedIncidentOpen(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	projectID, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := createPlaced(ctx, s, projectID, 1)
	if err != nil {
		t.Fatal(err)
	}
	recordChange(t, s, monitorID, 100_000, health.WentDown, "status 503")
	incidents, err := s.IncidentsByProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	incidentID := incidents[0].ID
	for range 2 { // the second publish must not add a second event
		if err := s.PublishIncident(ctx, incidentID, 150_000); err != nil {
			t.Fatal(err)
		}
	}

	recordChange(t, s, monitorID, 200_000, health.WentUp, "")
	open, err := s.Incident(ctx, incidentID)
	if err != nil {
		t.Fatal(err)
	}
	if open.Status != "investigating" || open.ResolvedAtMs.Valid {
		t.Fatalf("published incident after recovery: got status=%s resolved=%v, want it left open", open.Status, open.ResolvedAtMs)
	}

	if err := s.AddIncidentUpdate(ctx, incidentID, "resolved", "a bad deploy was rolled back", 300_000); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, projectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"incident_update", "monitor_up", "incident_published", "monitor_down"}
	if len(kinds) != len(want) {
		t.Fatalf("timeline: got %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("timeline: got %v, want %v", kinds, want)
		}
	}
}

func TestAnIncidentCannotLinkAnotherProjectsMonitor(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mine, err := s.CreateProject(ctx, "mine", "Mine", 1)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.CreateProject(ctx, "theirs", "Theirs", 1)
	if err != nil {
		t.Fatal(err)
	}
	theirMonitor, err := createPlaced(ctx, s, theirs, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateIncident(ctx, Incident{
		ProjectID: mine, Title: "slow api", Impact: "minor", Status: "investigating",
		Published: true, StartedAtMs: 1, MonitorIDs: []int64{theirMonitor},
	}, "we are looking into it", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Incident(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MonitorIDs) != 0 {
		t.Fatalf("linked monitors: got %v, want none", got.MonitorIDs)
	}
}

func TestOnlyTheFirstUserCanBeCreated(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	created, err := s.CreateFirstUser(ctx, "may", "hash-1", 1)
	if err != nil || !created {
		t.Fatalf("first user: created=%v err=%v, want created", created, err)
	}
	created, err = s.CreateFirstUser(ctx, "intruder", "hash-2", 2)
	if err != nil || created {
		t.Fatalf("second user: created=%v err=%v, want refused", created, err)
	}
}

func TestSessionsExpire(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.CreateFirstUser(ctx, "may", "hash", 1); err != nil {
		t.Fatal(err)
	}
	userID, _, err := s.UserPasswordHash(ctx, "may")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "token-hash", userID, 1_000, 5_000); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, "token-hash", 4_999); err != nil || got != userID {
		t.Fatalf("before expiry: got user=%d err=%v, want user=%d", got, err, userID)
	}
	if _, err := s.SessionUser(ctx, "token-hash", 5_000); err != ErrNotFound {
		t.Fatalf("at expiry: got err=%v, want ErrNotFound", err)
	}
}
