package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"uppies/internal/health"
	"uppies/internal/store"
)

func TestPercentileUsesTheNearestRank(t *testing.T) {
	hundred := make([]int64, 100)
	for i := range hundred {
		hundred[i] = int64(i + 1)
	}
	cases := []struct {
		sorted []int64
		p      int
		want   int64
	}{
		{nil, 99, 0},
		{[]int64{7}, 90, 7},
		{[]int64{10, 20, 30, 40}, 50, 20},
		{[]int64{10, 20, 30, 40}, 90, 40},
		{hundred, 90, 90},
		{hundred, 95, 95},
		{hundred, 99, 99},
	}
	for _, c := range cases {
		if got := percentile(c.sorted, c.p); got != c.want {
			t.Errorf("p%d of %d values: got %d, want %d", c.p, len(c.sorted), got, c.want)
		}
	}
}

func TestBadgeValueFitsFourCharacters(t *testing.T) {
	cases := []struct {
		ms          int64
		known       bool
		value, unit string
	}{
		{0, false, "--", "ms"},
		{42, true, "42", "ms"},
		{9999, true, "9999", "ms"},
		{12345, true, "12.3", "s"},
		{60000, true, "60.0", "s"},
	}
	for _, c := range cases {
		if value, unit := badgeValue(c.ms, c.known); value != c.value || unit != c.unit {
			t.Errorf("%d ms: got %q %q, want %q %q", c.ms, value, unit, c.value, c.unit)
		}
	}
}

func TestPercentileBadgeIsDeterministicAnd88By31(t *testing.T) {
	latencies := []int64{40, 50, 60, 70, 142}
	first := percentileBadge(99, latencies, stateOK)
	if second := percentileBadge(99, latencies, stateOK); first != second {
		t.Fatal("the same input gave two different images")
	}
	for _, want := range []string{`width="88" height="31"`, `aria-label="p99 response time over 24 hours: 142 ms"`, badgeColours[stateOK]} {
		if !strings.Contains(first, want) {
			t.Errorf("badge is missing %q", want)
		}
	}
	// Nothing in the image depends on a font being installed.
	for _, unwanted := range []string{"<text", "font"} {
		if strings.Contains(first, unwanted) {
			t.Errorf("badge contains %q", unwanted)
		}
	}
	if other := percentileBadge(99, []int64{40, 50, 60, 70, 143}, stateOK); other == first {
		t.Error("a different value gave the same image")
	}
	if empty := percentileBadge(90, nil, stateNone); !strings.Contains(empty, "p90 response time over 24 hours: no data") {
		t.Errorf("badge without data: %q", empty)
	}
}

func TestStateAndUptimeBadgesAndTheirOptions(t *testing.T) {
	up := drawBadge(statusSpec("Acme Payments API", stateOK, "up"))
	if !strings.Contains(up, `aria-label="Acme Payments API is up"`) || !strings.Contains(up, badgeColours[stateOK]) {
		t.Errorf("state badge: %q", up)
	}
	// A name is cut to what fits under the value, in the letters the font has.
	if got := badgeWord("Acme Payments API", 9); got != "acme paym" {
		t.Errorf("name on a badge: got %q, want %q", got, "acme paym")
	}
	if got := badgeWord("日本 <b>ok</b>", 4); got != "bok" {
		t.Errorf("a label keeps only what can be drawn: got %q, want %q", got, "bok")
	}
	cases := []struct{ uptime, value, title string }{
		{"99.94% uptime", "99.9", "uptime of api over 30 days: 99.94%"},
		{"100% uptime", "100", "uptime of api over 30 days: 100%"},
		{"no data yet", "--", "uptime of api over 30 days: no data"},
	}
	for _, c := range cases {
		spec := uptimeSpec("api", c.uptime, 30, stateOK)
		if spec.Value != c.value || spec.Title != c.title || spec.Sub != "30d" {
			t.Errorf("%q: got value %q sub %q title %q, want %q, 30d and %q", c.uptime, spec.Value, spec.Sub, spec.Title, c.value, c.title)
		}
	}
	// Options change the look and never the numbers. The same options give the same image.
	spec := uptimeSpec("api", "99.94% uptime", 30, stateOK)
	light := badgeOptions(spec, url.Values{"theme": {"light"}, "label": {"Live!!"}})
	if !light.Light || light.Label != "live" || light.Value != spec.Value {
		t.Errorf("options: got %+v", light)
	}
	if drawBadge(light) == drawBadge(spec) || drawBadge(light) != drawBadge(light) {
		t.Error("a light badge must differ from a dark one, and be the same each time")
	}
	for query, want := range map[string]int{"": 90, "days=7": 7, "days=0": 90, "days=9999": 90, "days=abc": 90} {
		values, _ := url.ParseQuery(query)
		if got := badgeDays(values, 90); got != want {
			t.Errorf("days from %q: got %d, want %d", query, got, want)
		}
	}
}

