package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/alert"
	"uppies/internal/store"
)

type apiPage struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Tagline       string `json:"tagline"`
	HomepageURL   string `json:"homepage_url"`
	AccentHue     int    `json:"accent_hue"`
	Theme         string `json:"theme"`
	ShowFace      bool   `json:"show_face"`
	HistoryDays   int    `json:"history_days"`
	Backfill      string `json:"backfill"`
	NavLinks      string `json:"nav_links"`
	NavbarBrand   string `json:"navbar_brand"`
	CustomCSS     string `json:"custom_css"`
	FooterText    string `json:"footer_text"`
	ShowPoweredBy bool   `json:"show_powered_by"`
	NoIndex       bool   `json:"noindex"`
	Timezone      string `json:"timezone"`
	Surface       string `json:"surface"`
	Corners       string `json:"corners"`
	Font          string `json:"font"`
	Domain        string `json:"domain"`
	HideQuietDays bool   `json:"hide_quiet_days"`
	GraphFill     string `json:"graph_fill"`
}

func (p apiPage) cursor() string { return p.ID }

func pageJSON(p store.Project) apiPage {
	return apiPage{
		ID: strconv.FormatInt(p.ID, 10), Slug: p.Slug, Name: p.Name,
		Tagline: p.Tagline, HomepageURL: p.HomepageURL, AccentHue: p.AccentHue, Theme: p.Theme, ShowFace: p.ShowFace,
		HistoryDays: p.HistoryDays, Backfill: p.Backfill, NavLinks: p.NavLinks, NavbarBrand: p.NavbarBrand, CustomCSS: p.CustomCSS,
		FooterText: p.FooterText, ShowPoweredBy: p.ShowPoweredBy, NoIndex: p.NoIndex, Timezone: p.Timezone,
		Surface: p.Surface, Corners: p.Corners, Font: p.Font, Domain: p.Domain, HideQuietDays: p.HideQuietDays, GraphFill: p.GraphFill,
	}
}

func (s *Server) apiListPages(w http.ResponseWriter, r *http.Request) {
	apiList(s, w, r, s.store.ProjectsAfter, pageJSON)
}

func (s *Server) apiReadPage(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	page, err := s.store.ProjectByID(r.Context(), id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiJSON(w, r, http.StatusOK, pageJSON(page))
}

func (s *Server) apiCreatePage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if err := apiText("name", input.Name, 1, 120); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if !slugPattern.MatchString(input.Slug) {
		s.apiFailure(w, r, invalidAPIField("slug", "Use 1 to 64 lowercase letters, digits and internal hyphens."))
		return
	}
	id, err := s.store.CreateProject(r.Context(), input.Slug, input.Name, s.now().UnixMilli())
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiCreated(w, r, "/api/v1/status-pages", id)
}

type pagePatch struct {
	Name          *string `json:"name"`
	Tagline       *string `json:"tagline"`
	HomepageURL   *string `json:"homepage_url"`
	AccentHue     *int    `json:"accent_hue"`
	Theme         *string `json:"theme"`
	ShowFace      *bool   `json:"show_face"`
	HistoryDays   *int    `json:"history_days"`
	Backfill      *string `json:"backfill"`
	NavLinks      *string `json:"nav_links"`
	NavbarBrand   *string `json:"navbar_brand"`
	CustomCSS     *string `json:"custom_css"`
	FooterText    *string `json:"footer_text"`
	ShowPoweredBy *bool   `json:"show_powered_by"`
	NoIndex       *bool   `json:"noindex"`
	Timezone      *string `json:"timezone"`
	Surface       *string `json:"surface"`
	Corners       *string `json:"corners"`
	Font          *string `json:"font"`
	Domain        *string `json:"domain"`
	HideQuietDays *bool   `json:"hide_quiet_days"`
	GraphFill     *string `json:"graph_fill"`
}

