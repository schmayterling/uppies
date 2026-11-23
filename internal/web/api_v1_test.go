package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (s *site) api(t *testing.T, method, path, token, body string, want int) (http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.server.URL+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.anonymous.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if res.StatusCode != want {
		t.Fatalf("%s %s: got %d, want %d", method, req.URL.Path, res.StatusCode, want)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want != http.StatusNoContent && method != http.MethodHead {
		if !json.Valid(data) || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("%s %s: expected a JSON response", method, req.URL.Path)
		}
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%s %s: expected Cache-Control: no-store", method, req.URL.Path)
	}
	return res.Header, string(data)
}

func TestAPIRequiresBearerAndRechecksGrantsAndRevocation(t *testing.T) {
	s := newSite(t)
	const collection = "/status-pages"
	header, _ := s.api(t, http.MethodGet, collection, "", "", http.StatusUnauthorized)
	if !strings.HasPrefix(header.Get("WWW-Authenticate"), "Bearer ") {
		t.Fatal("missing bearer challenge")
	}
	if status, _ := s.get(t, s.admin, "/api/v1"+collection); status != http.StatusUnauthorized {
		t.Fatalf("an admin cookie was accepted with status %d", status)
	}
	token := createAPIToken(t, s)
	s.api(t, http.MethodGet, collection, token, "", http.StatusForbidden)

	edit := url.Values{"permission": {"status_pages:read"}}
	if res := s.post(t, s.admin, "/admin/api-tokens/1/permissions", edit, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save permissions: got %d, want 303", res.StatusCode)
	}
	s.api(t, http.MethodGet, collection, token, "", http.StatusOK)
	s.api(t, http.MethodPost, collection, token, `{"name":"Private","slug":"private"}`, http.StatusForbidden)
	s.api(t, http.MethodGet, "/no-route", token, "", http.StatusNotFound)
	method, _ := s.api(t, http.MethodPut, collection, token, "", http.StatusMethodNotAllowed)
	if !strings.Contains(method.Get("Allow"), "GET") {
		t.Fatal("method-not-allowed response lacks the Allow header")
	}

	if res := s.post(t, s.admin, "/admin/api-tokens/1/permissions", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("clear permissions: got %d, want 303", res.StatusCode)
	}
	s.api(t, http.MethodGet, collection, token, "", http.StatusForbidden)
	if res := s.post(t, s.admin, "/admin/api-tokens/1/permissions", edit, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("restore permissions: got %d, want 303", res.StatusCode)
	}
	if res := s.post(t, s.admin, "/admin/api-tokens/1/revoke", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke token: got %d, want 303", res.StatusCode)
	}
	s.api(t, http.MethodGet, collection, token, "", http.StatusUnauthorized)
}

func TestEveryAPIRouteRequiresItsOwnGrant(t *testing.T) {
	s := newSite(t)
	ungranted := createAPIToken(t, s)
	routes := []struct{ method, path string }{
		{"GET", "/status-pages"},
		{"POST", "/status-pages"},
		{"GET", "/status-pages/1"},
		{"PATCH", "/status-pages/1"},
		{"DELETE", "/status-pages/1"},
		{"GET", "/status-pages/1/layout"},
		{"PUT", "/status-pages/1/layout"},
		{"GET", "/monitors"},
		{"POST", "/monitors"},
		{"GET", "/monitors/1"},
		{"PATCH", "/monitors/1"},
		{"DELETE", "/monitors/1"},
		{"GET", "/monitors/1/alert-channels"},
		{"PUT", "/monitors/1/alert-channels"},
		{"GET", "/alert-channels"},
		{"POST", "/alert-channels"},
		{"GET", "/alert-channels/1"},
		{"PATCH", "/alert-channels/1"},
		{"DELETE", "/alert-channels/1"},
		{"GET", "/status-pages/1/maintenance"},
		{"POST", "/status-pages/1/maintenance"},
		{"GET", "/status-pages/1/maintenance/1"},
		{"PATCH", "/status-pages/1/maintenance/1"},
		{"DELETE", "/status-pages/1/maintenance/1"},
		{"POST", "/status-pages/1/maintenance/1/end"},
		{"GET", "/status-pages/1/maintenance/1/updates"},
		{"POST", "/status-pages/1/maintenance/1/updates"},
		{"GET", "/status-pages/1/incidents"},
		{"POST", "/status-pages/1/incidents"},
		{"GET", "/status-pages/1/incidents/1"},
		{"PATCH", "/status-pages/1/incidents/1"},
		{"DELETE", "/status-pages/1/incidents/1"},
		{"GET", "/status-pages/1/incidents/1/updates"},
		{"POST", "/status-pages/1/incidents/1/updates"},
		{"POST", "/status-pages/1/incidents/1/publish"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			s.api(t, route.method, route.path, "", "", http.StatusUnauthorized)
			_, body := s.api(t, route.method, route.path, ungranted, "", http.StatusForbidden)
			if !strings.Contains(body, `"code":"insufficient_permission"`) {
				t.Fatal("route did not return the permission error")
			}
		})
	}
}

