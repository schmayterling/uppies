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
	// layout reads the page back as "category/name:display:fill" per monitor, then its categories.
	layout := func() string {
		t.Helper()
		placed, err := s.store.MonitorsByProject(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range placed {
			got = append(got, m.Category+"/"+m.Name+":"+m.Display+":"+m.GraphFill)
		}
		categories, err := s.store.PageCategories(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, " ") + " | " + strings.Join(categories, ",")
	}
	want := func(step, expected string) {
		t.Helper()
		if got := layout(); got != expected {
			t.Fatalf("%s:\n got %s\nwant %s", step, got, expected)
		}
	}
	item := func(id string) string { return "m:" + id }
	api, website, database := ids[0], ids[1], ids[2]

	if resp := s.post(t, s.anonymous, save, url.Values{"item": {item(api)}}, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous save: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The editor is a page of its own. It lists the page's monitors and offers the one that is not on it.
	editor := fmt.Sprintf("/admin/projects/%d/editor", projectID)
	_, page := s.get(t, s.admin, editor)
	for _, wanted := range []string{`name="item" value="m:` + api + `"`, `<option value="` + spare + `">queue</option>`, `data-preview src="/s/acme"`, `name="new_category"`} {
		if !strings.Contains(page, wanted) {
			t.Errorf("editor page is missing %q", wanted)
		}
	}
	if _, admin := s.get(t, s.admin, fmt.Sprintf("/admin/projects/%d", projectID)); !strings.Contains(admin, editor) || !strings.Contains(admin, `data-pane="incidents"`) {
		t.Error("the status page's admin page must link to the editor and keep incidents in a tab")
	}

	// An arrangement with categories and a look per monitor. An unknown look falls back to the plain one.
	form := url.Values{
		"item":                {"c:", item(database), "c:web", item(api), item(website), "c:empty"},
		"display-" + database: {"latency"}, "fill-" + database: {"filled"},
		"display-" + api: {"sparkles"}, "fill-" + api: {"default"},
		"display-" + website: {"minimal"},
	}
	if resp := s.post(t, s.admin, save, form, nil); resp.Header.Get("Location") != editor {
		t.Fatalf("save: got %d to %q, want a redirect to the editor", resp.StatusCode, resp.Header.Get("Location"))
	}
	want("after the arrangement", "/database:latency:filled web/api:bars: web/website:minimal: | web,empty")

	// Without script each button sends one change with the arrangement.
	change := func(field, value string) {
		t.Helper()
		placed, err := s.store.MonitorsByProject(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		categories, err := s.store.PageCategories(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		// The form as the editor would send it: each category with its monitors, in order.
		sent := url.Values{"item": {"c:"}, field: {value}}
		for _, category := range append([]string{""}, categories...) {
			if category != "" {
				sent.Add("item", "c:"+category)
			}
			for _, m := range placed {
				if m.Category == category {
					sent.Add("item", fmt.Sprintf("m:%d", m.MonitorID))
					sent.Set(fmt.Sprintf("display-%d", m.MonitorID), m.Display)
					sent.Set(fmt.Sprintf("fill-%d", m.MonitorID), m.GraphFill)
				}
			}
		}
		if resp := s.post(t, s.admin, save, sent, nil); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s=%s: got %d, want 303", field, value, resp.StatusCode)
		}
	}
	change("up", website)
	want("the website moves up inside its category", "/database:latency:filled web/website:minimal: web/api:bars: | web,empty")
	change("up", website)
	want("at the top of its category it crosses into the one above", "/database:latency:filled /website:minimal: web/api:bars: | web,empty")
	change("down", api)
	want("at the bottom of its category it crosses into the one below", "/database:latency:filled /website:minimal: empty/api:bars: | web,empty")
	change("category_up", "empty")
	want("a category moves above another", "/database:latency:filled /website:minimal: empty/api:bars: | empty,web")
	change("category_up", "empty")
	want("the first category cannot move above the monitors without one", "/database:latency:filled /website:minimal: empty/api:bars: | empty,web")
	change("new_category", "late")
	want("a new category goes to the end", "/database:latency:filled /website:minimal: empty/api:bars: | empty,web,late")
	change("remove_category", "empty")
	want("removing a category keeps its monitors, without a category", "/database:latency:filled /website:minimal: /api:bars: | web,late")
	change("remove", database)
	change("add", spare)
	want("a monitor is taken off and another one added", "/website:minimal: /api:bars: /queue:bars: | web,late")
	// Taking a monitor off a page does not delete it. One that does not exist, or is named twice, is left out.
	if all, err := s.store.Monitors(ctx); err != nil || len(all) != 4 {
		t.Fatalf("monitors: got %d err=%v, want all 4 still there", len(all), err)
	}
	s.post(t, s.admin, save, url.Values{"item": {item(api), "m:999", item(api)}}, nil)
	want("a list with a missing and a repeated monitor", "/api:bars: | ")
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
