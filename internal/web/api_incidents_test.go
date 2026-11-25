package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestAPIIncidentCRUDUsesPageOwnershipAndThePublicationLifecycle(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	if _, err := s.store.CreateProject(ctx, "one", "One", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateProject(ctx, "two", "Two", 1); err != nil {
		t.Fatal(err)
	}
	m := defaultMonitor()
	m.Name, m.Target = "Database", "https://example.test"
	if _, err := s.createShown(ctx, 2, m, 1); err != nil {
		t.Fatal(err)
	}
	editor := createAPIToken(t, s, "incidents:read", "incidents:create", "incidents:update", "incidents:delete")
	publisher := createAPIToken(t, s, "incidents:publish")
	const collection = "/status-pages/1/incidents"
	crossPage := `{"title":"Database unavailable","message":"Checking","monitor_ids":["1"]}`
	s.api(t, http.MethodPost, collection, editor, crossPage, http.StatusUnprocessableEntity)
	_, empty := s.api(t, http.MethodGet, collection, editor, "", http.StatusOK)
	if !strings.Contains(empty, `"items":[]`) {
		t.Fatal("the rejected incident was persisted")
	}

	s.api(t, http.MethodPost, collection, editor,
		`{"title":"Database unavailable","update_title":" Checking dependencies ","message":"Checking"}`, http.StatusCreated)
	const path = collection + "/1"
	_, firstUpdate := s.api(t, http.MethodGet, path+"/updates", editor, "", http.StatusOK)
	if !strings.Contains(firstUpdate, `"title":"Checking dependencies"`) {
		t.Error("the first update is missing its title")
	}
	const wrongPage = "/status-pages/2/incidents/1"
	s.api(t, http.MethodGet, wrongPage, editor, "", http.StatusNotFound)
	s.api(t, http.MethodPatch, wrongPage, editor, `{"title":"Wrong page"}`, http.StatusNotFound)
	s.api(t, http.MethodPost, wrongPage+"/updates", editor, `{"status":"monitoring","body":"Wrong page"}`, http.StatusNotFound)
	s.api(t, http.MethodGet, wrongPage+"/updates", editor, "", http.StatusNotFound)
	s.api(t, http.MethodPost, wrongPage+"/publish", publisher, "", http.StatusNotFound)
	s.api(t, http.MethodDelete, wrongPage, editor, "", http.StatusNotFound)
	s.api(t, http.MethodPost, path+"/publish", editor, "", http.StatusForbidden)

	s.api(t, http.MethodPatch, path, editor, `{"title":"Invalid edit","monitor_ids":["1"]}`, http.StatusUnprocessableEntity)
	_, draft := s.api(t, http.MethodGet, path, editor, "", http.StatusOK)
	if !strings.Contains(draft, `"title":"Database unavailable"`) || !strings.Contains(draft, `"published":false`) {
		t.Fatal("an invalid edit changed the draft")
	}
	if err := s.store.PlaceMonitor(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	s.api(t, http.MethodPatch, path, editor, `{"impact":"major","monitor_ids":["1"]}`, http.StatusNoContent)
	t.Run("concurrent publication", func(t *testing.T) {
		for _, name := range []string{"first", "second"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				s.api(t, http.MethodPost, path+"/publish", publisher, "", http.StatusNoContent)
			})
		}
	})
	oversized := `{"status":"resolved","title":"` + strings.Repeat("x", 201) + `","body":"Invalid update"}`
	s.api(t, http.MethodPost, path+"/updates", editor, oversized, http.StatusUnprocessableEntity)
	s.api(t, http.MethodPost, path+"/updates", editor,
		`{"status":"resolved","title":"Database back online","body":"Recovered"}`, http.StatusNoContent)
	_, incident := s.api(t, http.MethodGet, path, editor, "", http.StatusOK)
	for _, want := range []string{`"published":true`, `"impact":"major"`, `"monitor_ids":["1"]`, `"status":"resolved"`} {
		if !strings.Contains(incident, want) {
			t.Errorf("incident after updates is missing %s", want)
		}
	}
	_, updates := s.api(t, http.MethodGet, path+"/updates?after=1", editor, "", http.StatusOK)
	if !strings.Contains(updates, `"title":"Database back online"`) || !strings.Contains(updates, `"body":"Recovered"`) ||
		strings.Contains(updates, `"body":"Checking"`) {
		t.Fatal("updates did not paginate the incident timeline")
	}
	_, published := s.get(t, s.anonymous, "/s/one")
	for _, heading := range []string{"Checking dependencies", "Database back online"} {
		if !strings.Contains(published, "<strong>"+heading+"</strong>") {
			t.Errorf("the public timeline is missing the update heading %q", heading)
		}
	}
	events, err := s.store.Events(ctx, 1, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("publishing twice and appending one update produced %d events, err=%v, want 2", len(events), err)
	}
	s.api(t, http.MethodDelete, path, editor, "", http.StatusNoContent)
	s.api(t, http.MethodGet, path, editor, "", http.StatusNotFound)
}
