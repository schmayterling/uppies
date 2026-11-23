package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"uppies/internal/store"
)

func TestAPIMonitorCRUDKeepsCredentialBearingSettingsPrivate(t *testing.T) {
	s := newSite(t)
	token := createAPIToken(t, s, "monitors:read", "monitors:create", "monitors:update", "monitors:delete")
	const target = "https://example.test/fixture-target?key=fixture"
	const keyword = "fixture-keyword"
	input := `{"name":"API","target":"` + target + `","keyword":"` + keyword + `","timeout_ms":3000,"tags":["Production"," DB  team ","production"]}`
	s.api(t, http.MethodPost, "/monitors", token, input, http.StatusCreated)
	s.api(t, http.MethodPost, "/monitors", token, `{"name":"Worker","kind":"push"}`, http.StatusCreated)

	s.api(t, http.MethodPatch, "/monitors/1", token, `{"paused":true}`, http.StatusNoContent)
	m, err := s.store.Monitor(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Target != target || m.Keyword != keyword || m.TimeoutMs != 3000 || !m.Paused || m.Tags != "Production, DB team" {
		t.Fatal("a partial monitor update changed the omitted settings")
	}
	push, err := s.store.Monitor(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if push.PushToken == "" {
		t.Fatal("push monitor has no generated credential")
	}
	paths := []string{"/monitors", "/monitors/1", "/monitors/2"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			_, body := s.api(t, http.MethodGet, path, token, "", http.StatusOK)
			for _, forbidden := range []string{target, keyword, push.PushToken, `"target"`, `"push_token"`, `"last_error"`} {
				if strings.Contains(body, forbidden) {
					t.Fatal("read response exposes a write-only or private field")
				}
			}
		})
	}

	_, body := s.api(t, http.MethodGet, "/monitors/1", token, "", http.StatusOK)
	if !strings.Contains(body, `"tags":["Production","DB team"]`) {
		t.Fatal("read response is missing the normalized tags")
	}
	s.api(t, http.MethodPatch, "/monitors/1", token, `{"tags":[]}`, http.StatusNoContent)
	_, body = s.api(t, http.MethodGet, "/monitors/1", token, "", http.StatusOK)
	if !strings.Contains(body, `"tags":[]`) {
		t.Fatal("an empty tag list did not clear the tags")
	}

	s.api(t, http.MethodPatch, "/monitors/1", token, `{"kind":"tcp"}`, http.StatusUnprocessableEntity)
	s.api(t, http.MethodPatch, "/monitors/1", token, `{"kind":"tcp","target":"https://example.test"}`, http.StatusUnprocessableEntity)
	s.api(t, http.MethodPatch, "/monitors/1", token, `{"kind":"tcp","target":"db.example.test:5432","keyword":""}`, http.StatusNoContent)
	s.api(t, http.MethodDelete, "/monitors/1", token, "", http.StatusNoContent)
	s.api(t, http.MethodGet, "/monitors/1", token, "", http.StatusNotFound)
}

func TestAPIMonitorIncidentPolicies(t *testing.T) {
	s := newSite(t)
	restricted := createAPIToken(t, s, monitorsRead, monitorsCreate, monitorsUpdate)
	allowed := createAPIToken(t, s, monitorsRead, monitorsCreate, monitorsUpdate, incidentsCreate, incidentsPublish, incidentsUpdate)
	s.api(t, http.MethodPost, "/monitors", restricted, `{"name":"Probe","kind":"push","auto_create_incident":"publish"}`, http.StatusForbidden)
	s.api(t, http.MethodPost, "/monitors", allowed, `{"name":"Probe","kind":"push","auto_create_incident":"publish"}`, http.StatusCreated)

	_, body := s.api(t, http.MethodGet, "/monitors/1", restricted, "", http.StatusOK)
	if !strings.Contains(body, `"auto_create_incident":"publish"`) || !strings.Contains(body, `"auto_resolve_incident":"drafts"`) {
		t.Fatalf("read response is missing the published policy and default resolution: %s", body)
	}
	s.api(t, http.MethodPatch, "/monitors/1", restricted, `{"auto_resolve_incident":"all"}`, http.StatusForbidden)
	s.api(t, http.MethodPatch, "/monitors/1", allowed, `{"auto_resolve_incident":"all"}`, http.StatusNoContent)
	s.api(t, http.MethodPatch, "/monitors/1", restricted, `{"name":"Probe renamed"}`, http.StatusNoContent)
	_, body = s.api(t, http.MethodPatch, "/monitors/1", restricted, `{"auto_create_incident":""}`, http.StatusUnprocessableEntity)
	if !strings.Contains(body, `"field":"auto_create_incident"`) {
		t.Fatalf("invalid policy response is missing the field: %s", body)
	}
	_, body = s.api(t, http.MethodGet, "/monitors/1", restricted, "", http.StatusOK)
	if !strings.Contains(body, `"auto_create_incident":"publish"`) || !strings.Contains(body, `"auto_resolve_incident":"all"`) {
		t.Fatalf("updating unrelated settings changed the incident policies: %s", body)
	}
}

func TestAPIReplacesMonitorChannelAssignmentsAtomically(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	monitor := defaultMonitor()
	monitor.Name, monitor.Target = "Web", "https://example.test"
	if _, err := s.store.CreateMonitor(ctx, monitor, 1); err != nil {
		t.Fatal(err)
	}
	channel := store.AlertChannel{Kind: "email", URL: "fixture@example.test"}
	if _, err := s.store.CreateAlertChannel(ctx, channel, 1); err != nil {
		t.Fatal(err)
	}

	token := createAPIToken(t, s, "monitors:read", "monitors:update")
	const path = "/monitors/1/alert-channels"
	s.api(t, http.MethodPut, path, token, `{"alert_channel_ids":["999"]}`, http.StatusUnprocessableEntity)
	_, body := s.api(t, http.MethodGet, path, token, "", http.StatusOK)
	if !strings.Contains(body, `"alert_channel_ids":["1"]`) || strings.Contains(body, channel.URL) {
		t.Fatal("invalid assignment changed the monitor's channel IDs or exposed the destination")
	}
	s.api(t, http.MethodPut, path, token, `{"alert_channel_ids":[]}`, http.StatusNoContent)
	_, body = s.api(t, http.MethodGet, path, token, "", http.StatusOK)
	if !strings.Contains(body, `"alert_channel_ids":[]`) {
		t.Fatal("an empty replacement did not clear channel assignments")
	}
}
