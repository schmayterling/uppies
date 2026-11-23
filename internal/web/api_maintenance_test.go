package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAPIMaintenanceCRUDChecksPageOwnershipAndDates(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	if _, err := s.store.CreateProject(ctx, "one", "One", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateProject(ctx, "two", "Two", 1); err != nil {
		t.Fatal(err)
	}
	token := createAPIToken(t, s, "maintenance:read", "maintenance:create", "maintenance:update", "maintenance:delete")
	const collection = "/status-pages/1/maintenance"
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(time.Hour).Format(time.RFC3339)
	end := now.Add(2 * time.Hour).Format(time.RFC3339)
	reversed := fmt.Sprintf(`{"title":"Database upgrade","starts_at":%q,"ends_at":%q}`, end, start)
	s.api(t, http.MethodPost, collection, token, reversed, http.StatusUnprocessableEntity)
	payload := fmt.Sprintf(`{"title":"Database upgrade","starts_at":%q,"ends_at":%q}`, start, end)
	s.api(t, http.MethodPost, collection, token, payload, http.StatusCreated)

	const other = "/status-pages/2/maintenance/1"
	s.api(t, http.MethodGet, other, token, "", http.StatusNotFound)
	s.api(t, http.MethodPatch, other, token, `{"title":"Wrong page"}`, http.StatusNotFound)
	s.api(t, http.MethodDelete, other, token, "", http.StatusNotFound)

	const path = collection + "/1"
	s.api(t, http.MethodPatch, path, token, `{"title":"Rescheduled upgrade"}`, http.StatusNoContent)
	_, result := s.api(t, http.MethodGet, path, token, "", http.StatusOK)
	if !strings.Contains(result, `"title":"Rescheduled upgrade"`) || !strings.Contains(result, end) || !strings.Contains(result, start) {
		t.Fatal("maintenance title update failed or changed the omitted times")
	}
	_, result = s.api(t, http.MethodGet, collection, token, "", http.StatusOK)
	if !strings.Contains(result, `"title":"Rescheduled upgrade"`) {
		t.Fatal("maintenance list omitted the updated window")
	}
	s.api(t, http.MethodDelete, path, token, "", http.StatusNoContent)
	s.api(t, http.MethodGet, path, token, "", http.StatusNotFound)
}

func TestAPIMaintenanceScopeEndAndUpdates(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	page, err := s.store.CreateProject(ctx, "one", "One", 1)
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := s.store.CreateProject(ctx, "two", "Two", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor := defaultMonitor()
	monitor.Name, monitor.Target = "API", "https://example.test"
	selected, err := s.createShown(ctx, page, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.createShown(ctx, secondPage, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}

	token := createAPIToken(t, s, maintenanceRead, maintenanceCreate, maintenanceUpdate)
	collection := fmt.Sprintf("/status-pages/%d/maintenance", page)
	start := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	input := fmt.Sprintf(`{"title":"Open upgrade","starts_at":%q,"until_further_notice":true,"monitor_ids":[%q]}`, start, strconv.FormatInt(selected, 10))
	header, _ := s.api(t, http.MethodPost, collection, token, input, http.StatusCreated)
	window := strings.TrimPrefix(header.Get("Location"), apiPrefix)
	patch := fmt.Sprintf(`{"title":"Wrong scope","monitor_ids":[%q]}`, strconv.FormatInt(foreign, 10))
	s.api(t, http.MethodPatch, window, token, patch, http.StatusUnprocessableEntity)
	_, body := s.api(t, http.MethodGet, window, token, "", http.StatusOK)
	if !strings.Contains(body, `"ends_at":null`) || !strings.Contains(body, `"all_monitors":false`) ||
		!strings.Contains(body, `"title":"Open upgrade"`) || !strings.Contains(body, fmt.Sprintf(`"monitor_ids":["%d"]`, selected)) {
		t.Fatalf("window response after rejected PATCH: %s", body)
	}

	wrongPage := fmt.Sprintf("/status-pages/%d/maintenance/1", secondPage)
	s.api(t, http.MethodPost, wrongPage+"/end", token, "", http.StatusNotFound)
	s.api(t, http.MethodGet, wrongPage+"/updates", token, "", http.StatusNotFound)
	s.api(t, http.MethodPost, wrongPage+"/updates", token, `{"body":"Not on this page"}`, http.StatusNotFound)
	s.api(t, http.MethodPost, window+"/updates", token, `{"body":"Work has started"}`, http.StatusNoContent)
	s.api(t, http.MethodPost, window+"/end", token, "", http.StatusNoContent)
	_, ended := s.api(t, http.MethodGet, window, token, "", http.StatusOK)
	if !strings.Contains(ended, `"until_further_notice":false`) || strings.Contains(ended, `"ends_at":null`) {
		t.Fatalf("end did not record the window's end: %s", ended)
	}
	s.api(t, http.MethodPost, window+"/end", token, "", http.StatusNoContent)
	_, repeated := s.api(t, http.MethodGet, window, token, "", http.StatusOK)
	if repeated != ended {
		t.Fatalf("repeated end changed the window: %s to %s", ended, repeated)
	}
	s.api(t, http.MethodPost, window+"/updates", token, `{"body":"Work is complete"}`, http.StatusNoContent)
	_, notes := s.api(t, http.MethodGet, window+"/updates", token, "", http.StatusOK)
	if !strings.Contains(notes, `"body":"Work has started"`) || !strings.Contains(notes, `"body":"Work is complete"`) || !strings.Contains(notes, `"at":`) {
		t.Fatalf("update history after end: %s", notes)
	}
}
