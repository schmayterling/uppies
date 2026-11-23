package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
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
		ProjectID: projectID, Name: "payments api", Kind: "http", Target: "http://10.0.0.7:9000/internal-health", Public: true,
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

func TestAlertChannelsAreManagedFromSettings(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := s.store.CreateProject(ctx, "other", "Other", 1)
	if err != nil {
		t.Fatal(err)
	}
	var delivered atomic.Int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
	}))
	defer chat.Close()
	settings := fmt.Sprintf("/admin/projects/%d", projectID)

	resp := s.post(t, s.anonymous, settings+"/channels", url.Values{"kind": {"slack"}, "url": {chat.URL}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous add: got %d to %q, want 303 to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = s.post(t, s.admin, settings+"/channels", url.Values{"kind": {"slack"}, "url": {"ftp://example.com"}}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("add with an ftp url: got %d, want 422", resp.StatusCode)
	}
	resp = s.post(t, s.admin, settings+"/channels", url.Values{"kind": {"slack"}, "url": {chat.URL}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add: got %d, want 303", resp.StatusCode)
	}
	channels, err := s.store.AlertChannels(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("got %d channel(s), want exactly the one added by the admin", len(channels))
	}
	channel := fmt.Sprintf("/channels/%d", channels[0].ID)

	resp = s.post(t, s.admin, settings+channel+"/test", nil, nil)
	if resp.StatusCode != http.StatusOK || delivered.Load() != 1 {
		t.Fatalf("test alert: got %d with %d delivery, want 200 with 1", resp.StatusCode, delivered.Load())
	}
	resp = s.post(t, s.admin, fmt.Sprintf("/admin/projects/%d%s/delete", otherID, channel), nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("remove through another project: got %d, want 404", resp.StatusCode)
	}
	resp = s.post(t, s.admin, settings+channel+"/delete", nil, nil)
	channels, err = s.store.AlertChannels(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther || len(channels) != 0 {
		t.Fatalf("remove: got %d with %d channel(s) left, want 303 with 0", resp.StatusCode, len(channels))
	}
}

func TestStatusPageFollowsTheProjectSettings(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor := store.Monitor{
		ProjectID: projectID, Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
	monitor.Name, monitor.Public = "website", true
	websiteID, err := s.store.CreateMonitor(ctx, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor.Name, monitor.Public = "internal queue", false
	hiddenID, err := s.store.CreateMonitor(ctx, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	nowMs := time.Now().UnixMilli()
	outage := health.Step{Tracker: health.Tracker{Status: health.Down, FailingSinceMs: nowMs}, Change: health.WentDown}
	if err := s.store.RecordCheck(ctx, store.CheckResult{MonitorID: hiddenID, AtMs: nowMs, Step: outage}); err != nil {
		t.Fatal(err)
	}
	healthy := health.Step{Tracker: health.Tracker{Status: health.Up}}
	if err := s.store.RecordCheck(ctx, store.CheckResult{MonitorID: websiteID, AtMs: nowMs, OK: true, Step: healthy}); err != nil {
		t.Fatal(err)
	}

	settings := fmt.Sprintf("/admin/projects/%d/settings", projectID)
	valid := url.Values{
		"name": {"Acme Cloud"}, "tagline": {"how our services are doing"}, "homepage_url": {"https://acme.example"},
		"theme": {"dark"}, "accent_hue": {"320"}, "history_days": {"30"}, "custom_css": {".banner { border-width: 3px; }"},
		"timezone": {"UTC"},
	}
	invalid := url.Values{
		"name": {"Acme Cloud"}, "homepage_url": {"javascript:alert(1)"},
		"theme": {"dark"}, "accent_hue": {"361"}, "history_days": {"45"}, "timezone": {"UTC"},
	}
	if resp := s.post(t, s.admin, settings, invalid, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid settings: got %d, want 422", resp.StatusCode)
	}
	if resp := s.post(t, s.anonymous, settings, valid, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous save: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if p, err := s.store.ProjectByID(ctx, projectID); err != nil || p.Name != "Acme" || p.Theme != "auto" {
		t.Fatalf("refused saves changed the project: %+v err=%v", p, err)
	}
	if resp := s.post(t, s.admin, settings, valid, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid settings: got %d, want 303", resp.StatusCode)
	}

	_, page := s.get(t, s.anonymous, "/s/acme")
	for _, want := range []string{"Acme Cloud", "how our services are doing", `href="https://acme.example"`, "website", "everything is up.", "30 days ago"} {
		if !strings.Contains(page, want) {
			t.Errorf("status page is missing %q", want)
		}
	}
	for _, unwanted := range []string{"internal queue", `class="face`, "90 days"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("status page still shows %q", unwanted)
		}
	}
	if got := strings.Count(page, `class="day day-`); got != 30 {
		t.Errorf("got %d day cells, want 30", got)
	}

	status, theme := s.get(t, s.anonymous, "/s/acme/theme.css")
	wantTheme := ":root {\n  color-scheme: dark;\n  --accent: light-dark(oklch(0.45 0.075 320), oklch(0.8 0.075 320));\n}\n.banner { border-width: 3px; }\n"
	if status != http.StatusOK || theme != wantTheme {
		t.Errorf("theme.css: got %d %q, want 200 %q", status, theme, wantTheme)
	}
}

func TestStatusPageFooterTimezoneDomainAndBadge(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.store.CreateMonitor(ctx, store.Monitor{
		ProjectID: projectID, Name: "website", Kind: "http", Target: "https://example.com", Public: true,
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	healthy := health.Step{Tracker: health.Tracker{Status: health.Up}}
	if err := s.store.RecordCheck(ctx, store.CheckResult{MonitorID: monitorID, AtMs: time.Now().UnixMilli(), OK: true, Step: healthy}); err != nil {
		t.Fatal(err)
	}
	// 2026-11-23 04:05 UTC is 12:05 in Manila, which is eight hours ahead.
	startedAtMs := time.Date(2026, 11, 23, 4, 5, 0, 0, time.UTC).UnixMilli()
	_, err = s.store.CreateIncident(ctx, store.Incident{
		ProjectID: projectID, Title: "slow search", Impact: "minor", Status: "investigating",
		Published: true, StartedAtMs: startedAtMs,
	}, "search is slow.", startedAtMs)
	if err != nil {
		t.Fatal(err)
	}

	settings := fmt.Sprintf("/admin/projects/%d/settings", projectID)
	form := url.Values{
		"name": {"Acme"}, "theme": {"auto"}, "accent_hue": {"200"}, "history_days": {"90"},
		"timezone": {"Asia/Manila"}, "domain": {"Status.Acme.Example"}, "footer_text": {"write to help@acme.example"},
		"noindex": {"on"},
	}
	if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: got %d, want 303", resp.StatusCode)
	}
	for field, bad := range map[string]string{"timezone": "Mars/Olympus", "domain": "https://status.acme.example/x"} {
		invalid := url.Values{}
		for key, values := range form {
			invalid[key] = values
		}
		invalid.Set(field, bad)
		if resp := s.post(t, s.admin, settings, invalid, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s %q: got %d, want 422", field, bad, resp.StatusCode)
		}
	}

	_, page := s.get(t, s.anonymous, "/s/acme")
	page = html.UnescapeString(page) // the template writes "+" as an entity
	for _, want := range []string{"2026-11-23 12:05 +08:00", "write to help@acme.example", `<meta name="robots" content="noindex">`} {
		if !strings.Contains(page, want) {
			t.Errorf("status page is missing %q", want)
		}
	}
	if strings.Contains(page, "uppies") {
		t.Error("status page still mentions uppies with the powered-by line switched off")
	}

	req, err := http.NewRequest(http.MethodGet, s.server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "status.acme.example:8443"
	resp, err := s.anonymous.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	front, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(front), "slow search") {
		t.Errorf("front page on the custom domain: got %d, want 200 with the project's status page", resp.StatusCode)
	}
	if status, _ := s.get(t, s.anonymous, "/"); status != http.StatusOK {
		t.Errorf("front page on another host: got %d, want the list of status pages", status)
	}

	status, badge := s.get(t, s.anonymous, "/s/acme/badge.svg")
	if status != http.StatusOK || !strings.Contains(badge, `aria-label="status: incident"`) || !strings.Contains(badge, `fill="#704600"`) {
		t.Errorf("badge: got %d %q, want the incident state in the warning colour", status, badge)
	}
}

// upload posts a multipart form with the given files and plain fields.
func (s *site) upload(t *testing.T, client *http.Client, path string, files map[string][]byte, fields map[string]string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for field, data := range files {
		part, err := form.CreateFormFile(field, field+".bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for field, value := range fields {
		if err := form.WriteField(field, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, s.server.URL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLogoAndFaviconUploads(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script>alert(1)</script></svg>`)
	images := fmt.Sprintf("/admin/projects/%d/images", projectID)

	if resp := s.upload(t, s.anonymous, images, map[string][]byte{"logo": png}, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous upload: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	rejected := map[string][]byte{
		"a web page":         []byte("<html><script>alert(1)</script></html>"),
		"a file over 512 KB": append(bytes.Clone(png), make([]byte, store.MaxImageBytes)...),
	}
	for name, data := range rejected {
		if resp := s.upload(t, s.admin, images, map[string][]byte{"logo": data}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s as a logo: got %d, want 422", name, resp.StatusCode)
		}
	}
	if status, _ := s.get(t, s.anonymous, "/s/acme/images/logo"); status != http.StatusNotFound {
		t.Fatalf("logo after refused uploads: got %d, want 404", status)
	}

	if resp := s.upload(t, s.admin, images, map[string][]byte{"logo": svg, "favicon": png}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: got %d, want 303", resp.StatusCode)
	}
	_, page := s.get(t, s.anonymous, "/s/acme")
	for _, want := range []string{`src="/s/acme/images/logo?v=`, `<link rel="icon" href="/s/acme/images/favicon?v=`} {
		if !strings.Contains(page, want) {
			t.Errorf("status page is missing %q", want)
		}
	}
	resp, err := s.anonymous.Get(s.server.URL + "/s/acme/images/logo")
	if err != nil {
		t.Fatal(err)
	}
	served, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "image/svg+xml" || !bytes.Equal(served, svg) {
		t.Errorf("logo: got type %q and %d bytes, want the uploaded svg", resp.Header.Get("Content-Type"), len(served))
	}
	if policy := resp.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "sandbox") || !strings.Contains(policy, "default-src 'none'") {
		t.Errorf("logo policy: got %q, want a sandbox that allows nothing", policy)
	}

	if resp := s.upload(t, s.admin, images, nil, map[string]string{"remove_logo": "on"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: got %d, want 303", resp.StatusCode)
	}
	if status, _ := s.get(t, s.anonymous, "/s/acme/images/logo"); status != http.StatusNotFound {
		t.Errorf("logo after removal: got %d, want 404", status)
	}
	if status, _ := s.get(t, s.anonymous, "/s/acme/images/favicon"); status != http.StatusOK {
		t.Errorf("favicon after removing only the logo: got %d, want 200", status)
	}
}
