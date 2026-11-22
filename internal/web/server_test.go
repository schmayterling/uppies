package web

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uppies/internal/health"
	"uppies/internal/store"
)

type site struct {
	store  *store.Store
	server *httptest.Server
	// admin keeps the session cookie. Neither client follows redirects.
	admin     *http.Client
	anonymous *http.Client
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func newSite(t *testing.T) *site {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "uppies.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{
		store:     st,
		server:    server,
		admin:     &http.Client{Jar: jar, CheckRedirect: noRedirect},
		anonymous: &http.Client{CheckRedirect: noRedirect},
	}
	resp := s.post(t, s.admin, "/admin/setup", url.Values{"username": {"may"}, "password": {"correct-horse-battery"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: got status %d, want 303", resp.StatusCode)
	}
	return s
}

func (s *site) post(t *testing.T, client *http.Client, path string, form url.Values, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	maps.Copy(req.Header, header)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp
}

func (s *site) get(t *testing.T, client *http.Client, path string) (int, string) {
	t.Helper()
	resp, err := client.Get(s.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func (s *site) projectCount(t *testing.T) int {
	t.Helper()
	projects, err := s.store.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(projects)
}

func TestAdminRoutesRefuseRequestsWithoutASession(t *testing.T) {
	s := newSite(t)
	form := url.Values{"name": {"Acme"}, "slug": {"acme"}}

	resp := s.post(t, s.anonymous, "/admin/projects", form, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous post: got %d to %q, want 303 to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if status, _ := s.get(t, s.anonymous, "/admin"); status != http.StatusSeeOther {
		t.Fatalf("anonymous dashboard: got %d, want 303", status)
	}
	if n := s.projectCount(t); n != 0 {
		t.Fatalf("an anonymous post created %d project(s)", n)
	}

	resp = s.post(t, s.admin, "/admin/projects", form, nil)
	if resp.StatusCode != http.StatusSeeOther || s.projectCount(t) != 1 {
		t.Fatalf("signed-in post: got %d and %d project(s), want 303 and 1", resp.StatusCode, s.projectCount(t))
	}
}

func TestACrossSitePostIsRefused(t *testing.T) {
	s := newSite(t)
	form := url.Values{"name": {"Acme"}, "slug": {"acme"}}
	resp := s.post(t, s.admin, "/admin/projects", form, http.Header{"Sec-Fetch-Site": {"cross-site"}})
	if resp.StatusCode != http.StatusForbidden || s.projectCount(t) != 0 {
		t.Fatalf("cross-site post: got %d and %d project(s), want 403 and 0", resp.StatusCode, s.projectCount(t))
	}
}

func TestSetupCannotRunTwice(t *testing.T) {
	s := newSite(t)
	s.post(t, s.anonymous, "/admin/setup", url.Values{"username": {"eve"}, "password": {"another-long-password"}}, nil)
	if _, _, err := s.store.UserPasswordHash(context.Background(), "eve"); err != store.ErrNotFound {
		t.Fatalf("a second setup created a user: err=%v, want ErrNotFound", err)
	}
}

func TestSignInRejectsAWrongPasswordAndThenLocksOut(t *testing.T) {
	s := newSite(t)
	wrong := url.Values{"username": {"may"}, "password": {"not-the-password"}}
	for attempt := 1; attempt <= maxLoginFailures; attempt++ {
		if resp := s.post(t, s.anonymous, "/admin/login", wrong, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", attempt, resp.StatusCode)
		}
	}
	right := url.Values{"username": {"may"}, "password": {"correct-horse-battery"}}
	if resp := s.post(t, s.anonymous, "/admin/login", right, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: got %d, want 429", maxLoginFailures, resp.StatusCode)
	}
}

func TestStatusPageShowsOnlyWhatIsPublic(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.store.CreateMonitor(ctx, store.Monitor{
		ProjectID: projectID, Name: "payments api", Kind: "http", Target: "http://10.0.0.7:9000/internal-health",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	nowMs := time.Now().UnixMilli()
	outage := health.Step{Tracker: health.Tracker{Status: health.Down, FailingSinceMs: nowMs - 40_000}, Change: health.WentDown}
	err = s.store.RecordCheck(ctx, store.CheckResult{
		MonitorID: monitorID, AtMs: nowMs, Error: "dial tcp 10.0.0.7:9000: connection refused", Step: outage,
	})
	if err != nil {
		t.Fatal(err)
	}

	status, page := s.get(t, s.anonymous, "/s/acme")
	if status != http.StatusOK {
		t.Fatalf("status page: got %d, want 200", status)
	}
	for _, want := range []string{"everything is down.", "payments api"} {
		if !strings.Contains(page, want) {
			t.Errorf("status page is missing %q", want)
		}
	}
	for _, private := range []string{"10.0.0.7", "internal-health", "payments api is down"} {
		if strings.Contains(page, private) {
			t.Errorf("status page leaks %q", private)
		}
	}

	incidents, err := s.store.IncidentsByProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.PublishIncident(ctx, incidents[0].ID, nowMs); err != nil {
		t.Fatal(err)
	}
	_, page = s.get(t, s.anonymous, "/s/acme")
	if !strings.Contains(page, "payments api is down") {
		t.Error("a published incident is missing from the status page")
	}
	if strings.Contains(page, "10.0.0.7") {
		t.Error("publishing the draft put the raw check error on the status page")
	}
}
