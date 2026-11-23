package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"uppies/internal/alert"
	"uppies/internal/store"
)

type settingsPage struct {
	Chrome    chrome
	Project   store.Project
	Zones     []string
	Themes    []string
	Backfills []string
	Swatches  []int
	// CustomHue is true when the accent is not one of the swatches.
	CustomHue bool
	Surfaces  []string
	Corners   []string
	// GraphFills are the graph fill choices, the first one meaning "as the instance says".
	GraphFills []string
	Fonts      []string
	// Components are the monitors the status page shows, for their badges.
	Components []store.PlacedMonitor
	// Images maps each image kind the project has to its version.
	Images   map[string]int64
	Problems map[string]string
}

func (s *Server) settingsPage(r *http.Request) (settingsPage, error) {
	page := settingsPage{
		Themes:     themes,
		Backfills:  backfills,
		Swatches:   accentSwatches,
		Surfaces:   surfaces,
		Corners:    cornerStyles,
		GraphFills: scopedGraphFills,
		Fonts:      fonts,
	}
	id, err := pathID(r)
	if err != nil {
		return page, err
	}
	page.Project, err = s.store.ProjectByID(r.Context(), id)
	if err != nil {
		return page, err
	}
	page.Chrome = s.adminChrome(r, page.Project.Name+" settings")
	page.Zones = zoneChoices(page.Project, page.Project.Timezone)
	page.CustomHue = !slices.Contains(accentSwatches, page.Project.AccentHue)
	page.Images, err = s.store.ProjectImageVersions(r.Context(), id)
	if err != nil {
		return page, err
	}
	page.Components, err = s.store.MonitorsByProject(r.Context(), id)
	return page, err
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "settings.html", page)
}

// accentSwatches are the hues offered as one-click accent colours.
var accentSwatches = []int{25, 55, 85, 150, 200, 245, 290, 330}

var (
	surfaces     = []string{"neutral", "paper"}
	backfills    = []string{"none", "up", "degraded", "down"}
	cornerStyles = []string{"sharp", "soft", "round"}
	fonts        = []string{"system", "rounded", "serif", "mono"}
)

// choice reads a field that must be one of the allowed values. An absent
// field keeps the current value.
func choice(form url.Values, field string, allowed []string, current string, problems map[string]string) string {
	value := form.Get(field)
	if value == "" {
		return current
	}
	if !slices.Contains(allowed, value) {
		problems[field] = "choose one of the options."
		return current
	}
	return value
}

var (
	domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	themes        = []string{"auto", "light", "dark"}
)

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	form := r.PostForm
	problems := make(map[string]string)
	p := page.Project
	p.Name = strings.TrimSpace(form.Get("name"))
	p.Tagline = strings.TrimSpace(form.Get("tagline"))
	p.HomepageURL = strings.TrimSpace(form.Get("homepage_url"))
	p.Theme = form.Get("theme")
	p.ShowFace = form.Get("show_face") == "on"
	p.HideQuietDays = form.Get("hide_quiet_days") == "on"
	p.CustomCSS = form.Get("custom_css")
	p.FooterText = strings.TrimSpace(form.Get("footer_text"))
	p.NavLinks = strings.TrimSpace(strings.ReplaceAll(form.Get("nav_links"), "\r", ""))
	if _, problem := parseNavLinks(p.NavLinks); problem != "" {
		problems["nav_links"] = problem
	} else if len(p.NavLinks) > 2000 {
		problems["nav_links"] = "the links are too long."
	}
	p.ShowPoweredBy = form.Get("show_powered_by") == "on"
	p.NoIndex = form.Get("noindex") == "on"
	p.Timezone = strings.TrimSpace(form.Get("timezone"))
	p.Domain = strings.ToLower(strings.TrimSpace(form.Get("domain")))
	p.AccentHue = formInt(form, "accent_hue", 0, 360, problems)
	if custom := strings.TrimSpace(form.Get("accent_hue_custom")); custom != "" {
		// A hue typed under "advanced" wins over the swatch.
		p.AccentHue = formInt(form, "accent_hue_custom", 0, 360, problems)
	}
	// A look field that is not sent keeps its value, so older forms still save.
	p.Surface = choice(form, "surface", surfaces, p.Surface, problems)
	p.Corners = choice(form, "corners", cornerStyles, p.Corners, problems)
	p.GraphFill = scopedGraphFill(form, p.GraphFill, problems)
	p.Font = choice(form, "font", fonts, p.Font, problems)
	p.HistoryDays = formInt(form, "history_days", 7, 365, problems)
	p.Backfill = choice(form, "backfill", backfills, p.Backfill, problems)

	if p.Name == "" || len(p.Name) > 120 {
		problems["name"] = "give the status page a name of 1 to 120 characters."
	}
	if len(p.Tagline) > 200 {
		problems["tagline"] = "keep the tagline to 200 characters."
	}
	if p.HomepageURL != "" {
		if len(p.HomepageURL) > 2048 {
			problems["homepage_url"] = "the url is too long."
		} else if err := alert.ValidateURL(p.HomepageURL); err != nil {
			problems["homepage_url"] = err.Error() + "."
		}
	}
	if !slices.Contains(themes, p.Theme) {
		problems["theme"] = "choose a theme."
	}
	if len(p.CustomCSS) > 20000 {
		problems["custom_css"] = "keep the custom css to 20000 characters."
	}
	if len(p.FooterText) > 500 {
		problems["footer_text"] = "keep the footer text to 500 characters."
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" || len(p.Timezone) > 64 {
		problems["timezone"] = "choose a timezone."
		p.Timezone = page.Project.Timezone
	}
	page.Zones = zoneChoices(page.Project, p.Timezone)
	if p.Domain != "" && (len(p.Domain) > 253 || !domainPattern.MatchString(p.Domain)) {
		problems["domain"] = "enter a hostname without http:// or a path, for example status.example.com."
	}
	page.Project = p
	if len(problems) == 0 {
		err := s.store.UpdateProject(r.Context(), p)
		if errors.Is(err, store.ErrDomainTaken) {
			problems["domain"] = "another status page already uses this domain."
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if len(problems) > 0 {
		page.Problems = problems
		s.render(w, r, http.StatusUnprocessableEntity, "settings.html", page)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d/settings", p.ID), http.StatusSeeOther)
}
