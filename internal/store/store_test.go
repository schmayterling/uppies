package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func newMonitor() Monitor {
	return Monitor{
		Name: "api", Kind: "http", Target: "https://example.com/health",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
}

// createPlaced adds a monitor and puts it on a status page.
func createPlaced(ctx context.Context, s *Store, projectID, nowMs int64) (int64, error) {
	id, err := s.CreateMonitor(ctx, newMonitor(), nowMs)
	if err != nil {
		return 0, err
	}
	return id, s.PlaceMonitor(ctx, projectID, id)
}

func TestRecordCheckTracksAnOutage(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const day = int64(1_800_000_000_000) - int64(1_800_000_000_000)%dayMs
	projectID, err := s.CreateProject(ctx, "acme", "Acme", day)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := createPlaced(ctx, s, projectID, day)
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

func TestDeletingAProjectKeepsItsMonitorsAndDropsTheirPlaces(t *testing.T) {
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
	if err := s.DeleteProject(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	// Monitors belong to the install since step 15, so only the place on the page goes.
	if _, err := s.Monitor(ctx, monitorID); err != nil {
		t.Fatalf("monitor after project delete: got err=%v, want it to stay", err)
	}
	if placed, err := s.PlacedMonitors(ctx); err != nil || len(placed) != 0 {
		t.Fatalf("places after project delete: got %+v err=%v, want none", placed, err)
	}
}

func TestSchemaRejectsOutOfRangeSettings(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	m := newMonitor()
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
	monitorID, err := createPlaced(ctx, s, projectID, 1)
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
		ShowPoweredBy: true, Timezone: "UTC", Surface: "neutral", Corners: "soft", Font: "system", Backfill: "none",
		NavbarBrand: "icon-name",
	}
	if got != want {
		t.Fatalf("project after upgrade: got %+v, want %+v", got, want)
	}
	monitors, err := s.MonitorsByProject(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	// Step 15 puts each monitor on the status page it used to belong to.
	if len(monitors) != 1 || monitors[0].Name != "api" || monitors[0].Position != 1 || monitors[0].Display != "bars" {
		t.Fatalf("monitors after upgrade: got %+v, want the one monitor, first on its page", monitors)
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
	if err := s.UpdateMonitor(ctx, monitors[0].Monitor); err != nil {
		t.Fatalf("a kind added in step 5 is refused after the upgrade: %v", err)
	}
	if err := s.DeleteProject(ctx, 7); err != nil {
		t.Fatal(err)
	}
	// A monitor belongs to the install, so it outlives a status page that showed it.
	if left, err := s.Monitors(ctx); err != nil || len(left) != 1 {
		t.Fatalf("monitors after deleting the project: got %v err=%v, want the monitor to stay", left, err)
	}
	if kept, err := s.MonitorRollups(ctx, monitors[0].ID, 0); err != nil || len(kept) != 1 {
		t.Fatalf("rollups after deleting the project: got %v err=%v, want the monitor's history to stay", kept, err)
	}
	if _, err := os.Stat(path + ".backup-v1"); err != nil {
		t.Fatalf("no backup was written before the upgrade: %v", err)
	}
}

func TestAMonitorAlertsTheChannelsItNames(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	first, err := s.CreateMonitor(ctx, newMonitor(), 1)
	if err != nil {
		t.Fatal(err)
	}
	// A new channel is alerted by every monitor there is.
	hook, err := s.CreateAlertChannel(ctx, AlertChannel{Kind: "webhook", URL: "https://example.com/hook"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := s.CreateAlertChannel(ctx, AlertChannel{Kind: "email", URL: "ops@example.com"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if channels, err := s.MonitorChannels(ctx, first); err != nil || len(channels) != 2 {
		t.Fatalf("channels of a monitor that was there first: got %+v err=%v, want both", channels, err)
	}
	// A monitor added later names its own. An id that is no channel is left out.
	second, err := s.CreateMonitor(ctx, newMonitor(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMonitorChannels(ctx, second, []int64{mail, 999}); err != nil {
		t.Fatal(err)
	}
	channels, err := s.MonitorChannels(ctx, second)
	if err != nil || len(channels) != 1 || channels[0].ID != mail {
		t.Fatalf("channels of the second monitor: got %+v err=%v, want only the email one", channels, err)
	}
	if err := s.DeleteAlertChannel(ctx, mail); err != nil {
		t.Fatal(err)
	}
	if channels, err := s.MonitorChannels(ctx, second); err != nil || len(channels) != 0 {
		t.Fatalf("after deleting its channel: got %+v err=%v, want none", channels, err)
	}
	if all, err := s.AlertChannels(ctx); err != nil || len(all) != 1 || all[0].ID != hook {
		t.Fatalf("all channels: got %+v err=%v, want the webhook", all, err)
	}
	if err := s.DeleteAlertChannel(ctx, mail); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting it twice: got err=%v, want ErrNotFound", err)
	}
}

func TestAStatusPageListsMonitorsInItsOwnOrderAndStyle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	one, err := s.CreateProject(ctx, "one", "One", 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.CreateProject(ctx, "two", "Two", 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for range 3 {
		id, err := createPlaced(ctx, s, one, 1)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Placing a monitor twice changes nothing. The same monitor can be on two pages.
	if err := errors.Join(s.PlaceMonitor(ctx, one, ids[0]), s.PlaceMonitor(ctx, two, ids[0])); err != nil {
		t.Fatal(err)
	}
	want := []Placement{
		{MonitorID: ids[2], Display: "latency", GraphFill: "filled"},
		{MonitorID: ids[0]},
	}
	if err := s.SetPlacements(ctx, one, want); err != nil {
		t.Fatal(err)
	}
	placed, err := s.MonitorsByProject(ctx, one)
	if err != nil || len(placed) != 2 {
		t.Fatalf("page one: got %+v err=%v, want two monitors", placed, err)
	}
	if placed[0].MonitorID != ids[2] || placed[0].Display != "latency" || placed[0].GraphFill != "filled" ||
		placed[1].MonitorID != ids[0] || placed[1].Display != "bars" || placed[1].Position != 2 {
		t.Fatalf("page one: got %+v, want the saved order and styles, with bars where none was given", placed)
	}
	if other, err := s.MonitorsByProject(ctx, two); err != nil || len(other) != 1 || other[0].MonitorID != ids[0] {
		t.Fatalf("page two: got %+v err=%v, want it untouched", other, err)
	}
	// Maintenance on any page that shows a monitor rests it. A monitor on no page never rests.
	if _, err := s.CreateMaintenanceWindow(ctx, MaintenanceWindow{ProjectID: two, Title: "move", StartsAtMs: 10, EndsAtMs: 20}, 1); err != nil {
		t.Fatal(err)
	}
	for id, wantResting := range map[int64]bool{ids[0]: true, ids[1]: false, ids[2]: false} {
		if resting, err := s.InMaintenance(ctx, id, 15); err != nil || resting != wantResting {
			t.Errorf("monitor %d during page two's maintenance: got %v err=%v, want %v", id, resting, err, wantResting)
		}
	}
	// Each status page has its own categories. Page one names two, page two has none.
	layout := []Placement{{MonitorID: ids[1], Category: "core"}, {MonitorID: ids[0], Category: "web", Display: "minimal"}}
	if err := s.SetPageLayout(ctx, one, []string{"web", "empty one", "web"}, layout); err != nil {
		t.Fatal(err)
	}
	if names, err := s.PageCategories(ctx, one); err != nil || !slices.Equal(names, []string{"web", "empty one", "core"}) {
		t.Fatalf("categories of page one: got %v err=%v, want the listed ones once each, then the one only a monitor names", names, err)
	}
	if names, err := s.PageCategories(ctx, two); err != nil || len(names) != 0 {
		t.Fatalf("categories of page two: got %v err=%v, want none", names, err)
	}
	placed, err = s.MonitorsByProject(ctx, one)
	if err != nil || len(placed) != 2 || placed[0].Category != "core" || placed[1].Category != "web" {
		t.Fatalf("page one after the layout: got %+v err=%v, want the two monitors in their categories", placed, err)
	}
	// Saving only the monitors keeps the page's categories, the empty one included.
	if err := s.SetPlacements(ctx, one, []Placement{{MonitorID: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	if names, err := s.PageCategories(ctx, one); err != nil || !slices.Equal(names, []string{"web", "empty one", "core"}) {
		t.Fatalf("categories after saving only the monitors: got %v err=%v, want them kept", names, err)
	}
	if names, err := s.PageNames(ctx, ids[0]); err != nil || !slices.Equal(names, []string{"One", "Two"}) {
		t.Fatalf("pages that show the first monitor: got %v err=%v, want One and Two", names, err)
	}
	// The list of all monitors has an order of its own. A monitor that is not named goes behind.
	if err := s.SetMonitorOrder(ctx, []int64{ids[2], ids[0]}); err != nil {
		t.Fatal(err)
	}
	arranged, err := s.Monitors(ctx)
	if err != nil || len(arranged) != 3 || arranged[0].ID != ids[2] || arranged[1].ID != ids[0] || arranged[2].ID != ids[1] {
		t.Fatalf("monitors after arranging: got %+v err=%v, want the third, the first, then the one not named", arranged, err)
	}
	// Status pages keep the order they are given.
	if err := s.SetProjectOrder(ctx, []int64{two, one}); err != nil {
		t.Fatal(err)
	}
	if projects, err := s.Projects(ctx); err != nil || len(projects) != 2 || projects[0].ID != two {
		t.Fatalf("projects after reordering: got %+v err=%v, want page two first", projects, err)
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
	if err != nil || got != (InstanceSettings{Name: "uppies", Theme: "auto", AccentHue: 200, PublicList: true, LoginLayout: "split", LoginBackground: "gradient", ListLayout: "rows", GraphFill: "gradient"}) {
		t.Fatalf("fresh install: got %+v err=%v, want the defaults", got, err)
	}
	want := InstanceSettings{Name: "acme status", Theme: "dark", AccentHue: 25, PublicList: false, LoginLayout: "centered", LoginBackground: "image", ListLayout: "cards", GraphFill: "none"}
	if err := s.SaveInstance(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Instance(ctx); err != nil || got != want {
		t.Fatalf("after a save: got %+v err=%v, want %+v", got, err, want)
	}
}

func TestCheckQueriesForTheDetailView(t *testing.T) {
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
	// Two checks in the first half hour, one of them failed, and one in the second.
	const half = int64(30 * 60 * 1000)
	checks := []CheckResult{
		{MonitorID: monitorID, AtMs: half + 1, OK: true, LatencyMs: 300},
		{MonitorID: monitorID, AtMs: half + 2, OK: false, LatencyMs: 9000, Error: "timeout"},
		{MonitorID: monitorID, AtMs: half + 3, OK: true, LatencyMs: 100},
		{MonitorID: monitorID, AtMs: 2*half + 1, OK: true, LatencyMs: 200},
	}
	for _, c := range checks {
		c.Step = health.Step{Tracker: health.Tracker{Status: health.Up}}
		if err := s.RecordCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	latencies, err := s.LatenciesSince(ctx, monitorID, 0)
	if err != nil || !slices.Equal(latencies, []int64{100, 200, 300}) {
		t.Fatalf("latencies: got %v err=%v, want the successful ones sorted", latencies, err)
	}
	buckets, err := s.CheckBuckets(ctx, monitorID, 0, half)
	want := []CheckBucket{
		{StartMs: half, Checks: 3, Failures: 1, LatencyAvgMs: 200, LatencyMaxMs: 300},
		{StartMs: 2 * half, Checks: 1, LatencyAvgMs: 200, LatencyMaxMs: 200},
	}
	if err != nil || !slices.Equal(buckets, want) {
		t.Fatalf("buckets: got %+v err=%v, want %+v", buckets, err, want)
	}
	recent, err := s.RecentChecks(ctx, monitorID, 2)
	if err != nil || len(recent) != 2 || recent[0].AtMs != 2*half+1 || recent[1].LatencyMs != 100 {
		t.Fatalf("recent: got %+v err=%v, want the two newest, newest first", recent, err)
	}
}

func TestInstanceImageIsStoredReplacedAndRemoved(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if version, err := s.InstanceImageVersion(ctx, ImageLoginBackground); err != nil || version != 0 {
		t.Fatalf("version with no image: got %d err=%v, want 0", version, err)
	}
	if err := s.SetInstanceImage(ctx, ImageLoginBackground, "image/png", []byte("first"), 10); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstanceImage(ctx, ImageLoginBackground, "image/webp", []byte("second"), 20); err != nil {
		t.Fatal(err)
	}
	contentType, data, err := s.InstanceImage(ctx, ImageLoginBackground)
	if err != nil || contentType != "image/webp" || string(data) != "second" {
		t.Fatalf("after replacing: got %q %q err=%v, want the second upload", contentType, data, err)
	}
	if version, err := s.InstanceImageVersion(ctx, ImageLoginBackground); err != nil || version != 20 {
		t.Fatalf("version: got %d err=%v, want 20", version, err)
	}
	if err := s.DeleteInstanceImage(ctx, ImageLoginBackground); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InstanceImage(ctx, ImageLoginBackground); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after removal: got err=%v, want ErrNotFound", err)
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
