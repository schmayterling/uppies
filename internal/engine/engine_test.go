package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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
	var alerts struct {
		sync.Mutex
		texts []string
	}
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		alerts.Lock()
		alerts.texts = append(alerts.texts, body.Text)
		alerts.Unlock()
	}))
	defer chat.Close()
	if _, err := st.CreateAlertChannel(ctx, store.AlertChannel{ProjectID: projectID, Kind: "slack", URL: chat.URL}, 1); err != nil {
		t.Fatal(err)
	}
	m, err := st.Monitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}

	e := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

	e.alerts.Wait()
	alerts.Lock()
	defer alerts.Unlock()
	if len(alerts.texts) != 2 || alerts.texts[0] != "api is down: status 500. (Acme)" ||
		alerts.texts[1] != "api is up again after under a minute. (Acme)" {
		t.Fatalf("alerts: got %q, want one for the outage and one for the recovery", alerts.texts)
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
	e := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

func TestHeartbeatKeepsRequestingTheURL(t *testing.T) {
	beats := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beats <- struct{}{}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Heartbeat(ctx, server.URL, 5*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	for range 3 {
		<-beats
	}
	cancel()
	<-done
}

func TestPushResult(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	tests := []struct {
		name      string
		silence   time.Duration
		wantOK    bool
		wantError string
	}{
		{"reported within the interval", 50 * time.Second, true, ""},
		{"exactly at the interval plus the grace", 70 * time.Second, true, ""},
		{"one second past the grace", 71 * time.Second, false, "no report for 1m11s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pushResult(now.Add(-tt.silence).UnixMilli(), now, time.Minute)
			if got.OK != tt.wantOK || got.Error != tt.wantError {
				t.Fatalf("got ok=%v error=%q, want ok=%v error=%q", got.OK, got.Error, tt.wantOK, tt.wantError)
			}
		})
	}
}

func TestCertWarningDue(t *testing.T) {
	tests := []struct {
		name       string
		daysLeft   int
		warnedDays int
		want       int
	}{
		{"plenty of time", 15, 0, 0},
		{"first warning at 14 days", 14, 0, 14},
		{"no repeat inside the same threshold", 10, 14, 0},
		{"next warning at 7 days", 7, 14, 7},
		{"a first look at 2 days warns at the 3-day threshold", 2, 0, 3},
		{"last warning at 1 day", 1, 3, 1},
		{"no repeat after the last warning", 0, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := certWarningDue(tt.daysLeft, tt.warnedDays); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
