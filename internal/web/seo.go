package web

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"uppies/internal/store"
)

const socialImagePath = "/static/status-preview.png"

func parsePublicOrigin(text string) (url.URL, bool) {
	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.ForceQuery ||
		u.RawQuery != "" || strings.Contains(text, "#") {
		return url.URL{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || (net.ParseIP(host) == nil && !domainPattern.MatchString(strings.TrimSuffix(host, "."))) {
		return url.URL{}, false
	}
	return url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, true
}

func publicOrigin(r *http.Request, configured string) url.URL {
	if origin, ok := parsePublicOrigin(configured); ok {
		return origin
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	origin, _ := parsePublicOrigin(scheme + "://" + r.Host)
	return origin
}

// A custom domain serves the status page at its root and its other public
// pages under the same paths as the instance.
func projectURL(origin url.URL, project store.Project, suffix string, query url.Values) url.URL {
	if project.Domain != "" {
		origin.Host = project.Domain
	}
	origin.Path = "/s/" + project.Slug + suffix
	if project.Domain != "" && suffix == "" {
		origin.Path = "/"
	}
	origin.RawQuery = query.Encode()
	return origin
}

func (c *chrome) setPublicMetadata(canonical url.URL, siteName string, noIndex bool) {
	c.NoIndex = noIndex
	if canonical.Host == "" || canonical.Scheme == "" {
		return
	}
	c.CanonicalURL = canonical.String()
	c.SiteName = siteName
	canonical.Path = socialImagePath
	canonical.RawQuery = ""
	c.SocialImage = canonical.String()
}

// crawlOrigin resolves the sitemap host and whether it has indexable entries.
func (s *Server) crawlOrigin(r *http.Request) (url.URL, store.Project, bool, error) {
	ctx := r.Context()
	instance, err := s.store.Instance(ctx)
	if err != nil {
		return url.URL{}, store.Project{}, false, err
	}
	project, err := s.store.ProjectByDomain(ctx, requestHost(r))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return url.URL{}, project, false, err
	}
	origin := publicOrigin(r, instance.PublicURL)
	if project.Domain != "" {
		origin.Host = project.Domain
		return origin, project, !project.NoIndex, nil
	}
	if !instance.PublicList {
		return origin, project, false, nil
	}
	exists, err := s.store.HasUsers(ctx)
	return origin, project, exists, err
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	origin, _, indexable, err := s.crawlOrigin(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\nDisallow:\n")
	if indexable && origin.Host != "" {
		origin.Path = "/sitemap.xml"
		fmt.Fprintln(w, "Sitemap:", origin.String())
	}
}

type sitemapURL struct {
	Location string `xml:"loc"`
}

type sitemap struct {
	XMLName xml.Name     `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
	URLs    []sitemapURL `xml:"url"`
}

func (d *sitemap) addProject(origin url.URL, project store.Project) {
	for _, suffix := range []string{"", "/history"} {
		location := projectURL(origin, project, suffix, nil)
		d.URLs = append(d.URLs, sitemapURL{location.String()})
	}
}

func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	origin, project, indexable, err := s.crawlOrigin(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if origin.Host == "" {
		http.Error(w, "invalid public host", http.StatusBadRequest)
		return
	}
	if !indexable {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(r.Host, origin.Host) {
		origin.Path = "/sitemap.xml"
		http.Redirect(w, r, origin.String(), http.StatusTemporaryRedirect)
		return
	}

	var doc sitemap
	if project.Domain != "" {
		doc.addProject(origin, project)
	} else {
		root := origin
		root.Path = "/"
		doc.URLs = append(doc.URLs, sitemapURL{root.String()})
		projects, err := s.store.Projects(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, p := range projects {
			if !p.NoIndex && p.Domain == "" {
				doc.addProject(origin, p)
			}
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, xml.Header)
	if err := xml.NewEncoder(w).Encode(doc); err != nil {
		s.log.Error("write sitemap", "error", err)
	}
}
