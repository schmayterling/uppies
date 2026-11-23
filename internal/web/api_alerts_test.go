package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"uppies/internal/store"
)

func TestAPIAlertChannelCRUDRedactsDestinationsAndPreservesAssignments(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	m := defaultMonitor()
	m.Name, m.Target = "Existing", "https://example.test"
	if _, err := s.store.CreateMonitor(ctx, m, 1); err != nil {
		t.Fatal(err)
	}
	token := createAPIToken(t, s, "alert_channels:read", "alert_channels:create", "alert_channels:update", "alert_channels:delete")
	const paths = "/alert-channels"
	const destination = "https://example.test/private-webhook"
	s.api(t, http.MethodPost, paths, token, `{"kind":"webhook","destination":"`+destination+`"}`, http.StatusCreated)
	if assigned, err := s.store.MonitorChannelIDs(ctx, 1); err != nil || len(assigned) != 0 {
		t.Fatalf("API channel creation assigned it to an existing monitor: count=%d err=%v", len(assigned), err)
	}
	_, listed := s.api(t, http.MethodGet, paths, token, "", http.StatusOK)
	_, read := s.api(t, http.MethodGet, paths+"/1", token, "", http.StatusOK)
	if !strings.Contains(read, `"kind":"webhook"`) || strings.Contains(listed, destination) || strings.Contains(read, destination) {
		t.Fatal("expected redacted channel results")
	}

	if err := s.store.SetMonitorChannels(ctx, 1, []int64{1}); err != nil {
		t.Fatal(err)
	}
	s.api(t, http.MethodPatch, paths+"/1", token, `{"kind":"email"}`, http.StatusUnprocessableEntity)
	s.api(t, http.MethodPatch, paths+"/1", token, `{"kind":"email","destination":"fixture@example.test"}`, http.StatusNoContent)
	channels, err := s.store.MonitorChannels(ctx, 1)
	if err != nil || len(channels) != 1 || channels[0] != (store.AlertChannel{ID: 1, Kind: "email", URL: "fixture@example.test"}) {
		t.Fatalf("channel update did not preserve the assignment or configuration: count=%d err=%v", len(channels), err)
	}
	s.api(t, http.MethodDelete, paths+"/1", token, "", http.StatusNoContent)
	s.api(t, http.MethodGet, paths+"/1", token, "", http.StatusNotFound)
}
