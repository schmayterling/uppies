package web

import (
	"context"
	"fmt"
	"net/http"
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