func TestAPIRejectsCrossOriginWritesAndOtherMediaTypes(t *testing.T) {
	s := newSite(t)
	token := createAPIToken(t, s, "status_pages:create")
	const input = `{"name":"Private","slug":"private"}`
	tests := []struct {
		name, origin, mediaType, code string
		status                        int
	}{
		{"cross-origin", "https://example.test", "application/json", "cross_origin", http.StatusForbidden},
		{"plain text", "", "text/plain", "unsupported_media_type", http.StatusUnsupportedMediaType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/status-pages", strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", tc.mediaType)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := s.anonymous.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var problem struct {
				Error struct{ Code string }
			}
			if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
				t.Fatal("expected a JSON error")
			}
			if resp.StatusCode != tc.status || problem.Error.Code != tc.code || resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("got HTTP %d and code %q, want %d and %q without CORS headers", resp.StatusCode, problem.Error.Code, tc.status, tc.code)
			}
		})
	}
	if got := s.projectCount(t); got != 0 {
		t.Fatalf("rejected requests created %d projects, want 0", got)
	}
}

func TestAPIRejectsAnExpiredBearer(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	userID, _, err := s.store.UserPasswordHash(ctx, "may")
	if err != nil {
		t.Fatal(err)
	}
	token := "uppies_" + strings.Repeat("1", 64)
	nowMs := time.Now().UnixMilli()
	if err := s.store.CreateAPIToken(ctx, userID, "expired", hashToken(token), nowMs-1000, nowMs-1, []string{"status_pages:read"}); err != nil {
		t.Fatal(err)
	}
	s.api(t, http.MethodGet, "/status-pages", token, "", http.StatusUnauthorized)
}

func TestAPIStatusPageCRUDAndPagination(t *testing.T) {
	s := newSite(t)
	token := createAPIToken(t, s, "status_pages:read", "status_pages:create", "status_pages:update", "status_pages:delete")
	const pages = "/status-pages"

	header, _ := s.api(t, http.MethodPost, pages, token, `{"name":"Site","slug":"site"}`, http.StatusCreated)
	if header.Get("Location") != "/api/v1/status-pages/1" {
		t.Fatalf("create status page location = %q", header.Get("Location"))
	}
	s.api(t, http.MethodPost, pages, token, `{"name":"Duplicate","slug":"site"}`, http.StatusConflict)
	s.api(t, http.MethodPost, pages, token, `{"name":"Second","slug":"second"}`, http.StatusCreated)

	_, first := s.api(t, http.MethodGet, pages+"?limit=1", token, "", http.StatusOK)
	_, second := s.api(t, http.MethodGet, pages+"?limit=1&after=1", token, "", http.StatusOK)
	if !strings.Contains(first, `"next_after":"1"`) || !strings.Contains(second, `"next_after":null`) || !strings.Contains(second, `"slug":"second"`) {
		t.Fatal("pagination did not advance from the first page to the last")
	}

	path := pages + "/1"
	s.api(t, http.MethodPatch, path, token, `{"navbar_link":"homepage"}`, http.StatusUnprocessableEntity)
	s.api(t, http.MethodPatch, path, token, `{"navbar_link":"invalid"}`, http.StatusUnprocessableEntity)
	s.api(t, http.MethodPatch, path, token, `{"theme":"dark","timezone":"Asia/Manila","show_face":false,"navbar_link":"homepage","homepage_url":"https://example.test/docs"}`, http.StatusNoContent)
	s.api(t, http.MethodPatch, path, token, `{"announcement":"Maintenance tonight","announcement_color":"green"}`, http.StatusNoContent)
	s.api(t, http.MethodPatch, path, token, `{"name":"Renamed"}`, http.StatusNoContent)
	_, result := s.api(t, http.MethodGet, path, token, "", http.StatusOK)
	for _, want := range []string{`"name":"Renamed"`, `"theme":"dark"`, `"timezone":"Asia/Manila"`, `"show_face":false`, `"navbar_link":"homepage"`, `"homepage_url":"https://example.test/docs"`, `"announcement":"Maintenance tonight"`, `"announcement_color":"green"`} {
		if !strings.Contains(result, want) {
			t.Errorf("status page after a partial update is missing %s", want)
		}
	}
	s.api(t, http.MethodDelete, path, token, "", http.StatusNoContent)
	s.api(t, http.MethodGet, path, token, "", http.StatusNotFound)
}

