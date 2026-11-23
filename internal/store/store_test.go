package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
		ProjectID: projectID, Name: "api", Kind: "http", Target: "https://example.com/health", Public: true,
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
	failureTimes := []int64{day + 1_000, day + 21_000, day + 41_000, day + 101_000}
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
	want := DayRollup{DayStartMs: day, Checks: 4, Failures: 4, CoveredMs: 100_000, DownMs: 100_000, LatencyAvgMs: 30}
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

func TestASlugCanBeUsedOnce(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.CreateProject(ctx, "acme", "Acme", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "acme", "Another Acme", 2); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("second project with the same slug: got err=%v, want ErrSlugTaken", err)
	}
}

func TestUpgradingAVersionOneFileKeepsItsDataAndAddsDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "uppies.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	setup := migrations[0] + `
		pragma user_version = 1;
		insert into projects (id, slug, name, created_at_ms) values (7, 'acme', 'Acme', 1);
		insert into monitors (project_id, name, kind, target, interval_s, timeout_ms, retries,
			retry_interval_s, recovery_checks, status_since_ms, created_at_ms)
		values (7, 'api', 'http', 'https://example.com', 60, 5000, 2, 20, 2, 1, 1);
		insert into daily_rollups values (1, 86400000, 3, 0, 120000, 0, 90, 40);`
	if _, err := old.ExecContext(ctx, setup); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open a version 1 file: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := s.ProjectBySlug(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	want := Project{
		ID: 7, Slug: "acme", Name: "Acme", AccentHue: 200, Theme: "auto", ShowFace: true, HistoryDays: 90,
		ShowPoweredBy: true, Timezone: "UTC", Surface: "neutral", Corners: "soft", Font: "system",
	}
	if got != want {
		t.Fatalf("project after upgrade: got %+v, want %+v", got, want)
	}
	monitors, err := s.MonitorsByProject(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 1 || !monitors[0].Public || monitors[0].Name != "api" {
		t.Fatalf("monitors after upgrade: got %+v, want the one public monitor", monitors)
	}
	// The monitors table is rebuilt in step 5. Rows that point at a monitor must survive that.
	rollups, err := s.DailyRollups(ctx, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := rollups[monitors[0].ID]; len(got) != 1 || got[0].Checks != 3 {
		t.Fatalf("rollups after upgrade: got %+v, want the one day with 3 checks", got)
	}
	monitors[0].Kind, monitors[0].Target = "dns", "example.com"
	if err := s.UpdateMonitor(ctx, monitors[0]); err != nil {
		t.Fatalf("a kind added in step 5 is refused after the upgrade: %v", err)
	}
	if err := s.DeleteProject(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if left, err := s.DailyRollups(ctx, 7, 0); err != nil || len(left) != 0 {
		t.Fatalf("rollups after deleting the project: got %v err=%v, want none, so cascades still work", left, err)
	}
	if _, err := os.Stat(path + ".backup-v1"); err != nil {
		t.Fatalf("no backup was written before the upgrade: %v", err)
	}
}

func TestAnAlertChannelCanOnlyBeDeletedThroughItsProject(t *testing.T) {
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
	channelID, err := s.CreateAlertChannel(ctx, AlertChannel{ProjectID: mine, Kind: "webhook", URL: "https://example.com/hook"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAlertChannel(ctx, theirs, channelID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete through another project: got err=%v, want ErrNotFound", err)
	}
	if err := s.DeleteAlertChannel(ctx, mine, channelID); err != nil {
		t.Fatalf("delete through its own project: %v", err)
	}
}

func TestADomainBelongsToOneProject(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	for _, slug := range []string{"one", "two", "three"} {
		if _, err := s.CreateProject(ctx, slug, slug, 1); err != nil {
			t.Fatal(err)
		}
	}
	one, err := s.ProjectBySlug(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.ProjectBySlug(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	one.Domain = "status.example.com"
	if err := s.UpdateProject(ctx, one); err != nil {
		t.Fatal(err)
	}
	two.Domain = "status.example.com"
	if err := s.UpdateProject(ctx, two); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("second project on the same domain: got err=%v, want ErrDomainTaken", err)
	}
	found, err := s.ProjectByDomain(ctx, "status.example.com")
	if err != nil || found.Slug != "one" {
		t.Fatalf("lookup by domain: got %q err=%v, want project one", found.Slug, err)
	}
	if _, err := s.ProjectByDomain(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup of the empty domain: got err=%v, want ErrNotFound although two projects have none", err)
	}
}

func TestProjectImagesAreReplacedAndRemoved(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	projectID, err := s.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectImage(ctx, projectID, ImageLogo, "image/png", []byte("first"), 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectImage(ctx, projectID, ImageLogo, "image/webp", []byte("second"), 200); err != nil {
		t.Fatal(err)
	}
	contentType, data, err := s.ProjectImage(ctx, projectID, ImageLogo)
	if err != nil || contentType != "image/webp" || string(data) != "second" {
		t.Fatalf("after a replace: got %q %q err=%v, want the second upload", contentType, data, err)
	}
	versions, err := s.ProjectImageVersions(ctx, projectID)
	if err != nil || len(versions) != 1 || versions[ImageLogo] != 200 {
		t.Fatalf("versions: got %v err=%v, want only the logo at 200", versions, err)
	}
	tooLarge := make([]byte, MaxImageBytes+1)
	if err := s.SetProjectImage(ctx, projectID, ImageFavicon, "image/png", tooLarge, 300); err == nil {
		t.Fatal("an image over the size limit was stored, want a constraint error")
	}
	if err := s.DeleteProjectImage(ctx, projectID, ImageLogo); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProjectImage(ctx, projectID, ImageLogo); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after a remove: got err=%v, want ErrNotFound", err)
	}
}

func TestInstanceSettingsDefaultAndSave(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	got, err := s.Instance(ctx)
	if err != nil || got != (InstanceSettings{Name: "uppies", Theme: "auto", AccentHue: 200, PublicList: true}) {
		t.Fatalf("fresh install: got %+v err=%v, want the defaults", got, err)
	}
	want := InstanceSettings{Name: "acme status", Theme: "dark", AccentHue: 25, PublicList: false}
	if err := s.SaveInstance(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Instance(ctx); err != nil || got != want {
		t.Fatalf("after a save: got %+v err=%v, want %+v", got, err, want)
	}
}

func TestUsersCanBeAddedButTheLastOneStays(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.CreateFirstUser(ctx, "may", "hash-1", 1); err != nil {
		t.Fatal(err)
	}
	firstID, _, err := s.UserPasswordHash(ctx, "may")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, firstID); !errors.Is(err, ErrLastUser) {
		t.Fatalf("deleting the only account: got err=%v, want ErrLastUser", err)
	}
	if err := s.CreateUser(ctx, "may", "hash-2", 2); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("a second may: got err=%v, want ErrUsernameTaken", err)
	}
	if err := s.CreateUser(ctx, "sam", "hash-2", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "kept", firstID, 1, 9_000); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "other-device", firstID, 1, 9_000); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, firstID, "hash-3", "kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "kept", 2); err != nil {
		t.Errorf("the session that changed the password was ended: %v", err)
	}
	if _, err := s.SessionUser(ctx, "other-device", 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("another session survived a password change: err=%v, want ErrNotFound", err)
	}
	if err := s.DeleteUser(ctx, firstID); err != nil {
		t.Fatalf("deleting one of two accounts: %v", err)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "sam" {
		t.Fatalf("users: got %+v err=%v, want only sam", users, err)
	}
}