func TestBadgeTextDrawsOneRectanglePerRunOfPixels(t *testing.T) {
	// A dash is one row of five pixels: one rectangle, three rows down.
	if got, want := badgeText("-", 10, 20, 2), "M10 26h10v2h-10z"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := badgeTextWidth("p99", 1); got != 17 {
		t.Errorf("width of p99: got %d, want 17", got)
	}
}

func TestMonitorDetailPagesAndBadges(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor := store.Monitor{
		Name: "api", Kind: "http", Target: "https://api.acme.example/health",
		IntervalS: 60, TimeoutMs: 10_000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 1,
	}
	apiID, err := s.createShown(ctx, projectID, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor.Name = "internal queue"
	hiddenID, err := s.store.CreateMonitor(ctx, monitor, 1) // on no status page
	if err != nil {
		t.Fatal(err)
	}
	nowMs := time.Now().UnixMilli()
	tracker := health.Tracker{Status: health.Up}
	for i, latency := range []int64{80, 120, 100, 4000} {
		check := store.CheckResult{
			MonitorID: apiID, AtMs: nowMs - int64(i+1)*60_000, OK: true, LatencyMs: latency, CoveredMs: 60_000,
			Step: health.Step{Tracker: tracker},
		}
		if err := s.store.RecordCheck(ctx, check); err != nil {
			t.Fatal(err)
		}
	}
	failed := store.CheckResult{MonitorID: apiID, AtMs: nowMs - 10*60_000, Error: "dial tcp 10.0.0.7:443: refused", CoveredMs: 60_000, Step: health.Step{Tracker: tracker}}
	if err := s.store.RecordCheck(ctx, failed); err != nil {
		t.Fatal(err)
	}

	// The status page links each component to its own page.
	public := fmt.Sprintf("/s/acme/monitors/%d", apiID)
	if _, page := s.get(t, s.anonymous, "/s/acme"); !strings.Contains(page, `href="`+public+`"`) {
		t.Error("the status page does not link to the monitor's page")
	}
	status, page := s.get(t, s.anonymous, public)
	if status != http.StatusOK {
		t.Fatalf("public monitor page: got %d, want 200", status)
	}
	for _, want := range []string{"api", "p99, 24 h", "4000 ms", "p50, 24 h", "response time, last 24 hours", public + "/88x31.svg?show=p95", "<polyline"} {
		if !strings.Contains(page, want) {
			t.Errorf("public monitor page is missing %q", want)
		}
	}
	// What a check failed with can name private hosts, so the public page never shows it.
	for _, unwanted := range []string{"10.0.0.7", "api.acme.example"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("public monitor page shows %q", unwanted)
		}
	}
	for _, path := range []string{
		fmt.Sprintf("/s/acme/monitors/%d", hiddenID), fmt.Sprintf("/s/acme/monitors/%d/p99.svg", hiddenID),
		"/s/acme/monitors/999", public + "/p42.svg",
	} {
		if status, _ := s.get(t, s.anonymous, path); status != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, status)
		}
	}

	status, badge := s.get(t, s.anonymous, public+"/p99.svg")
	if status != http.StatusOK || !strings.Contains(badge, "p99 response time over 24 hours: 4000 ms") {
		t.Errorf("p99 badge: got %d %q", status, badge)
	}
	if _, again := s.get(t, s.anonymous, public+"/p99.svg"); again != badge {
		t.Error("the badge changed between two requests with the same data")
	}
	// One address serves every kind, for a component and for the whole page.
	for path, want := range map[string]string{
		public + "/88x31.svg":                       `aria-label="api is up"`,
		public + "/88x31.svg?show=p99":              "p99 response time over 24 hours: 4000 ms",
		public + "/88x31.svg?show=uptime&days=7":    "uptime of api over 7 days: 100%",
		"/s/acme/88x31.svg":                         `aria-label="Acme is up"`,
		"/s/acme/88x31.svg?show=uptime&theme=light": "uptime of Acme over 90 days: 100%",
	} {
		if status, image := s.get(t, s.anonymous, path); status != http.StatusOK || !strings.Contains(image, want) || !strings.Contains(image, `width="88" height="31"`) {
			t.Errorf("%s: got %d, want an 88 by 31 image with %q", path, status, want)
		}
	}
	for _, path := range []string{public + "/88x31.svg?show=nonsense", "/s/acme/88x31.svg?show=p99", "/s/nowhere/88x31.svg"} {
		if status, _ := s.get(t, s.anonymous, path); status != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, status)
		}
	}
	if _, p90 := s.get(t, s.anonymous, public+"/p90.svg"); !strings.Contains(p90, "p90 response time over 24 hours: 4000 ms") {
		t.Errorf("p90 of four values is the largest: %q", p90)
	}

	// The admin page of a monitor opens on an overview, with the errors the public page leaves out.
	_, admin := s.get(t, s.admin, fmt.Sprintf("/admin/monitors/%d", apiID))
	for _, want := range []string{`data-pane="overview"`, `data-pane="settings"`, "latest checks", "dial tcp 10.0.0.7:443: refused", "p95, 24 h", fmt.Sprintf(`shown on <a href="/s/acme/monitors/%d">Acme</a>`, apiID), `class="admonition admonition-success"`} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin monitor page is missing %q", want)
		}
	}
	if _, admin := s.get(t, s.admin, fmt.Sprintf("/admin/monitors/%d", hiddenID)); !strings.Contains(admin, "on no status page") {
		t.Error("the admin page of a monitor that no status page shows must say so")
	}
}