func TestAPIReplacesPageLayoutWithoutChangingOtherPages(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	if _, err := s.store.CreateProject(ctx, "one", "One", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateProject(ctx, "two", "Two", 1); err != nil {
		t.Fatal(err)
	}
	monitor := defaultMonitor()
	monitor.Name, monitor.Target = "Web", "https://example.test"
	monitorID, err := s.store.CreateMonitor(ctx, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.PlaceMonitor(ctx, 2, monitorID); err != nil {
		t.Fatal(err)
	}

	token := createAPIToken(t, s, "status_pages:read", "status_pages:update")
	const path = "/status-pages/1/layout"
	layout := `{"categories":["Backend"],"monitors":[{"monitor_id":"1","category":"Backend","display":"latency"}]}`
	s.api(t, http.MethodPut, path, token, layout, http.StatusNoContent)
	_, first := s.api(t, http.MethodGet, path, token, "", http.StatusOK)
	if !strings.Contains(first, `"categories":["Backend"]`) || !strings.Contains(first, `"display":"latency"`) {
		t.Fatal("expected the requested category and monitor display")
	}
	invalid := `{"categories":[],"monitors":[{"monitor_id":"999"}]}`
	s.api(t, http.MethodPut, path, token, invalid, http.StatusUnprocessableEntity)
	_, after := s.api(t, http.MethodGet, path, token, "", http.StatusOK)
	if after != first {
		t.Fatal("invalid monitor reference changed the existing layout")
	}
	_, other := s.api(t, http.MethodGet, "/status-pages/2/layout", token, "", http.StatusOK)
	if !strings.Contains(other, `"display":"bars"`) || strings.Contains(other, "Backend") {
		t.Fatal("replacing one page's layout changed a different page")
	}
}

func TestAPIRejectsInvalidStatusPagePayloadsBeforeWriting(t *testing.T) {
	s := newSite(t)
	token := createAPIToken(t, s, "status_pages:create")
	cases := []struct {
		name, body string
		status     int
	}{
		{"unknown field", `{"name":"Site","slug":"site","id":"1"}`, http.StatusBadRequest},
		{"duplicate field", `{"name":"Site","slug":"site","slug":"other"}`, http.StatusBadRequest},
		{"null value", `{"name":"Site","slug":null}`, http.StatusBadRequest},
		{"trailing object", `{"name":"Site","slug":"site"} {}`, http.StatusBadRequest},
		{"invalid slug", `{"name":"Site","slug":"Not a slug"}`, http.StatusUnprocessableEntity},
		{"oversized", `{"name":"` + strings.Repeat("a", 64<<10) + `","slug":"site"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s.api(t, http.MethodPost, "/status-pages", token, tc.body, tc.status)
		})
	}
	if got := s.projectCount(t); got != 0 {
		t.Fatalf("invalid payloads created %d projects, want 0", got)
	}
}
