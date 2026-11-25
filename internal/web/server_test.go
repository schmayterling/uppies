package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
	"strconv"
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
	srv, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {}, nil)
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

// createShown adds a monitor and puts it on a status page.
func (s *site) createShown(ctx context.Context, projectID int64, m store.Monitor, nowMs int64) (int64, error) {
	id, err := s.store.CreateMonitor(ctx, m, nowMs)
	if err != nil {
		return 0, err
	}
	return id, s.store.PlaceMonitor(ctx, projectID, id)
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

func TestResetPasswordGivesANewPasswordAndSignsOut(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	if _, err := ResetPassword(ctx, s.store, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown account: got err=%v, want ErrNotFound", err)
	}
	password, err := ResetPassword(ctx, s.store, "may")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < minPasswordLen {
		t.Fatalf("new password %q is shorter than %d characters", password, minPasswordLen)
	}
	if status, _ := s.get(t, s.admin, "/admin"); status != http.StatusSeeOther {
		t.Errorf("the old session: got %d, want a redirect to sign in", status)
	}
	old := url.Values{"username": {"may"}, "password": {"correct-horse-battery"}}
	if resp := s.post(t, s.anonymous, "/admin/login", old, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the old password: got %d, want 401", resp.StatusCode)
	}
	fresh := url.Values{"username": {"may"}, "password": {password}}
	if resp := s.post(t, s.anonymous, "/admin/login", fresh, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("the new password: got %d, want 303", resp.StatusCode)
	}
}

func TestStatusPageShowsOnlyWhatIsPublic(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.createShown(ctx, projectID, store.Monitor{
		Name: "payments api", Kind: "http", Target: "http://10.0.0.7:9000/internal-health",
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
	checkedAt := time.UnixMilli(nowMs).UTC().Format(time.RFC3339)
	for _, want := range []string{`<h1 class="banner-text">everything is down.</h1>`, "payments api", `last check <time datetime="` + checkedAt + `"`} {
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
	_, history, _ := strings.Cut(page, `<section aria-labelledby="history-heading">`)
	history, _, _ = strings.Cut(history, "</section>")
	if !strings.Contains(history, "payments api is down") {
		t.Error("past incidents is missing the published active incident")
	}
	if strings.Contains(page, "10.0.0.7") {
		t.Error("publishing the draft put the raw check error on the status page")
	}
}

func TestAlertChannelsBelongToTheInstallAndMonitorsChooseThem(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	var delivered atomic.Int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
	}))
	defer chat.Close()
	const alerts = "/admin/alerts"

	resp := s.post(t, s.anonymous, alerts, url.Values{"kind": {"slack"}, "url": {chat.URL}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous add: got %d to %q, want 303 to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = s.post(t, s.admin, alerts, url.Values{"kind": {"slack"}, "url": {"ftp://example.com"}}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("add with an ftp url: got %d, want 422", resp.StatusCode)
	}
	resp = s.post(t, s.admin, alerts, url.Values{"kind": {"email"}, "url": {chat.URL}}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("add an email channel with a url: got %d, want 422", resp.StatusCode)
	}
	resp = s.post(t, s.admin, alerts, url.Values{"kind": {"slack"}, "url": {chat.URL}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add: got %d, want 303", resp.StatusCode)
	}
	channels, err := s.store.AlertChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("got %d channel(s), want exactly the one added by the admin", len(channels))
	}
	channel := fmt.Sprintf("%s/%d", alerts, channels[0].ID)
	if status, page := s.get(t, s.admin, alerts); status != http.StatusOK || !strings.Contains(page, chat.URL) {
		t.Errorf("alerts page: got %d, want 200 with the channel listed", status)
	}

	resp = s.post(t, s.admin, channel+"/test", nil, nil)
	if resp.StatusCode != http.StatusOK || delivered.Load() != 1 {
		t.Fatalf("test alert: got %d with %d delivery, want 200 with 1", resp.StatusCode, delivered.Load())
	}

	// A monitor chooses its channels in its own form. A new one starts with all of them offered.
	form := url.Values{
		"name": {"api"}, "kind": {"http"}, "target": {"https://example.com"}, "interval_s": {"60"}, "timeout_s": {"5"},
		"retries": {"2"}, "retry_interval_s": {"20"}, "recovery_checks": {"2"},
		"channel": {strconv.FormatInt(channels[0].ID, 10)},
	}
	if resp := s.post(t, s.admin, "/admin/monitors", form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create a monitor: got %d, want 303", resp.StatusCode)
	}
	monitors, err := s.store.Monitors(ctx)
	if err != nil || len(monitors) != 1 {
		t.Fatalf("monitors: got %d err=%v, want the one just made", len(monitors), err)
	}
	if alerted, err := s.store.MonitorChannels(ctx, monitors[0].ID); err != nil || len(alerted) != 1 {
		t.Fatalf("channels of the new monitor: got %d err=%v, want 1", len(alerted), err)
	}
	form.Del("channel")
	if resp := s.post(t, s.admin, fmt.Sprintf("/admin/monitors/%d", monitors[0].ID), form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save the monitor without channels: got %d, want 303", resp.StatusCode)
	}
	if alerted, err := s.store.MonitorChannels(ctx, monitors[0].ID); err != nil || len(alerted) != 0 {
		t.Fatalf("channels after unticking: got %d err=%v, want 0", len(alerted), err)
	}

	if resp := s.post(t, s.admin, alerts+"/999/delete", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("remove a channel that does not exist: got %d, want 404", resp.StatusCode)
	}
	resp = s.post(t, s.admin, channel+"/delete", nil, nil)
	channels, err = s.store.AlertChannels(ctx)
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
		Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
	monitor.Name = "website"
	websiteID, err := s.createShown(ctx, projectID, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor.Name = "internal queue"
	hiddenID, err := s.store.CreateMonitor(ctx, monitor, 1) // on no status page
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
		"timezone": {"UTC"}, "description": {"We run **checkout** for Acme."},
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
	// The description is Markdown above the status. The tagline is not repeated on the page.
	description := `<div class="page-description md"><p>We run <strong>checkout</strong> for Acme.</p>`
	if at, banner := strings.Index(page, description), strings.Index(page, `class="banner `); at < 0 || at > banner {
		t.Errorf("the description must come before the status, found at %d with the status at %d", at, banner)
	}
	if strings.Contains(page, `<p class="muted">how our services are doing</p>`) {
		t.Error("the tagline must not show under the status any more")
	}
	older := maps.Clone(valid)
	delete(older, "description")
	if resp := s.post(t, s.admin, settings, older, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a form without the description: got %d, want 303", resp.StatusCode)
	}
	if p, err := s.store.ProjectByID(ctx, projectID); err != nil || p.Description != "We run **checkout** for Acme." {
		t.Errorf("a form without the description must keep it, got %q err=%v", p.Description, err)
	}
	tooLong := maps.Clone(valid)
	tooLong.Set("description", strings.Repeat("é", 2001))
	if resp := s.post(t, s.admin, settings, tooLong, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a description over 2000 characters: got %d, want 422", resp.StatusCode)
	}

	status, theme := s.get(t, s.anonymous, "/s/acme/theme.css")
	wantTheme := ":root {\n  color-scheme: dark;\n  --accent: light-dark(oklch(0.45 0.075 320), oklch(0.8 0.075 320));\n}\n.banner { border-width: 3px; }\n"
	if status != http.StatusOK || theme != wantTheme {
		t.Errorf("theme.css: got %d %q, want 200 %q", status, theme, wantTheme)
	}
}

func TestNavbarBrandControlsThePublicHeader(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	id, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf("/admin/projects/%d/settings", id)
	form := url.Values{
		"name": {"Acme"}, "theme": {"auto"}, "accent_hue": {"200"}, "history_days": {"90"}, "timezone": {"UTC"},
	}
	brand := func(t *testing.T, path, text, asset, target string) {
		t.Helper()
		status, page := s.get(t, s.anonymous, path)
		_, header, found := strings.Cut(page, `<header class="site-bar">`)
		link, _, closed := strings.Cut(header, "</a>")
		if status != http.StatusOK || !found || !closed || !strings.Contains(link, `aria-label="Acme"`) || !strings.Contains(link, `href="`+target+`"`) {
			t.Fatalf("%s: a public brand link with its accessible name is missing", path)
		}
		visibleText := strings.TrimSpace(link[strings.LastIndexByte(link, '>')+1:])
		if visibleText != text {
			t.Errorf("%s: brand text %q, want %q", path, visibleText, text)
		}
		if asset != "" && !strings.Contains(link, asset) || asset == "" && strings.Contains(link, "<img") {
			t.Errorf("%s: unexpected branding image in %q, want %q", path, link, asset)
		}
	}
	logo := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><rect width="20" height="20"/></svg>`)
	if err := s.store.SetProjectImage(ctx, id, store.ImageLogo, "image/svg+xml", logo, 1); err != nil {
		t.Fatal(err)
	}
	brand(t, "/s/acme", "Acme", "/images/logo", "/s/acme")
	for _, c := range []struct{ mode, text, asset string }{
		{"icon", "", "/images/logo"},
		{"name", "Acme", ""},
		{"icon-name", "Acme", "/images/logo"},
	} {
		form.Set("navbar_brand", c.mode)
		if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusSeeOther {
			t.Fatalf("%s: save returned %d", c.mode, got)
		}
		brand(t, "/s/acme", c.text, c.asset, "/s/acme")
		brand(t, "/s/acme/history", c.text, c.asset, "/s/acme")
	}
	form.Set("navbar_brand", "icon")
	if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusSeeOther {
		t.Fatalf("icon save returned %d", got)
	}
	form.Del("navbar_brand")
	if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusSeeOther {
		t.Fatalf("older form returned %d", got)
	}
	if err := s.store.DeleteProjectImage(ctx, id, store.ImageLogo); err != nil {
		t.Fatal(err)
	}
	brand(t, "/s/acme", "", "icons.svg#globe", "/s/acme")
	if err := s.store.SetProjectImage(ctx, id, store.ImageFavicon, "image/svg+xml", logo, 2); err != nil {
		t.Fatal(err)
	}
	form.Set("navbar_brand", "invalid")
	if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("invalid navbar choice returned %d", got)
	}
	brand(t, "/s/acme", "", "/images/favicon", "/s/acme")

	form.Del("navbar_brand")
	form.Set("navbar_link", "homepage")
	if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusUnprocessableEntity {
		t.Fatalf("missing homepage returned %d", got)
	}
	form.Set("homepage_url", "https://acme.example/docs")
	if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusSeeOther {
		t.Fatalf("homepage link save returned %d", got)
	}
	brand(t, "/s/acme/history", "", "/images/favicon", "https://acme.example/docs")
	form.Set("navbar_link", "list")
	form.Set("domain", "custom.example.test")
	if got := s.post(t, s.admin, settings, form, nil).StatusCode; got != http.StatusSeeOther {
		t.Fatalf("list link save returned %d", got)
	}
	brand(t, "/s/acme", "", "/images/favicon", "/status-pages")

	const instance = "/admin/instance"
	if got := s.post(t, s.admin, instance, url.Values{
		"name": {"Acme status"}, "public_list": {"on"}, "public_url": {"https://status.example.test"},
	}, nil).StatusCode; got != http.StatusSeeOther {
		t.Fatalf("public origin save returned %d", got)
	}
	brand(t, "/s/acme", "", "/images/favicon", "https://status.example.test/status-pages")

	req, err := http.NewRequest(http.MethodGet, s.server.URL+"/status-pages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "custom.example.test"
	resp, err := s.anonymous.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `<h1 class="visually-hidden">status pages</h1>`) {
		t.Fatalf("custom domain list returned %d, read %v", resp.StatusCode, err)
	}
}

func TestStatusPageFooterTimezoneDomainAndBadge(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.createShown(ctx, projectID, store.Monitor{
		Name: "website", Kind: "http", Target: "https://example.com",
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
	incidentID, err := s.store.CreateIncident(ctx, store.Incident{
		ProjectID: projectID, Title: "slow search", Impact: "minor", Status: "investigating",
		Published: true, StartedAtMs: startedAtMs,
	}, store.IncidentUpdate{Body: "search is slow."}, startedAtMs)
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
	// The default timezone is the install's. It moved from each page's settings to the instance settings.
	instance := url.Values{"name": {"uppies"}, "public_list": {"on"}, "timezone": {"Asia/Manila"}}
	if resp := s.post(t, s.admin, "/admin/instance", instance, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save the instance timezone: got %d, want 303", resp.StatusCode)
	}
	instance.Set("timezone", "Mars/Olympus")
	if resp := s.post(t, s.admin, "/admin/instance", instance, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown instance timezone: got %d, want 422", resp.StatusCode)
	}
	_, instanceHTML := s.get(t, s.admin, "/admin/instance")
	for _, want := range []string{
		`<select id="timezone" name="timezone"`, `<option value="Asia/Manila" selected>Asia/Manila</option>`,
		`<option value="America/New_York">America/New_York</option>`, `<option value="Pacific/Auckland">Pacific/Auckland</option>`,
		`<option value="Etc/GMT&#43;5">Etc/GMT&#43;5</option>`, `<option value="US/Eastern">US/Eastern</option>`,
	} {
		if !strings.Contains(instanceHTML, want) {
			t.Errorf("timezone dropdown is missing %q", want)
		}
	}
	if _, settingsHTML := s.get(t, s.admin, settings); strings.Contains(settingsHTML, `name="timezone"`) {
		t.Error("a status page's settings must not offer a timezone any more")
	}
	if _, admin := s.get(t, s.admin, fmt.Sprintf("/admin/incidents/%d", incidentID)); !strings.Contains(html.UnescapeString(admin), "2026-11-23 12:05 +08:00") {
		t.Error("the admin pages must show times in the instance timezone")
	}
	for field, bad := range map[string]string{"timezone": "Mars/Olympus", "domain": "https://status.acme.example/x"} {
		invalid := url.Values{}
		maps.Copy(invalid, form)
		invalid.Set(field, bad)
		if resp := s.post(t, s.admin, settings, invalid, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s %q: got %d, want 422", field, bad, resp.StatusCode)
		}
	}

	_, page := s.get(t, s.anonymous, "/s/acme")
	page = html.UnescapeString(page) // the template writes "+" as an entity
	// The footer is Markdown, so the address becomes a mail link.
	for _, want := range []string{"2026-11-23 12:05 +08:00", `write to <a href="mailto:help@acme.example">help@acme.example</a>`, `<meta name="robots" content="noindex">`} {
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
	for _, path := range []string{"/s/acme", "/s/acme/history"} {
		status, html := s.get(t, s.anonymous, path)
		if status != http.StatusOK || strings.Count(html, `<link rel="icon"`) != 1 ||
			!strings.Contains(html, `<link rel="icon" href="/s/acme/images/favicon?v=`) {
			t.Errorf("%s should use the uploaded favicon as its only icon link", path)
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

func TestTheInstallHasAFaviconOfItsOwn(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	if _, err := s.store.CreateProject(ctx, "acme", "Acme", 1); err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	const save = "/admin/appearance/favicon"
	if status, _ := s.get(t, s.anonymous, "/favicon.ico"); status != http.StatusNotFound {
		t.Fatalf("favicon before an upload: got %d, want 404", status)
	}
	if _, page := s.get(t, s.admin, "/admin/appearance"); !strings.Contains(page, `name="favicon"`) || !strings.Contains(page, `<link rel="icon" href="/static/favicon.svg">`) {
		t.Error("appearance must offer a favicon field, and a page links the built-in favicon before one is uploaded")
	}
	if resp := s.upload(t, s.anonymous, save, map[string][]byte{"favicon": png}, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous upload: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := s.upload(t, s.admin, save, map[string][]byte{"favicon": []byte("<html></html>")}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a web page as the favicon: got %d, want 422", resp.StatusCode)
	}
	if resp := s.upload(t, s.admin, save, map[string][]byte{"favicon": png}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: got %d, want 303", resp.StatusCode)
	}
	if status, _ := s.get(t, s.anonymous, "/favicon.ico"); status != http.StatusOK {
		t.Errorf("favicon after the upload: got %d, want 200", status)
	}
	pages := map[string]*http.Client{"/admin": s.admin, "/admin/login": s.anonymous, "/": s.anonymous}
	for path, client := range pages {
		if _, page := s.get(t, client, path); !strings.Contains(page, `<link rel="icon" href="/favicon.ico?v=`) {
			t.Errorf("%s does not link the favicon", path)
		}
	}
	// A status page without a favicon of its own uses the install's.
	if _, page := s.get(t, s.anonymous, "/s/acme"); !strings.Contains(page, `<link rel="icon" href="/favicon.ico?v=`) {
		t.Error("a status page without its own favicon does not link the install's")
	}
	if resp := s.upload(t, s.admin, save, nil, map[string]string{"remove_favicon": "on"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: got %d, want 303", resp.StatusCode)
	}
	if status, _ := s.get(t, s.anonymous, "/favicon.ico"); status != http.StatusNotFound {
		t.Errorf("favicon after removal: got %d, want 404", status)
	}
}

func TestTheInstallLogoTakesTheStatusIconsPlaceOnThePublicList(t *testing.T) {
	s := newSite(t)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	brand := func(path string) string {
		t.Helper()
		_, page := s.get(t, s.anonymous, path)
		_, link, found := strings.Cut(page, `<a class="site-brand" href="/status-pages">`)
		if !found {
			t.Fatalf("%s has no site name link", path)
		}
		link, _, _ = strings.Cut(link, "</a>")
		return link
	}
	const save = "/admin/appearance/logo"
	if b := brand("/"); !strings.Contains(b, "status-icon") || strings.Contains(b, "<img") {
		t.Errorf("before an upload the site name shows the status icon, got %q", b)
	}
	if status, _ := s.get(t, s.anonymous, "/logo"); status != http.StatusNotFound {
		t.Fatalf("logo before an upload: got %d, want 404", status)
	}
	if resp := s.upload(t, s.anonymous, save, map[string][]byte{"logo": png}, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous upload: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := s.upload(t, s.admin, save, map[string][]byte{"logo": []byte("<html></html>")}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a web page as the logo: got %d, want 422", resp.StatusCode)
	}
	if resp := s.upload(t, s.admin, save, map[string][]byte{"logo": png}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: got %d, want 303", resp.StatusCode)
	}
	for _, path := range []string{"/", "/incidents"} {
		if b := brand(path); !strings.Contains(b, `<img class="logo" src="/logo?v=`) || strings.Contains(b, "status-icon") {
			t.Errorf("%s: the logo must replace the status icon, got %q", path, b)
		}
	}
	signInBrand := func() string {
		t.Helper()
		_, page := s.get(t, s.anonymous, "/admin/login")
		_, card, _ := strings.Cut(page, `<p class="auth-brand">`)
		card, _, _ = strings.Cut(card, "</p>")
		return card
	}
	if b := signInBrand(); !strings.Contains(b, `<img class="logo" src="/logo?v=`) || strings.Contains(b, "status-icon") {
		t.Errorf("the sign-in page must show the logo in place of the check, got %q", b)
	}
	resp, err := s.anonymous.Get(s.server.URL + "/logo")
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
	if !bytes.Equal(served, png) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("the logo must be served as uploaded and sandboxed, got %d bytes and policy %q", len(served), resp.Header.Get("Content-Security-Policy"))
	}
	if resp := s.upload(t, s.admin, save, nil, map[string]string{"remove_logo": "on"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: got %d, want 303", resp.StatusCode)
	}
	if b := brand("/"); !strings.Contains(b, "status-icon") {
		t.Errorf("after removal the site name shows the status icon again, got %q", b)
	}
	if b := signInBrand(); !strings.Contains(b, "status-icon-check") {
		t.Errorf("after removal the sign-in page shows the check again, got %q", b)
	}
}

func TestSignInPageLayoutAndBackgroundAreChosenInAppearance(t *testing.T) {
	s := newSite(t)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	const save = "/admin/appearance/sign-in"

	_, page := s.get(t, s.anonymous, "/admin/login")
	for _, want := range []string{`class="auth auth-split"`, `class="auth-art auth-art-gradient"`} {
		if !strings.Contains(page, want) {
			t.Errorf("fresh sign-in page is missing %q", want)
		}
	}
	if resp := s.upload(t, s.anonymous, save, map[string][]byte{"background": png}, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous save: got %d to %q, want a redirect to /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}

	refused := []struct {
		name   string
		files  map[string][]byte
		fields map[string]string
	}{
		{"image chosen with no file", nil, map[string]string{"login_background": "image"}},
		{"a web page as the image", map[string][]byte{"background": []byte("<html><script>alert(1)</script></html>")}, nil},
		{"a file over 2 MB", map[string][]byte{"background": append(bytes.Clone(png), make([]byte, store.MaxBackgroundBytes)...)}, nil},
		{"an unknown layout", nil, map[string]string{"login_layout": "sideways"}},
	}
	for _, c := range refused {
		if resp := s.upload(t, s.admin, save, c.files, c.fields); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422", c.name, resp.StatusCode)
		}
	}
	if status, _ := s.get(t, s.anonymous, "/sign-in-background"); status != http.StatusNotFound {
		t.Fatalf("background after refused saves: got %d, want 404", status)
	}

	// A real background is far larger than an ordinary form may be.
	large := append(bytes.Clone(png), make([]byte, 1<<20)...)
	fields := map[string]string{"login_layout": "centered", "login_background": "image"}
	if resp := s.upload(t, s.admin, save, map[string][]byte{"background": large}, fields); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: got %d, want 303", resp.StatusCode)
	}
	_, page = s.get(t, s.anonymous, "/admin/login")
	for _, want := range []string{`class="auth auth-centered"`, `class="auth-art auth-art-image"`, `<img src="/sign-in-background?v=`} {
		if !strings.Contains(page, want) {
			t.Errorf("sign-in page after the save is missing %q", want)
		}
	}
	if status, _ := s.get(t, s.anonymous, "/sign-in-background"); status != http.StatusOK {
		t.Errorf("background: got %d, want 200", status)
	}

	// Removing the image while it is still the chosen background is refused.
	if resp := s.upload(t, s.admin, save, nil, map[string]string{"remove_background": "on"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("removing the image in use: got %d, want 422", resp.StatusCode)
	}
	fields = map[string]string{"login_background": "plain", "remove_background": "on"}
	if resp := s.upload(t, s.admin, save, nil, fields); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: got %d, want 303", resp.StatusCode)
	}
	if status, _ := s.get(t, s.anonymous, "/sign-in-background"); status != http.StatusNotFound {
		t.Errorf("background after removal: got %d, want 404", status)
	}
	if _, page = s.get(t, s.anonymous, "/admin/login"); !strings.Contains(page, `class="auth-art auth-art-plain"`) {
		t.Error("sign-in page after choosing plain still shows art")
	}
}

func TestMonitorsBelongToTheInstallAndKeepTheirOrder(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := s.get(t, s.anonymous, "/admin/monitors"); status != http.StatusSeeOther {
		t.Fatalf("monitors without a session: got %d, want 303", status)
	}
	form := url.Values{
		"name": {"database"}, "kind": {"tcp"}, "target": {"db.example.com:5432"}, "interval_s": {"60"}, "timeout_s": {"5"},
		"retries": {"2"}, "retry_interval_s": {"20"}, "recovery_checks": {"2"},
	}
	// One monitor on no status page, and one on a page.
	form.Set("tags", " Production, data   team, production, ")
	if resp := s.post(t, s.admin, "/admin/monitors", form, nil); resp.Header.Get("Location") != "/admin/monitors" {
		t.Fatalf("create on no page: got %d to %q, want a redirect to the list", resp.StatusCode, resp.Header.Get("Location"))
	}
	form.Set("name", "website")
	form.Set("tags", "private check")
	form.Set("page", strconv.FormatInt(projectID, 10))
	if resp := s.post(t, s.admin, "/admin/monitors", form, nil); resp.Header.Get("Location") != fmt.Sprintf("/admin/projects/%d", projectID) {
		t.Fatalf("create on a page: got %d to %q, want a redirect to the page", resp.StatusCode, resp.Header.Get("Location"))
	}
	inOrder := func(page string, order []string) {
		t.Helper()
		at := 0
		for _, want := range order {
			next := strings.Index(page[at:], want)
			if next < 0 {
				t.Fatalf("monitors page: %q is missing or out of order, wanted the order %v", want, order)
			}
			at += next + len(want)
		}
	}
	_, page := s.get(t, s.admin, "/admin/monitors")
	inOrder(page, []string{"database", "on none", "website", "Acme"})
	// The list is arranged by sending its items in the new order.
	monitors, err := s.store.Monitors(ctx)
	if err != nil || len(monitors) != 2 {
		t.Fatalf("monitors: got %d err=%v, want 2", len(monitors), err)
	}
	if monitors[0].Tags != "Production, data team" || monitors[1].Tags != "private check" {
		t.Fatalf("monitor tags: got %q and %q", monitors[0].Tags, monitors[1].Tags)
	}
	if !strings.Contains(html.UnescapeString(page), `<option value="data+team">data team</option>`) {
		t.Error("the monitor list should offer multiword tags as filter options")
	}
	form.Del("tags")
	form.Set("name", monitors[0].Name)
	edit := fmt.Sprintf("/admin/monitors/%d", monitors[0].ID)
	if resp := s.post(t, s.admin, edit, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("older edit form: got %d, want 303", resp.StatusCode)
	}
	if updated, err := s.store.Monitor(ctx, monitors[0].ID); err != nil || updated.Tags != monitors[0].Tags {
		t.Fatalf("older edit form lost the tags: %q, %v", updated.Tags, err)
	}
	form.Set("tags", strings.Repeat("x", 1001))
	if resp := s.post(t, s.admin, edit, form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("overlong tags: got %d, want 422", resp.StatusCode)
	}
	arrange := url.Values{"item": {fmt.Sprintf("m:%d", monitors[1].ID), "m:not-a-number", fmt.Sprintf("m:%d", monitors[0].ID)}}
	if resp := s.post(t, s.admin, "/admin/monitors/order", arrange, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("arrange: got %d, want 303", resp.StatusCode)
	}
	_, page = s.get(t, s.admin, "/admin/monitors")
	inOrder(page, []string{"website", "Acme", "database", "on none"})
	// Only the monitor that was put on the page is public there.
	if _, status := s.get(t, s.anonymous, "/s/acme"); !strings.Contains(status, "website") || strings.Contains(status, "database") || strings.Contains(status, "private check") {
		t.Error("the status page must show the placed monitor without its private tags")
	}
	// Deleting a status page keeps its monitors.
	if resp := s.post(t, s.admin, fmt.Sprintf("/admin/projects/%d/delete", projectID), nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete the status page: got %d, want 303", resp.StatusCode)
	}
	if _, page = s.get(t, s.admin, "/admin/monitors"); !strings.Contains(page, "database") || !strings.Contains(page, "website") {
		t.Error("after deleting a status page, both monitors must remain")
	}
}

func TestAPushMonitorGetsATokenThatTheJobReportsTo(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"name": {"nightly backup"}, "kind": {"push"}, "interval_s": {"3600"}, "timeout_s": {"10"},
		"retries": {"0"}, "retry_interval_s": {"60"}, "recovery_checks": {"1"},
	}
	// The form names the status page the new monitor starts on.
	form.Set("page", strconv.FormatInt(projectID, 10))
	resp := s.post(t, s.admin, "/admin/monitors", form, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: got %d, want 303", resp.StatusCode)
	}
	monitors, err := s.store.MonitorsByProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 1 || len(monitors[0].PushToken) < 20 {
		t.Fatalf("got %+v, want one monitor with a long random token", monitors)
	}
	token := monitors[0].PushToken
	if _, page := s.get(t, s.admin, resp.Header.Get("Location")); !strings.Contains(page, "/push/"+token) {
		t.Error("the monitor page does not show the address the job must call")
	}

	if status, _ := s.get(t, s.anonymous, "/push/not-a-real-token"); status != http.StatusNotFound {
		t.Errorf("unknown token: got %d, want 404", status)
	}
	// A scheduled job is not a browser. Its POST carries no origin and must still be accepted.
	if resp := s.post(t, s.anonymous, "/push/"+token, nil, http.Header{"Sec-Fetch-Site": {"cross-site"}}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("report by POST: got %d, want 204", resp.StatusCode)
	}
	lastPushAtMs, err := s.store.LastPushAtMs(ctx, monitors[0].ID)
	if err != nil || lastPushAtMs == 0 {
		t.Fatalf("after a report: last push %d err=%v, want it recorded", lastPushAtMs, err)
	}
}

func TestPlannedMaintenanceIsAnnouncedAndShownWhileRunning(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.createShown(ctx, projectID, store.Monitor{
		Name: "website", Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	healthy := health.Step{Tracker: health.Tracker{Status: health.Up}}
	if err := s.store.RecordCheck(ctx, store.CheckResult{MonitorID: monitorID, AtMs: time.Now().UnixMilli(), OK: true, Step: healthy}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/admin/projects/%d/maintenance", projectID)
	utc := func(at time.Time) string { return at.UTC().Format("2006-01-02T15:04") }
	now := time.Now()

	backwards := url.Values{"title": {"upgrade"}, "starts": {utc(now.Add(2 * time.Hour))}, "ends": {utc(now.Add(time.Hour))}}
	if resp := s.post(t, s.admin, path, backwards, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a window that ends before it starts: got %d, want 422", resp.StatusCode)
	}
	upcoming := url.Values{"title": {"database upgrade"}, "starts": {utc(now.Add(24 * time.Hour))}, "ends": {utc(now.Add(25 * time.Hour))}}
	if resp := s.post(t, s.anonymous, path, upcoming, nil); resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous: got %d, want a redirect to /admin/login", resp.StatusCode)
	}
	if resp := s.post(t, s.admin, path, upcoming, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upcoming window: got %d, want 303", resp.StatusCode)
	}
	_, page := s.get(t, s.anonymous, "/s/acme")
	if !strings.Contains(page, "database upgrade") || !strings.Contains(page, "everything is up.") {
		t.Error("an upcoming window must be announced while the page still says everything is up")
	}

	running := url.Values{"title": {"network work"}, "starts": {utc(now.Add(-time.Hour))}, "ends": {utc(now.Add(time.Hour))}}
	if resp := s.post(t, s.admin, path, running, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("running window: got %d, want 303", resp.StatusCode)
	}
	_, page = s.get(t, s.anonymous, "/s/acme")
	for _, want := range []string{"planned maintenance is under way.", "under maintenance", "network work"} {
		if !strings.Contains(page, want) {
			t.Errorf("status page during maintenance is missing %q", want)
		}
	}
}

func TestUpdatesShowMarkdownAndLongOnesCollapse(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	nowMs := time.Now().UnixMilli()
	incidentID, err := s.store.CreateIncident(ctx, store.Incident{
		ProjectID: projectID, Title: "checkout errors", Impact: "major", Status: "investigating",
		Published: true, StartedAtMs: nowMs - 60_000,
	}, store.IncidentUpdate{Body: "**payments** fail. __do not retry__.\n-# times are in utc"}, nowMs-60_000)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("the fix is rolling out region by region.\n", 12)
	if err := s.store.AddIncidentUpdate(ctx, incidentID, store.IncidentUpdate{Status: "identified", Body: long, AtMs: nowMs}); err != nil {
		t.Fatal(err)
	}
	windowID, err := s.store.CreateMaintenanceWindow(ctx, store.MaintenanceWindow{
		ProjectID: projectID, Title: "database upgrade", StartsAtMs: nowMs - 60_000, EndsAtMs: nowMs + 3_600_000,
	}, nowMs)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddMaintenanceUpdate(ctx, projectID, windowID, "writes are *paused*", nowMs); err != nil {
		t.Fatal(err)
	}

	_, page := s.get(t, s.anonymous, "/s/acme")
	for _, want := range []string{
		"<strong>payments</strong>", "<u>do not retry</u>", `<p class="md-subtext">times are in utc</p>`, "<em>paused</em>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("status page is missing %q", want)
		}
	}
	// The active incident and the seven-day history both show the long update.
	if got := strings.Count(page, `<details class="update-more">`); got != 2 {
		t.Errorf("got %d read more toggles, want one per copy of the long update", got)
	}
	_, admin := s.get(t, s.admin, fmt.Sprintf("/admin/incidents/%d", incidentID))
	if !strings.Contains(admin, "<u>do not retry</u>") {
		t.Error("the admin incident page must show updates as they will look in public")
	}
}

func TestScopedMaintenanceNoticeAndEndedHistory(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor := defaultMonitor()
	monitor.Name, monitor.Target = "api", "https://example.test"
	selected, err := s.createShown(ctx, projectID, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor.Name = "web"
	uncovered, err := s.createShown(ctx, projectID, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for id, state := range map[int64]health.Status{selected: health.Down, uncovered: health.Up} {
		result := store.CheckResult{MonitorID: id, AtMs: now.UnixMilli(), OK: state == health.Up,
			Step: health.Step{Tracker: health.Tracker{Status: state}}}
		if err := s.store.RecordCheck(ctx, result); err != nil {
			t.Fatal(err)
		}
	}

	collection := fmt.Sprintf("/admin/projects/%d/maintenance", projectID)
	form := url.Values{"title": {"Open database work"}, "starts": {now.Add(-time.Hour).Format(dateTimeLayout)},
		"until_further_notice": {"1"}, "monitor": {strconv.FormatInt(selected, 10)}}
	window := collection + "/1"
	if response := s.post(t, s.admin, collection, form, nil); response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != window {
		t.Fatalf("scoped open window: got %d to %q, want 303 to %s", response.StatusCode, response.Header.Get("Location"), window)
	}
	update := url.Values{"body": {"The first task has finished."}}
	if response := s.post(t, s.admin, window+"/updates", update, nil); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("add update: got %d, want 303", response.StatusCode)
	}
	_, public := s.get(t, s.anonymous, "/s/acme")
	if !strings.Contains(public, "everything is up.") || strings.Count(public, "under maintenance") != 1 ||
		!strings.Contains(public, "until further notice") || !strings.Contains(public, "The first task has finished.") {
		t.Fatal("scoped notice did not mask just the selected monitor and show its update")
	}

	other, err := s.store.CreateProject(ctx, "other", "Other", now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.PlaceMonitor(ctx, other, selected); err != nil {
		t.Fatal(err)
	}
	listRow := func(slug string) string {
		t.Helper()
		_, list := s.get(t, s.anonymous, "/status-pages")
		_, after, found := strings.Cut(list, `href="/s/`+slug+`"`)
		row, _, closed := strings.Cut(after, "</li>")
		if !found || !closed {
			t.Fatalf("missing row for %s", slug)
		}
		return row
	}
	if row := listRow("acme"); !strings.Contains(row, "1 of 1 up") || !strings.Contains(row, "all up") {
		t.Error("the public list must count only the uncovered monitor")
	}
	if row := listRow("other"); !strings.Contains(row, "0 of 1 up") || !strings.Contains(row, "problems") {
		t.Error("the same monitor remains down on a page without maintenance")
	}

	if err := s.store.SetPlacements(ctx, projectID, []store.Placement{
		{MonitorID: selected}, {MonitorID: uncovered, ExcludeFromStatus: true},
	}); err != nil {
		t.Fatal(err)
	}
	_, public = s.get(t, s.anonymous, "/s/acme")
	if !strings.Contains(public, "planned maintenance is under way.") {
		t.Fatal("the only contributing monitor is covered, so the page should say maintenance")
	}
	if !strings.Contains(listRow("acme"), "under maintenance") {
		t.Error("the list must report maintenance when every contributing monitor is covered")
	}
	if response := s.post(t, s.admin, window+"/end", nil, nil); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("end window: got %d, want 303", response.StatusCode)
	}
	_, public = s.get(t, s.anonymous, "/s/acme")
	if strings.Contains(public, "Open database work") || !strings.Contains(public, "everything is down.") {
		t.Fatal("ending the window did not restore component status and retire the notice")
	}
	if row := listRow("acme"); !strings.Contains(row, "0 of 1 up") || !strings.Contains(row, "problems") {
		t.Error("ending maintenance must restore the list's problem status")
	}
	_, history := s.get(t, s.admin, collection)
	_, detail := s.get(t, s.admin, window)
	if !strings.Contains(history, "Open database work") || !strings.Contains(detail, "The first task has finished.") {
		t.Fatal("ended window and its update are missing from admin history")
	}
	if response := s.post(t, s.admin, window+"/delete", nil, nil); response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != collection {
		t.Fatalf("delete window: got %d to %q, want 303 to %s", response.StatusCode, response.Header.Get("Location"), collection)
	}
	if status, _ := s.get(t, s.admin, window); status != http.StatusNotFound {
		t.Errorf("removed window returned %d, want 404", status)
	}
}

func TestOverviewSumsUpEveryProject(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	nowMs := time.Now().UnixMilli()
	monitor := store.Monitor{
		Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
	for _, slug := range []string{"alpha", "beta"} {
		projectID, err := s.store.CreateProject(ctx, slug, slug+" project", 1)
		if err != nil {
			t.Fatal(err)
		}
		monitor.Name = slug + " api"
		monitorID, err := s.createShown(ctx, projectID, monitor, 1)
		if err != nil {
			t.Fatal(err)
		}
		step := health.Step{Tracker: health.Tracker{Status: health.Up}}
		if slug == "beta" {
			step = health.Step{Tracker: health.Tracker{Status: health.Down, FailingSinceMs: nowMs}, Change: health.WentDown}
		}
		err = s.store.RecordCheck(ctx, store.CheckResult{MonitorID: monitorID, AtMs: nowMs, OK: slug == "alpha", Error: "status 503", Step: step})
		if err != nil {
			t.Fatal(err)
		}
	}

	status, page := s.get(t, s.admin, "/admin")
	if status != http.StatusOK {
		t.Fatalf("overview: got %d, want 200", status)
	}
	page = html.UnescapeString(page)
	for _, want := range []string{
		`<span class="stat-value">1</span> <span class="meta">of 2</span>`, // one of two monitors is up
		`<span class="stat-value state-down">1</span>`,                     // one is down
		"beta api is down",
		"a draft incident is ready to publish",
		"alpha project", "beta project", "1 down", // the sidebar lists both projects and flags the one with an outage
	} {
		if !strings.Contains(page, want) {
			t.Errorf("overview is missing %q", want)
		}
	}
	if got := strings.Count(page, "beta api is down"); got != 1 {
		t.Errorf("the outage and its draft incident appear %d times, want one line for both", got)
	}
}

func TestEasyLookSettingsReachTheThemeStylesheet(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf("/admin/projects/%d/settings", projectID)
	form := url.Values{
		"name": {"Acme"}, "theme": {"auto"}, "accent_hue": {"200"}, "history_days": {"90"}, "timezone": {"UTC"},
		"surface": {"paper"}, "corners": {"round"}, "font": {"mono"}, "accent_hue_custom": {"312"},
	}
	if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: got %d, want 303", resp.StatusCode)
	}
	_, theme := s.get(t, s.anonymous, "/s/acme/theme.css")
	for _, want := range []string{
		"oklch(0.45 0.075 312)",                          // the typed hue wins over the swatch
		"--background: light-dark(oklch(0.985 0.022 93)", // paper
		"--radius-control: 10px",                         // round corners
		"--font-ui: var(--font-mono)",                    // mono typeface
	} {
		if !strings.Contains(theme, want) {
			t.Errorf("theme.css is missing %q", want)
		}
	}

	form.Set("corners", "wobbly")
	if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown corner style: got %d, want 422", resp.StatusCode)
	}
	if p, err := s.store.ProjectByID(ctx, projectID); err != nil || p.Corners != "round" {
		t.Fatalf("after the refused save: corners %q err=%v, want round kept", p.Corners, err)
	}
}

func TestIncidentHistoryPageListsAYearAndHidesDrafts(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	lastYear := time.Date(now.Year()-1, time.June, 15, 12, 0, 0, 0, time.UTC)
	seed := []struct {
		title     string
		published bool
		at        time.Time
	}{
		{"checkout errors", true, now.Add(-time.Hour)},
		{"secret draft", false, now.Add(-2 * time.Hour)},
		{"summer outage", true, lastYear},
	}
	for _, in := range seed {
		_, err := s.store.CreateIncident(ctx, store.Incident{
			ProjectID: projectID, Title: in.title, Impact: "major", Status: "resolved",
			Published: in.published, StartedAtMs: in.at.UnixMilli(),
		}, store.IncidentUpdate{Body: "details."}, in.at.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}

	status, page := s.get(t, s.anonymous, "/s/acme/history")
	if status != http.StatusOK || !strings.Contains(page, "checkout errors") {
		t.Fatalf("this year: got %d, want 200 with this year's incident", status)
	}
	for _, unwanted := range []string{"secret draft", "summer outage"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("this year's history shows %q", unwanted)
		}
	}
	_, page = s.get(t, s.anonymous, fmt.Sprintf("/s/acme/history?year=%d", now.Year()-1))
	if !strings.Contains(page, "summer outage") || strings.Contains(page, "checkout errors") {
		t.Error("last year's history must show only last year's incident")
	}
	if _, page := s.get(t, s.anonymous, "/s/acme"); !strings.Contains(page, "without incidents") || !strings.Contains(page, "/s/acme/history") {
		t.Error("the status page must list quiet days and link to the history")
	}
	if _, page := s.get(t, s.anonymous, "/s/acme/history"); !strings.Contains(page, "without incidents") {
		t.Error("the history lists quiet days unless asked not to")
	}
	// Hiding quiet days is the page owner's choice, not a visitor's.
	if _, page := s.get(t, s.anonymous, "/s/acme/history?quiet=hide"); !strings.Contains(page, "without incidents") {
		t.Error("a visitor must not be able to hide quiet days from the address")
	}
	project, err := s.store.ProjectBySlug(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	project.HideQuietDays = true
	if err := s.store.UpdateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	_, page = s.get(t, s.anonymous, "/s/acme/history")
	if strings.Contains(page, "without incidents") || strings.Contains(page, "quiet days") || !strings.Contains(page, "checkout errors") {
		t.Error("with the setting on, the history shows only days with incidents and no toggle")
	}
	if _, page := s.get(t, s.anonymous, "/s/acme"); strings.Contains(page, "without incidents") {
		t.Error("with the setting on, the status page lists no quiet days")
	}
}

func TestStatusPageGroupsMonitorsByItsOwnCategories(t *testing.T) {
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
	monitor := store.Monitor{
		Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
	var layout, plain []store.Placement
	for _, m := range []struct{ name, category string }{
		{"loose check", ""}, {"zebra api", "web"}, {"alpha site", "web"}, {"database", "infrastructure"},
	} {
		monitor.Name = m.name
		id, err := s.store.CreateMonitor(ctx, monitor, 1)
		if err != nil {
			t.Fatal(err)
		}
		layout = append(layout, store.Placement{MonitorID: id, Category: m.category})
		plain = append(plain, store.Placement{MonitorID: id})
	}
	// The same monitors are on two pages. Only the first page sorts them into categories.
	if err := s.store.SetPageLayout(ctx, projectID, []string{"web", "infrastructure"}, layout); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetPageLayout(ctx, otherID, nil, plain); err != nil {
		t.Fatal(err)
	}
	_, page := s.get(t, s.anonymous, "/s/acme")
	order := []string{"loose check", "<h3>web</h3>", "zebra api", "alpha site", "<h3>infrastructure</h3>", "database"}
	at := 0
	for _, want := range order {
		next := strings.Index(page[at:], want)
		if next < 0 {
			t.Fatalf("status page: %q is missing or out of order, wanted the order %v", want, order)
		}
		at += next + len(want)
	}
	if got := strings.Count(page, "<h3>web</h3>"); got != 1 {
		t.Errorf("the web heading appears %d times, want once", got)
	}
	if _, other := s.get(t, s.anonymous, "/s/other"); strings.Contains(other, "<h3>web</h3>") || !strings.Contains(other, "zebra api") {
		t.Error("the other page shows the same monitors and must have no category headings")
	}
}

func TestInstancePagesAndUsers(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	if _, err := s.store.CreateProject(ctx, "acme", "Acme", 1); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/appearance", "/admin/instance", "/admin/users"} {
		if status, _ := s.get(t, s.anonymous, path); status != http.StatusSeeOther {
			t.Errorf("%s without a session: got %d, want 303", path, status)
		}
		if status, _ := s.get(t, s.admin, path); status != http.StatusOK {
			t.Errorf("%s signed in: got %d, want 200", path, status)
		}
	}

	_, instance := s.get(t, s.admin, "/admin/instance")
	for _, want := range []string{
		"server uptime", "server timezone", "sqlite · schema", "<span>status pages</span><small>1</small>",
		"<span>monitors</span><small>0 total, 0 paused</small>", "<span>admin accounts</span><small>1</small>",
	} {
		if !strings.Contains(instance, want) {
			t.Errorf("instance details are missing %q", want)
		}
	}

	// Appearance reaches the admin stylesheet. Instance settings reach the front page.
	s.post(t, s.admin, "/admin/appearance", url.Values{"theme": {"dark"}, "accent_hue": {"25"}}, nil)
	if _, css := s.get(t, s.anonymous, "/theme.css"); !strings.Contains(css, "color-scheme: dark") || !strings.Contains(css, "0.075 25)") {
		t.Errorf("theme.css after saving appearance: %q", css)
	}
	if _, page := s.get(t, s.anonymous, "/"); !strings.Contains(page, "Acme") || strings.Contains(page, "index-footer") {
		t.Error("the front page lists status pages without a footer by default")
	}
	if resp := s.post(t, s.admin, "/admin/instance", url.Values{
		"name": {"acme ops"}, "public_list": {"on"}, "nav_links": {"docs | https://docs.example.com/"},
		"list_announcement": {"Planned work <today>.\nAn update will follow."}, "list_announcement_color": {"amber"},
		"list_footer_text": {"Reach the <ops> team"}, "list_show_powered_by": {"on", "off"},
	}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save homepage branding: got %d, want 303", resp.StatusCode)
	}
	if resp := s.post(t, s.admin, "/admin/instance", url.Values{
		"name": {"acme ops"}, "public_list": {"on"}, "list_footer_text": {strings.Repeat("a", 501)},
	}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("overlong footer: got %d, want 422", resp.StatusCode)
	}
	if resp := s.post(t, s.admin, "/admin/instance", url.Values{
		"name": {"acme ops"}, "public_list": {"on"}, "nav_links": {"docs | javascript:alert(1)"},
	}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unsafe navbar link: got %d, want 422", resp.StatusCode)
	}
	if resp := s.post(t, s.admin, "/admin/instance", url.Values{
		"name": {"acme ops"}, "public_list": {"on"}, "list_announcement_color": {"unknown"},
	}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown announcement color: got %d, want 422", resp.StatusCode)
	}
	// The list has three layouts. Each one names the page and links to it.
	for layout, marker := range map[string]string{"rows": `class="index-rows"`, "cards": `class="index-cards"`, "compact": `class="rows index-list"`} {
		s.post(t, s.admin, "/admin/appearance", url.Values{"theme": {"dark"}, "accent_hue": {"25"}, "list_layout": {layout}}, nil)
		_, page := s.get(t, s.anonymous, "/")
		if hasStrip := strings.Contains(page, `class="index-uptime"`); hasStrip != (layout == "rows") {
			t.Errorf("front page in the %s layout has the wrong uptime strip visibility", layout)
		}
		if strings.Contains(page, "<h1>status pages</h1>") {
			t.Errorf("front page in the %s layout has a visible duplicate heading", layout)
		}
		notice := strings.Index(page, `class="page-announcement md announcement-amber"`)
		if notice < 0 || notice >= strings.Index(page, marker) || !strings.Contains(page, "Planned work &lt;today&gt;.<br>\nAn update will follow.") {
			t.Errorf("the %s layout must show the escaped pinned announcement above the list", layout)
		}
		for _, want := range []string{
			marker, "Acme", `href="/s/acme"`, "<title>acme ops status</title>", `href="https://docs.example.com/"`,
			`<h1 class="visually-hidden">status pages</h1>`, "Reach the &lt;ops&gt; team", `powered by <a href="https://github.com/schmayterling/uppies">uppies</a>.`,
			`href="/status-pages" aria-current="page">status</a>`, `href="/incidents">incidents</a>`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("front page in the %s layout is missing %q", layout, want)
			}
		}
		status, archive := s.get(t, s.anonymous, "/incidents")
		if status != http.StatusOK {
			t.Fatalf("incident archive: got %d, want 200", status)
		}
		for _, want := range []string{
			`class="site-bar"`, `class="site-brand" href="/status-pages"`, `>acme ops</a>`,
			`href="/status-pages">status</a>`, `href="/incidents" aria-current="page">incidents</a>`,
			`href="https://docs.example.com/"`, "Planned work &lt;today&gt;.<br>\nAn update will follow.",
			`class="page-foot meta index-footer"`, "Reach the &lt;ops&gt; team", `powered by <a href="https://github.com/schmayterling/uppies">uppies</a>.`,
		} {
			if !strings.Contains(archive, want) {
				t.Errorf("incident archive with list layout %s is missing %q", layout, want)
			}
		}
	}
	if resp := s.post(t, s.admin, "/admin/appearance", url.Values{"theme": {"dark"}, "accent_hue": {"25"}, "list_layout": {"sideways"}}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown list layout: got %d, want 422", resp.StatusCode)
	}
	s.post(t, s.admin, "/admin/instance", url.Values{"name": {"acme ops"}}, nil)
	_, page := s.get(t, s.anonymous, "/")
	if strings.Contains(page, "Acme") || strings.Contains(page, "docs.example.com") ||
		strings.Contains(page, "Planned work") || !strings.Contains(page, "acme ops") {
		t.Error("with the public list off, the front page shows the instance name and no status page")
	}
	if !strings.Contains(page, "Reach the &lt;ops&gt; team") || !strings.Contains(page, `powered by <a href="https://github.com/schmayterling/uppies">uppies</a>.`) {
		t.Error("an older form must keep the footer settings")
	}
	homepage := url.Values{"name": {"acme ops"}, "public_list": {"on"}, "list_show_powered_by": {"off"}}
	if resp := s.post(t, s.admin, "/admin/instance", homepage, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("hide the footer credit: got %d, want 303", resp.StatusCode)
	}
	_, page = s.get(t, s.anonymous, "/")
	if !strings.Contains(page, "Reach the &lt;ops&gt; team") || strings.Contains(page, `powered by <a href="https://github.com/schmayterling/uppies">uppies</a>.`) {
		t.Error("hiding the credit must keep the homepage footer text")
	}
	if !strings.Contains(page, "Planned work &lt;today&gt;.") {
		t.Error("an older form must keep the homepage announcement")
	}
	homepage.Set("list_footer_text", "")
	homepage.Set("list_announcement", "")
	if resp := s.post(t, s.admin, "/admin/instance", homepage, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("clear homepage footer and announcement: got %d, want 303", resp.StatusCode)
	}
	if _, page := s.get(t, s.anonymous, "/"); strings.Contains(page, "index-footer") || strings.Contains(page, "page-announcement") {
		t.Error("an empty footer and announcement must be omitted")
	}
	if _, page := s.get(t, s.anonymous, "/s/acme"); !strings.Contains(page, `powered by <a href="https://github.com/schmayterling/uppies">uppies</a>.`) {
		t.Error("the homepage setting must not control the credit on a status page")
	}

	// Users: add one, refuse a short password, refuse removing yourself, change the password.
	if resp := s.post(t, s.admin, "/admin/users", url.Values{"username": {"sam"}, "password": {"short"}}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a short password: got %d, want 422", resp.StatusCode)
	}
	if resp := s.post(t, s.admin, "/admin/users", url.Values{"username": {"sam"}, "password": {"a-long-password"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add user: got %d, want 303", resp.StatusCode)
	}
	users, err := s.store.Users(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("users: got %d err=%v, want 2", len(users), err)
	}
	if resp := s.post(t, s.admin, fmt.Sprintf("/admin/users/%d/delete", users[0].ID), nil, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("removing your own account: got %d, want 422", resp.StatusCode)
	}
	wrong := url.Values{"current": {"not-my-password"}, "new": {"another-long-password"}}
	if resp := s.post(t, s.admin, "/admin/users/password", wrong, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a wrong current password: got %d, want 422", resp.StatusCode)
	}
	right := url.Values{"current": {"correct-horse-battery"}, "new": {"another-long-password"}}
	if resp := s.post(t, s.admin, "/admin/users/password", right, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("change password: got %d, want 303", resp.StatusCode)
	}
	if status, _ := s.get(t, s.admin, "/admin"); status != http.StatusOK {
		t.Errorf("the session that changed the password must stay signed in: got %d", status)
	}
	login := url.Values{"username": {"may"}, "password": {"another-long-password"}}
	if resp := s.post(t, s.anonymous, "/admin/login", login, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("signing in with the new password: got %d, want 303", resp.StatusCode)
	}
	if resp := s.post(t, s.admin, fmt.Sprintf("/admin/users/%d/delete", users[1].ID), nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("removing another account: got %d, want 303", resp.StatusCode)
	}
}

func TestLookbackLengthAndBackfillAreFree(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, err := s.createShown(ctx, projectID, store.Monitor{
		Name: "website", Kind: "http", Target: "https://example.com",
		IntervalS: 60, TimeoutMs: 5000, Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	healthy := health.Step{Tracker: health.Tracker{Status: health.Up}}
	// The check covers a minute, so that there is measured time to compute an uptime from.
	measured := store.CheckResult{MonitorID: monitorID, AtMs: time.Now().UnixMilli(), OK: true, CoveredMs: 60_000, Step: healthy}
	if err := s.store.RecordCheck(ctx, measured); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf("/admin/projects/%d/settings", projectID)
	form := url.Values{
		"name": {"Acme"}, "theme": {"auto"}, "accent_hue": {"200"}, "timezone": {"UTC"},
		"history_days": {"200"}, "backfill": {"degraded"},
	}
	if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("200 days with a backfill: got %d, want 303", resp.StatusCode)
	}
	_, page := s.get(t, s.anonymous, "/s/acme")
	// One day has data (today). The other 199 are drawn as degraded and say so.
	if got := strings.Count(page, `class="day day-`); got != 200 {
		t.Errorf("got %d day cells, want 200", got)
	}
	if got := strings.Count(page, `class="day day-warn" data-tip="`); got != 199 {
		t.Errorf("got %d backfilled days, want 199", got)
	}
	if !strings.Contains(page, "no data, shown as degraded") || !strings.Contains(page, "100% uptime") {
		t.Error("a backfilled day must say it has no data, and the uptime must count measured time only")
	}
	for _, days := range []string{"6", "366"} {
		form.Set("history_days", days)
		if resp := s.post(t, s.admin, settings, form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s days: got %d, want 422", days, resp.StatusCode)
		}
	}
}

func TestAVisitorCanPickTheTimezone(t *testing.T) {
	ctx := context.Background()
	s := newSite(t)
	projectID, err := s.store.CreateProject(ctx, "acme", "Acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-23 04:05 UTC is 13:05 in Tokyo, which is nine hours ahead.
	at := time.Date(2026, 11, 23, 4, 5, 0, 0, time.UTC).UnixMilli()
	_, err = s.store.CreateIncident(ctx, store.Incident{
		ProjectID: projectID, Title: "slow search", Impact: "minor", Status: "investigating", Published: true, StartedAtMs: at,
	}, store.IncidentUpdate{Body: "search is slow."}, at)
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		_, page := s.get(t, s.anonymous, path)
		return html.UnescapeString(page)
	}
	page := read("/s/acme")
	if !strings.Contains(page, "2026-11-23 04:05 utc") || !strings.Contains(page, `data-zone="UTC"`) || !strings.Contains(page, "/static/tz.js?v=") {
		t.Error("without a choice the page shows the install's zone, UTC here, and loads the script that reads the browser's")
	}
	page = read("/s/acme?tz=Asia/Tokyo")
	// The template writes the slash in a link's query as %2f, which means the same.
	if !strings.Contains(page, "2026-11-23 13:05 +09:00") || !strings.Contains(page, `history?tz=Asia%2fTokyo`) {
		t.Error("with tz=Asia/Tokyo the page shows Tokyo time and carries the choice into its links")
	}
	if page := read("/s/acme?tz=Mars/Olympus"); !strings.Contains(page, "2026-11-23 04:05 utc") {
		t.Error("an unknown zone falls back to the install's zone")
	}

	// tz.js keeps the browser's zone in a cookie. New York is five hours behind UTC in November.
	browser := func(path, zone string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, s.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "tz", Value: zone})
		resp, err := s.anonymous.Do(req)
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
		return html.UnescapeString(string(body))
	}
	page = browser("/s/acme", "America/New_York")
	if !strings.Contains(page, "2026-11-22 23:05 -05:00") || !strings.Contains(page, `data-zone="America/New_York"`) || strings.Contains(page, "history?tz=America") {
		t.Error("the browser's zone shows times in it without adding it to links")
	}
	if page := browser("/s/acme?tz=Asia/Tokyo", "America/New_York"); !strings.Contains(page, "2026-11-23 13:05 +09:00") {
		t.Error("a zone picked in the address wins over the browser's")
	}
	if page := browser("/s/acme", "../../etc/passwd"); !strings.Contains(page, "2026-11-23 04:05 utc") {
		t.Error("a cookie that is not a zone falls back to the install's zone")
	}
}

func TestANewProjectStartsInTheSystemTimezone(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	s := newSite(t)
	resp := s.post(t, s.admin, "/admin/projects", url.Values{"name": {"Acme"}, "slug": {"acme"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: got %d, want 303", resp.StatusCode)
	}
	p, err := s.store.ProjectBySlug(context.Background(), "acme")
	if err != nil || p.Timezone != "Asia/Tokyo" {
		t.Fatalf("new project: timezone %q err=%v, want the system zone Asia/Tokyo", p.Timezone, err)
	}
}