func TestResponseChartMarksFailuresAndGaps(t *testing.T) {
	// 1000 half hours after the epoch is 20:00 utc.
	first := int64(1000) * detailBucketMs
	buckets := []store.CheckBucket{
		{StartMs: first, Checks: 2, LatencyAvgMs: 50, LatencyMaxMs: 60},
		{StartMs: first + 2*detailBucketMs, Checks: 3, Failures: 3},
		{StartMs: first + 47*detailBucketMs, Checks: 4, Failures: 1, LatencyAvgMs: 100, LatencyMaxMs: 180},
	}
	line, slots, peak := responseChart(buckets, first, time.UTC)
	// The first point is half a slot in and halfway up, the last one touches the top margin.
	if line != "5.0,48.0 475.0,4.0" || peak != 100 || len(slots) != 48 {
		t.Fatalf("got line %q peak %d and %d slots", line, peak, len(slots))
	}
	want := map[int]chartSlot{
		0:  {Tip: "20:00: 50 ms on average, 60 ms at most"},
		1:  {Tip: "20:30: no checks"},
		2:  {Tip: "21:00: all 3 checks failed", Failed: true},
		47: {Tip: "19:30: 100 ms on average, 180 ms at most, 1 of 4 checks failed", Failed: true},
	}
	for i, w := range want {
		if slots[i] != w {
			t.Errorf("slot %d: got %+v, want %+v", i, slots[i], w)
		}
	}
}
