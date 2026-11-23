package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"uppies/internal/store"
)

func TestTheEditorOrdersAddsRemovesAndStylesMonitors(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor := store.Monitor{
		Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
	var ids []string
	for _, name := range []string{"api", "website", "database"} {
		monitor.Name = name
		id, err := s.createShown(ctx, projectID, monitor, 1)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	monitor.Name = "queue"
	spareID, err := s.store.CreateMonitor(ctx, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	spare := strconv.FormatInt(spareID, 10)
	save := fmt.Sprintf("/admin/projects/%d/placements", projectID)
	names := func() []string {
		t.Helper()
		placed, err := s.store.MonitorsByProject(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range placed {
			got = append(got, m.Name+":"+m.Display+":"+m.GraphFill)
		}
		return got
	}

	if resp := s.post(t, s.anonymous, save, url.Values{"monitor": ids}, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous save: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The editor page lists the page's monitors and offers the one that is not on it.
	_, page := s.get(t, s.admin, fmt.Sprintf("/admin/projects/%d", projectID))
	for _, want := range []string{`name="monitor" value="` + ids[0] + `"`, `<option value="` + spare + `">queue</option>`, `data-preview src="/s/acme"`, `data-pane="incidents"`} {
		if !strings.Contains(page, want) {
			t.Errorf("editor page is missing %q", want)
		}
	}

	// A new order with a look per monitor. An unknown look falls back to the plain one.
	form := url.Values{
		"monitor":           {ids[2], ids[0], ids[1]},
		"display-" + ids[2]: {"latency"}, "fill-" + ids[2]: {"filled"},
		"display-" + ids[0]: {"sparkles"}, "fill-" + ids[0]: {"default"},
		"display-" + ids[1]: {"minimal"},
	}
	if resp := s.post(t, s.admin, save, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: got %d, want 303", resp.StatusCode)
	}
	if got, want := names(), []string{"database:latency:filled", "api:bars:", "website:minimal:"}; !slices.Equal(got, want) {
		t.Fatalf("after a new order: got %v, want %v", got, want)
	}
	// Without script the arrows, the cross and the add menu each send one change with the list.
	form.Set("move", ids[1]+"-up")
	s.post(t, s.admin, save, form, nil)
	if got, want := names(), []string{"database:latency:filled", "website:minimal:", "api:bars:"}; !slices.Equal(got, want) {
		t.Fatalf("after moving the website up: got %v, want %v", got, want)
	}
	form = url.Values{"monitor": {ids[2], ids[1], ids[0]}, "remove": {ids[2]}, "add": {spare}}
	s.post(t, s.admin, save, form, nil)
	if got, want := names(), []string{"website:bars:", "api:bars:", "queue:bars:"}; !slices.Equal(got, want) {
		t.Fatalf("after removing the database and adding the queue: got %v, want %v", got, want)
	}
	// Taking a monitor off a page does not delete it. One that does not exist, or is named twice, is left out.
	if all, err := s.store.Monitors(ctx); err != nil || len(all) != 4 {
		t.Fatalf("monitors: got %d err=%v, want all 4 still there", len(all), err)
	}
	s.post(t, s.admin, save, url.Values{"monitor": {ids[0], "999", ids[0]}}, nil)
	if got, want := names(), []string{"api:bars:"}; !slices.Equal(got, want) {
		t.Fatalf("after a list with a missing and a repeated monitor: got %v, want %v", got, want)
	}
	if _, public := s.get(t, s.anonymous, "/s/acme"); !strings.Contains(public, "api") || strings.Contains(public, "website") {
		t.Error("the status page must show exactly the monitors the editor left on it")
	}
}

func TestTheEditorSavesTheLookAndTheOrderOfStatusPages(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	firstID, err := s.store.CreateProject(ctx, "first", "First", 1)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := s.store.CreateProject(ctx, "second", "Second", 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.store.ProjectByID(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	look := fmt.Sprintf("/admin/projects/%d/look", firstID)
	form := url.Values{
		"theme": {"dark"}, "accent_hue": {"25"}, "surface": {"paper"}, "corners": {"round"}, "font": {"mono"},
		"graph_fill": {"none"}, "hide_quiet_days": {"on"}, "custom_css": {".banner { border-width: 3px; }"},
	}
	if resp := s.post(t, s.admin, look, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save the look: got %d, want 303", resp.StatusCode)
	}
	got, err := s.store.ProjectByID(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	// The look changes. What the form does not hold, such as the name and the timezone, stays.
	want := before
	want.Theme, want.AccentHue, want.Surface, want.Corners, want.Font = "dark", 25, "paper", "round", "mono"
	want.GraphFill, want.HideQuietDays, want.ShowFace, want.CustomCSS = "none", true, false, ".banner { border-width: 3px; }"
	if got != want {
		t.Fatalf("project after saving the look:\n got %+v\nwant %+v", got, want)
	}
	form.Set("corners", "pointy")
	if resp := s.post(t, s.admin, look, form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown corner style: got %d, want 422", resp.StatusCode)
	}

	// Status pages move one place at a time. Moving the first one up changes nothing.
	order := func() []int64 {
		t.Helper()
		projects, err := s.store.Projects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, p := range projects {
			ids = append(ids, p.ID)
		}
		return ids
	}
	move := func(id int64, direction string) {
		t.Helper()
		resp := s.post(t, s.admin, fmt.Sprintf("/admin/projects/%d/move", id), url.Values{"direction": {direction}}, nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("move %d %s: got %d, want 303", id, direction, resp.StatusCode)
		}
	}
	move(firstID, "up")
	if got := order(); !slices.Equal(got, []int64{firstID, secondID}) {
		t.Fatalf("after moving the first page up: got %v, want no change", got)
	}
	move(firstID, "down")
	if got := order(); !slices.Equal(got, []int64{secondID, firstID}) {
		t.Fatalf("after moving the first page down: got %v, want it second", got)
	}
	// The public list follows the same order.
	_, list := s.get(t, s.anonymous, "/")
	if strings.Index(list, "Second") > strings.Index(list, "First") {
		t.Error("the public list does not follow the new order")
	}
}
