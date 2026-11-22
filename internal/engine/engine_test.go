package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"uppies/internal/health"
	"uppies/internal/store"
)

func TestCheckOnceFollowsAnOutageFromStartToRecovery(t *testing.T) {
	ctx := context.Background()
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "uppies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	projectID, err := st.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := st.CreateMonitor(ctx, store.Monitor{
		ProjectID: projectID, Name: "api", Kind: "http", Target: server.URL,
		IntervalS: 60, TimeoutMs: 2000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.Monitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}

	e := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	steps := []struct {
		healthy    bool
		wantStatus health.Status
		wantWait   time.Duration
	}{
		{true, health.Up, 60 * time.Second},
		{false, health.Up, 20 * time.Second},
		{false, health.Up, 20 * time.Second},
		{false, health.Down, 60 * time.Second},
		{true, health.Down, 60 * time.Second},
		{true, health.Up, 60 * time.Second},
	}
	for i, s := range steps {
		healthy.Store(s.healthy)
		var wait time.Duration
		m, wait, err = e.checkOnce(ctx, m)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if m.Tracker.Status != s.wantStatus || wait != s.wantWait {
			t.Fatalf("step %d: got status=%s wait=%s, want status=%s wait=%s", i, m.Tracker.Status, wait, s.wantStatus, s.wantWait)
		}
	}

	events, err := st.Events(ctx, projectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != "monitor_up" || events[1].Kind != "monitor_down" || events[1].Detail != "status 500" {
		t.Fatalf("events: got %+v, want monitor_up then monitor_down with detail \"status 500\"", events)
	}
}

func TestCheckOnceDoesNotRecordACancelledCheck(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "uppies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	projectID, err := st.CreateProject(context.Background(), "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := st.CreateMonitor(context.Background(), store.Monitor{
		ProjectID: projectID, Name: "api", Kind: "http", Target: server.URL,
		IntervalS: 60, TimeoutMs: 5000, Retries: 0, RetryIntervalS: 20, RecoveryChecks: 1,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.Monitor(context.Background(), monitorID)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, _, err := e.checkOnce(ctx, m); err == nil {
		t.Fatal("got no error from a cancelled check, want the context error")
	}

	after, err := st.Monitor(context.Background(), monitorID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastCheckAtMs != 0 || after.Tracker.Status != health.Unknown {
		t.Fatalf("a cancelled check was recorded: last_check=%d status=%s", after.LastCheckAtMs, after.Tracker.Status)
	}
}
