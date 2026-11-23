package web

import (
	"context"
	"encoding/xml"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"uppies/internal/store"
)

func TestPublicSearchMetadataAndSitemaps(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	form := url.Values{"name": {"Acme Status"}, "public_list": {"on"}, "public_url": {"https://STATUS.example.test/"}}
	if resp := s.post(t, s.admin, "/admin/instance", form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save public URL: got %d", resp.StatusCode)
	}
	for _, invalid := range []string{"javascript:alert(1)", "https://status.example.test/?a=b", "https://user@status.example.test/", "https://status.example.test/path"} {
		form.Set("public_url", invalid)
		if resp := s.post(t, s.admin, "/admin/instance", form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid public origin %q: got %d", invalid, resp.StatusCode)
		}
	}
	makePage := func(slug, domain string, noIndex bool) int64 {
		t.Helper()
		id, err := s.store.CreateProject(ctx, slug, slug, 1000)
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.store.ProjectByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		p.Domain, p.NoIndex = domain, noIndex
		if err := s.store.UpdateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := makePage("app", "", false)
	makePage("unlisted", "", true)
	makePage("custom", "custom.example.test", false)
	makePage("private-domain", "private.example.test", true)
	monitor, err := s.createShown(ctx, id, store.Monitor{
		Name: "API", Kind: "http", Target: "https://example.test", IntervalS: 60, TimeoutMs: 500,
		RetryIntervalS: 3, RecoveryChecks: 1,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}

	get := func(host, path string, status int) (http.Header, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, s.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("X-Forwarded-Host", "ignored.example.test")
		resp, err := s.anonymous.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s%s: got %d, want %d", host, path, resp.StatusCode, status)
		}
		return resp.Header, string(body)
	}

	const origin = "https://status.example.test"
	const custom = "https://custom.example.test"
	monitorPath := "/s/app/monitors/" + strconv.FormatInt(monitor, 10)
	for _, tc := range []struct{ host, path, canonical string }{
		{"alias.example.test", "/", origin + "/"},
		{"alias.example.test", "/s/app?tz=UTC&utm_source=external", origin + "/s/app"},
		{"status.example.test", "/s/app/history?year=2020&tz=UTC", origin + "/s/app/history?year=2020"},
		{"status.example.test", monitorPath, origin + monitorPath},
		{"custom.example.test", "/", custom + "/"},
		{"status.example.test", "/s/custom", custom + "/"},
		{"status.example.test", "/s/custom/history", custom + "/s/custom/history"},
	} {
		_, page := get(tc.host, tc.path, http.StatusOK)
		for _, tag := range []string{
			`<link rel="canonical" href="` + tc.canonical + `">`,
			`<meta property="og:url" content="` + tc.canonical + `">`,
			`<meta property="og:image" content="https://`,
			`<meta property="og:title" content="`,
			`<meta property="og:description" content="`,
			`<meta name="twitter:card" content="summary_large_image">`,
		} {
			if !strings.Contains(page, tag) {
				t.Errorf("%s%s missing %s", tc.host, tc.path, tag)
			}
		}
	}
	noIndex := `<meta name="robots" content="noindex">`
	for _, path := range []string{"/s/unlisted", "/s/unlisted/history"} {
		_, page := get("status.example.test", path, http.StatusOK)
		if !strings.Contains(page, noIndex) {
			t.Errorf("%s missing noindex", path)
		}
	}
	if headers, _ := get("status.example.test", "/admin/login", http.StatusOK); headers.Get("X-Robots-Tag") != "noindex" {
		t.Error("sign-in must be noindex")
	}
	if headers, _ := get("alias.example.test", "/sitemap.xml", http.StatusTemporaryRedirect); headers.Get("Location") != origin+"/sitemap.xml" {
		t.Error("alias sitemap must point to the configured origin")
	}
	checkSitemap := func(host string, want []string) {
		t.Helper()
		headers, body := get(host, "/sitemap.xml", http.StatusOK)
		if !strings.HasPrefix(headers.Get("Content-Type"), "application/xml") {
			t.Fatalf("sitemap content type: %s", headers.Get("Content-Type"))
		}
		var doc sitemap
		if err := xml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		var urls []string
		for _, entry := range doc.URLs {
			urls = append(urls, entry.Location)
		}
		if !slices.Equal(urls, want) {
			t.Errorf("%s sitemap: got %v, want %v", host, urls, want)
		}
	}
	checkSitemap("status.example.test", []string{origin + "/", origin + "/s/app", origin + "/s/app/history"})
	checkSitemap("custom.example.test", []string{custom + "/", custom + "/s/custom/history"})
	get("private.example.test", "/sitemap.xml", http.StatusNotFound)
	_, privateRobots := get("private.example.test", "/robots.txt", http.StatusOK)
	if strings.Contains(privateRobots, "Sitemap:") {
		t.Error("robots.txt should omit a sitemap when every page on the host is noindex")
	}
	_, robots := get("custom.example.test", "/robots.txt", http.StatusOK)
	if !strings.Contains(robots, "Sitemap: "+custom+"/sitemap.xml\n") || strings.Contains(robots, "Disallow: /") {
		t.Errorf("custom robots: %q", robots)
	}

	form = url.Values{"name": {"Acme Status"}}
	if resp := s.post(t, s.admin, "/admin/instance", form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("hide public list: got %d", resp.StatusCode)
	}
	get("status.example.test", "/sitemap.xml", http.StatusNotFound)
	_, page := get("status.example.test", "/", http.StatusOK)
	if !strings.Contains(page, noIndex) {
		t.Error("disabled public list must be noindex")
	}

	_, preview := get("status.example.test", socialImagePath, http.StatusOK)
	size, err := png.DecodeConfig(strings.NewReader(preview))
	if err != nil || size.Width != 1200 || size.Height != 630 {
		t.Fatalf("social preview: %v, %v", size, err)
	}
}
