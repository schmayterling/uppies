package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"uppies/internal/store"
)

func TestGraphFillTakesTheNarrowestChoice(t *testing.T) {
	instance := store.InstanceSettings{GraphFill: "gradient"}
	cases := []struct{ project, monitor, want string }{
		{"", "", "gradient"},
		{"filled", "", "filled"},
		{"filled", "none", "none"},
		{"", "none", "none"},
	}
	for _, c := range cases {
		got := graphFill(instance, store.Project{GraphFill: c.project}, store.Monitor{GraphFill: c.monitor})
		if got != c.want {
			t.Errorf("page %q monitor %q: got %q, want %q", c.project, c.monitor, got, c.want)
		}
	}
}

func TestGraphAreaClosesTheLineAlongTheBottom(t *testing.T) {
	area := newGraphArea("filled", "0.0,2.0 150.0,16.0 300.0,9.0", 32, "area-1")
	if area.Points != "0.0,32 0.0,2.0 150.0,16.0 300.0,9.0 300.0,32" || area.Fill != "filled" || area.ID != "area-1" {
		t.Errorf("got %+v", area)
	}
	// One point is no line, so there is nothing to fill under it.
	for _, c := range []struct{ fill, line string }{{"none", "0.0,2.0 300.0,9.0"}, {"gradient", "0.0,2.0"}, {"gradient", ""}} {
		if area := newGraphArea(c.fill, c.line, 32, "x"); area.Fill != "none" || area.Points != "" {
			t.Errorf("fill %q line %q: got %+v, want no area", c.fill, c.line, area)
		}
	}
}

func TestGraphFillIsSetForTheInstanceAStatusPageAndAMonitor(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor := store.Monitor{
		ProjectID: projectID, Name: "api", Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2, Public: true, Display: "latency",
	}
	monitorID, err := s.store.CreateMonitor(ctx, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Two days of response times, so that there is a line to fill under.
	today := store.DayStartMs(time.Now().UnixMilli())
	for daysAgo := int64(1); daysAgo <= 2; daysAgo++ {
		day := store.DayRollup{DayStartMs: today - daysAgo*dayMs, Checks: 10, CoveredMs: dayMs}
		if err := s.store.ImportDailyRollup(ctx, monitorID, day, 500, 80); err != nil {
			t.Fatal(err)
		}
	}
	const gradient, flat = `<linearGradient id="area-`, `class="area area-flat"`
	statusHas := func(marker string) bool {
		_, page := s.get(t, s.anonymous, "/s/acme")
		return strings.Contains(page, marker)
	}
	if !statusHas(gradient) {
		t.Fatal("a fresh install fills its graphs with a gradient")
	}

	// The instance setting applies where nothing narrower is set.
	s.post(t, s.admin, "/admin/appearance", url.Values{"theme": {"auto"}, "accent_hue": {"200"}, "graph_fill": {"filled"}}, nil)
	if !statusHas(flat) || statusHas(gradient) {
		t.Error("the instance setting did not reach the status page")
	}
	// The status page's own setting wins over the instance.
	project, err := s.store.ProjectBySlug(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	project.GraphFill = "none"
	if err := s.store.UpdateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if statusHas(flat) || statusHas(gradient) {
		t.Error("the status page chose no fill and a graph is still filled")
	}
	// The monitor's own setting wins over its status page, through the real form.
	form := url.Values{
		"name": {"api"}, "kind": {"http"}, "target": {"https://example.com"}, "interval_s": {"60"}, "timeout_s": {"5"},
		"retries": {"2"}, "retry_interval_s": {"20"}, "recovery_checks": {"2"}, "public": {"on"}, "display": {"latency"},
		"graph_fill": {"gradient"},
	}
	if resp := s.post(t, s.admin, fmt.Sprintf("/admin/monitors/%d", monitorID), form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save monitor: got %d, want 303", resp.StatusCode)
	}
	if !statusHas(gradient) {
		t.Error("the monitor chose a gradient and its graph has none")
	}
	// "default" hands the choice back to the status page.
	form.Set("graph_fill", "default")
	s.post(t, s.admin, fmt.Sprintf("/admin/monitors/%d", monitorID), form, nil)
	if statusHas(gradient) {
		t.Error("the monitor went back to the default and its graph is still filled")
	}
	form.Set("graph_fill", "sparkles")
	if resp := s.post(t, s.admin, fmt.Sprintf("/admin/monitors/%d", monitorID), form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown fill: got %d, want 422", resp.StatusCode)
	}
}