func (input pagePatch) apply(p *store.Project) error {
	for _, field := range []struct {
		key        string
		value      *string
		dest       *string
		minimum    int
		maximum    int
		whitespace bool
	}{
		{"name", input.Name, &p.Name, 1, 120, false},
		{"tagline", input.Tagline, &p.Tagline, 0, 200, false},
		{"homepage_url", input.HomepageURL, &p.HomepageURL, 0, 2048, false},
		{"nav_links", input.NavLinks, &p.NavLinks, 0, 2000, false},
		{"custom_css", input.CustomCSS, &p.CustomCSS, 0, 20_000, true},
		{"footer_text", input.FooterText, &p.FooterText, 0, 500, false},
		{"timezone", input.Timezone, &p.Timezone, 1, 64, false},
		{"domain", input.Domain, &p.Domain, 0, 253, false},
	} {
		if field.value == nil {
			continue
		}
		value := *field.value
		if !field.whitespace {
			value = strings.TrimSpace(value)
		}
		if err := apiText(field.key, value, field.minimum, field.maximum); err != nil {
			return err
		}
		*field.dest = value
	}
	for _, field := range []struct {
		key     string
		value   *string
		dest    *string
		options []string
	}{
		{"theme", input.Theme, &p.Theme, themes},
		{"backfill", input.Backfill, &p.Backfill, backfills},
		{"navbar_brand", input.NavbarBrand, &p.NavbarBrand, navbarBrands},
		{"surface", input.Surface, &p.Surface, surfaces},
		{"corners", input.Corners, &p.Corners, cornerStyles},
		{"font", input.Font, &p.Font, fonts},
		{"graph_fill", input.GraphFill, &p.GraphFill, append([]string{""}, store.GraphFills...)},
	} {
		if field.value == nil {
			continue
		}
		if !slices.Contains(field.options, *field.value) {
			return invalidAPIField(field.key, "Use a supported value.")
		}
		*field.dest = *field.value
	}
	if input.HomepageURL != nil && p.HomepageURL != "" && alert.ValidateURL(p.HomepageURL) != nil {
		return invalidAPIField("homepage_url", "Use an HTTP or HTTPS URL with a host.")
	}
	if input.NavLinks != nil {
		if _, problem := parseNavLinks(p.NavLinks); problem != "" {
			return invalidAPIField("nav_links", problem)
		}
	}
	if input.Timezone != nil {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return invalidAPIField("timezone", "Use a recognized timezone.")
		}
	}
	if input.Domain != nil {
		p.Domain = strings.ToLower(p.Domain)
		if p.Domain != "" && !domainPattern.MatchString(p.Domain) {
			return invalidAPIField("domain", "Use a hostname without a scheme or path.")
		}
	}
	if input.AccentHue != nil {
		if *input.AccentHue < 0 || *input.AccentHue > 360 {
			return invalidAPIField("accent_hue", "Use an integer from 0 to 360.")
		}
		p.AccentHue = *input.AccentHue
	}
	if input.HistoryDays != nil {
		if *input.HistoryDays < 7 || *input.HistoryDays > 365 {
			return invalidAPIField("history_days", "Use an integer from 7 to 365.")
		}
		p.HistoryDays = *input.HistoryDays
	}
	if input.ShowFace != nil {
		p.ShowFace = *input.ShowFace
	}
	if input.ShowPoweredBy != nil {
		p.ShowPoweredBy = *input.ShowPoweredBy
	}
	if input.NoIndex != nil {
		p.NoIndex = *input.NoIndex
	}
	if input.HideQuietDays != nil {
		p.HideQuietDays = *input.HideQuietDays
	}
	return nil
}

func (s *Server) apiUpdatePage(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input pagePatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	if err := s.store.EditProject(r.Context(), id, input.apply); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiDeletePage(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err == nil {
		err = s.store.DeleteProject(r.Context(), id)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	w.WriteHeader(http.StatusNoContent)
}

type apiPlacement struct {
	MonitorID         string `json:"monitor_id"`
	Category          string `json:"category"`
	Display           string `json:"display"`
	GraphFill         string `json:"graph_fill"`
	ExcludeFromStatus bool   `json:"exclude_from_status"`
}

type apiLayout struct {
	Categories []string       `json:"categories"`
	Monitors   []apiPlacement `json:"monitors"`
}

func (s *Server) apiReadLayout(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	categories, placements, err := s.store.PageLayout(r.Context(), id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	layout := apiLayout{Categories: categories, Monitors: make([]apiPlacement, len(placements))}
	for i, p := range placements {
		layout.Monitors[i] = apiPlacement{
			MonitorID: strconv.FormatInt(p.MonitorID, 10), Category: p.Category, Display: p.Display,
			GraphFill: p.GraphFill, ExcludeFromStatus: p.ExcludeFromStatus,
		}
	}
	s.apiJSON(w, r, http.StatusOK, layout)
}

func (layout apiLayout) placements() ([]store.Placement, error) {
	if layout.Categories == nil || layout.Monitors == nil {
		return nil, invalidAPIField("layout", "Supply both categories and monitors arrays. Use empty arrays to clear them.")
	}
	knownCategories := map[string]bool{"": true}
	for i, name := range layout.Categories {
		name = strings.TrimSpace(name)
		if err := apiText("categories", name, 1, 80); err != nil {
			return nil, err
		}
		if knownCategories[name] {
			return nil, invalidAPIField("categories", "Each category must appear only once.")
		}
		knownCategories[name] = true
		layout.Categories[i] = name
	}
	placements := make([]store.Placement, 0, len(layout.Monitors))
	knownMonitors := make(map[int64]bool)
	for i, row := range layout.Monitors {
		field := fmt.Sprintf("monitors[%d]", i)
		monitorID, ok := apiID(row.MonitorID)
		if !ok || knownMonitors[monitorID] {
			return nil, invalidAPIField(field+".monitor_id", "Use a valid monitor ID only once.")
		}
		if !knownCategories[row.Category] {
			return nil, invalidAPIField(field+".category", "Use a category from the categories array or an empty string.")
		}
		if row.Display != "" && !slices.Contains(store.MonitorDisplays, row.Display) {
			return nil, invalidAPIField(field+".display", "Use a supported display.")
		}
		if row.GraphFill != "" && !slices.Contains(store.GraphFills, row.GraphFill) {
			return nil, invalidAPIField(field+".graph_fill", "Use a supported graph fill or an empty string.")
		}
		knownMonitors[monitorID] = true
		placements = append(placements, store.Placement{
			MonitorID: monitorID, Category: row.Category, Display: row.Display, GraphFill: row.GraphFill,
			ExcludeFromStatus: row.ExcludeFromStatus,
		})
	}
	return placements, nil
}

func (s *Server) apiWriteLayout(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var layout apiLayout
	if !s.decodeAPIJSON(w, r, &layout) {
		return
	}
	placements, err := layout.placements()
	if err == nil {
		err = s.store.SetPageLayout(r.Context(), id, layout.Categories, placements)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
