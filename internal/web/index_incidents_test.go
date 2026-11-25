package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uppies/internal/store"
)

func TestIndexIncidentHistoryShowsPublishedSummariesFromEveryPage(t *testing.T) {
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	check(err)
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	s, err := New(st, slog.New(slog.NewTextHandler(t.Output(), nil)), nil, nil)
	check(err)
	s.now = func() time.Time { return time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC) }

	handler, err := s.Handler()
	check(err)
	get := func(path string, wantStatus int) string {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != wantStatus {
			t.Fatalf("%s: status = %d, want %d", path, w.Code, wantStatus)
		}
		return w.Body.String()
	}
	checkHTML := func(page string, want, unwanted []string) {
		t.Helper()
		for _, text := range want {
			if !strings.Contains(page, text) {
				t.Errorf("missing %q", text)
			}
		}
		for _, text := range unwanted {
			if strings.Contains(page, text) {
				t.Errorf("unexpected %q", text)
			}
		}
	}

	started := time.Date(2025, time.December, 31, 23, 30, 0, 0, time.UTC).UnixMilli()
	_, err = st.CreateFirstUser(ctx, "admin", "test hash", started)
	check(err)
	instance, err := st.Instance(ctx)
	check(err)
	const origin = "https://status.example.test"
	instance.PublicURL = origin
	check(st.SaveInstance(ctx, instance))

	empty := get("/incidents", http.StatusOK)
	checkHTML(empty, []string{"no published incidents to show.", "href=\"/status-pages\""}, []string{"older incidents"})

	core, err := st.CreateProject(ctx, "core", "Core & Checkout", started)
	check(err)
	edge, err := st.CreateProject(ctx, "edge", "Edge", started)
	check(err)
	project, err := st.ProjectByID(ctx, core)
	check(err)
	project.Timezone = "Asia/Manila"
	check(st.UpdateProject(ctx, project))

	create := func(projectID int64, title, status string, at int64, published bool) int64 {
		t.Helper()
		id, err := st.CreateIncident(ctx, store.Incident{
			ProjectID: projectID, Title: title, Impact: "major", Status: status,
			StartedAtMs: at, Published: published,
		}, store.IncidentUpdate{Body: "incident update body"}, at+2*time.Hour.Milliseconds())
		check(err)
		return id
	}
	create(edge, "Older ongoing incident", "investigating", started-time.Hour.Milliseconds(), true)
	monitor := func(name string) int64 {
		t.Helper()
		id, err := st.CreateMonitor(ctx, store.Monitor{
			Name: name, Kind: "http", Target: "https://example.test", IntervalS: 60, TimeoutMs: 5000,
			Retries: 1, RetryIntervalS: 20, RecoveryChecks: 1,
		}, started)
		check(err)
		check(st.PlaceMonitor(ctx, core, id))
		return id
	}
	payments, ledger := monitor("Payments API"), monitor("Internal ledger")
	checkout, err := st.CreateIncident(ctx, store.Incident{
		ProjectID: core, Title: "Checkout <orders>", Impact: "major", Status: "resolved",
		StartedAtMs: started, Published: true, MonitorIDs: []int64{payments, ledger},
	}, store.IncidentUpdate{Body: "incident update body"}, started+2*time.Hour.Milliseconds())
	check(err)
	// The ledger leaves the page, so the homepage stops naming it, as the page does.
	check(st.SetPlacements(ctx, core, []store.Placement{{MonitorID: payments, Display: "bars"}}))
	check(st.AddIncidentUpdate(ctx, checkout, store.IncidentUpdate{
		Status: "resolved", Title: "Payments are back", Body: "Every **order** went through.", AtMs: started + 3*time.Hour.Milliseconds(),
	}))
	archiveBoundary := create(core, "First tied incident", "resolved", started, true)
	for i := range 18 {
		create(core, fmt.Sprintf("Tied incident %02d", i), "resolved", started, true)
	}
	create(edge, "Active network incident", "monitoring", started, true)
	create(edge, "Unpublished incident", "investigating", started+1, false)
	create(edge, "Earlier year incident", "resolved", time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), true)

	for _, path := range []string{"/", "/status-pages?before=0.1"} {
		page := get(path, http.StatusOK)
		if count := strings.Count(page, `<article class="tl-incident">`); count != 5 {
			t.Fatalf("%s: preview has %d incidents, want 5", path, count)
		}
		checkHTML(page, []string{
			"href=\"/incidents\">full incident history", ">dec 31, 2025</h3>",
			"href=\"/s/core/history?year=2026\"", "Core &amp; Checkout",
			"href=\"/s/edge/history?year=2025\"", "Active network incident", "monitoring",
			`<div class="update-body md"><p>incident update body</p>`,
		}, []string{"Unpublished incident", "Older ongoing incident"})
	}

	olderURL := fmt.Sprintf("/incidents?before=%d.%d", started, archiveBoundary)
	first := get("/incidents?before=invalid&utm_source=test", http.StatusOK)
	checkHTML(first, []string{
		"href=\"" + olderURL + "\"",
		"rel=\"canonical\" href=\"" + origin + "/incidents\"",
		"Active network incident", "First tied incident", ">dec 31, 2025</h3>",
		`class="heat" role="img" aria-label="0 incidents in 2026`, `href="/incidents?year=2025">2025</a>`,
	}, []string{"Unpublished incident", "Checkout &lt;orders&gt;", "Older ongoing incident"})

	older := get(olderURL, http.StatusOK)
	checkHTML(older, []string{
		"rel=\"canonical\" href=\"" + origin + olderURL + "\"",
		"href=\"/incidents\">newest incidents",
		"Checkout &lt;orders&gt;", "Older ongoing incident", "Earlier year incident",
		"<strong>Payments are back</strong>", `datetime="2026-01-01T02:30:00Z"`,
		"<p>Every <strong>order</strong> went through.</p>", `datetime="2026-01-01T01:30:00Z"`,
		`<span class="chip chip-plain">Payments API</span>`,
	}, []string{"First tied incident", "older incidents", "Internal ledger"})
	// The archive shows an incident's updates newest first, as a status page does.
	_, checkoutArticle, _ := strings.Cut(older, "<h4>Checkout &lt;orders&gt;</h4>")
	checkoutArticle, _, _ = strings.Cut(checkoutArticle, "</article>")
	if strings.Index(checkoutArticle, "Payments are back") > strings.Index(checkoutArticle, "incident update body") {
		t.Error("the archive must list an incident's updates newest first")
	}

	year := get("/incidents?year=2025", http.StatusOK)
	checkHTML(year, []string{
		`class="heat" role="img" aria-label="22 incidents in 2025`, `class="heat-cell heat-2" data-tip="dec 31, 2025: 22 incidents"`,
		`href="/incidents?year=2026">2026</a>`, `href="/incidents">all years</a>`,
		`href="` + olderURL + `&amp;year=2025">older incidents</a>`,
		`rel="canonical" href="` + origin + `/incidents?year=2025"`,
	}, []string{"Unpublished incident", "Earlier year incident", "Checkout &lt;orders&gt;"})

	yearOlder := get(olderURL+"&year=2025", http.StatusOK)
	checkHTML(yearOlder, []string{
		"Checkout &lt;orders&gt;", "Older ongoing incident",
		`class="heat" role="img" aria-label="22 incidents in 2025`,
		`href="/incidents?year=2025">newest incidents</a>`,
		`rel="canonical" href="` + origin + olderURL + `&amp;year=2025"`,
	}, []string{"Unpublished incident", "Earlier year incident", "First tied incident", "older incidents"})

	instance.PublicList = false
	check(st.SaveInstance(ctx, instance))
	hidden := get(olderURL, http.StatusNotFound)
	checkHTML(hidden, nil, []string{"Checkout", "Edge", "Older ongoing incident"})
}
