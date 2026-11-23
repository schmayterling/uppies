package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"uppies/internal/store"
)

// editorRow is one monitor in a status page's editor: the monitor with its
// state and recent uptime, and how this page draws it.
type editorRow struct {
	overviewRow
	Category  string
	Display   string
	GraphFill string
}

// editorChoices is what the editor of a status page offers to choose from.
type editorChoices struct {
	Displays   []string
	GraphFills []string
	Themes     []string
	Swatches   []int
	Surfaces   []string
	Corners    []string
	Fonts      []string
}

var editorOptions = editorChoices{
	Displays: store.MonitorDisplays, GraphFills: scopedGraphFills, Themes: themes,
	Swatches: accentSwatches, Surfaces: surfaces, Corners: cornerStyles, Fonts: fonts,
}

// editorGroup is one of a status page's categories with its monitors. The
// group without a name holds the monitors that are in no category.
type editorGroup struct {
	Category string
	Rows     []editorRow
}

// editorGroups sorts a page's monitors into its categories: the monitors
// without a category first, then each category in the page's order, empty
// ones included, so that they can be filled.
func editorGroups(categories []string, rows []editorRow) []editorGroup {
	groups := []editorGroup{{}}
	index := map[string]int{"": 0}
	for _, name := range categories {
		index[name] = len(groups)
		groups = append(groups, editorGroup{Category: name})
	}
	for _, row := range rows {
		at, listed := index[row.Category]
		if !listed {
			at = len(groups)
			index[row.Category] = at
			groups = append(groups, editorGroup{Category: row.Category})
		}
		groups[at].Rows = append(groups[at].Rows, row)
	}
	return groups
}

// editorURL is where the editor of a status page lives.
func editorURL(projectID int64) string {
	return fmt.Sprintf("/admin/projects/%d/editor", projectID)
}

func (s *Server) handleEditor(w http.ResponseWriter, r *http.Request) {
	page, err := s.projectPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "editor.html", page)
}

// layoutGroup is a category of a status page while its layout is being put
// together from a form: its name and the monitors in it, in order.
type layoutGroup struct {
	name string
	ids  []int64
}

// parseLayout reads the arrangement the editor sends: one item per category
// and per monitor in the order they stand in, "c:" and a category's name,
// then "m:" and the id of each monitor in it. The group without a name is
// always first. A category named twice is one category, and a monitor named
// twice counts where it is named first.
func parseLayout(items []string) []layoutGroup {
	groups := []layoutGroup{{}}
	current := 0
	listed := make(map[int64]bool)
	for _, item := range items {
		kind, value, _ := strings.Cut(item, ":")
		switch kind {
		case "c":
			name := strings.TrimSpace(value)
			if len(name) > 80 {
				continue
			}
			current = slices.IndexFunc(groups, func(g layoutGroup) bool { return g.name == name })
			if current < 0 {
				current = len(groups)
				groups = append(groups, layoutGroup{name: name})
			}
		case "m":
			if id, err := strconv.ParseInt(value, 10, 64); err == nil && !listed[id] {
				listed[id] = true
				groups[current].ids = append(groups[current].ids, id)
			}
		}
	}
	return groups
}

// moveMonitor moves a monitor one place up or down. At the edge of its
// category it crosses into the neighbouring one.
func moveMonitor(groups []layoutGroup, id int64, up bool) {
	for g := range groups {
		at := slices.Index(groups[g].ids, id)
		if at < 0 {
			continue
		}
		ids := groups[g].ids
		switch {
		case up && at > 0:
			ids[at-1], ids[at] = ids[at], ids[at-1]
		case !up && at < len(ids)-1:
			ids[at+1], ids[at] = ids[at], ids[at+1]
		case up && g > 0:
			groups[g].ids = ids[1:]
			groups[g-1].ids = append(groups[g-1].ids, id)
		case !up && g < len(groups)-1:
			groups[g].ids = ids[:at]
			groups[g+1].ids = append([]int64{id}, groups[g+1].ids...)
		}
		return
	}
}

// handleSavePlacements stores what a status page shows: its categories and
// its monitors, each with its look. The form sends the whole arrangement and
// at most one change to it: a monitor to add, to take off or to move by one
// place, or a category to add, to remove or to move by one place.
func (s *Server) handleSavePlacements(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.store.ProjectByID(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	monitors, err := s.store.Monitors(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	known := make(map[int64]bool, len(monitors))
	for _, m := range monitors {
		known[m.ID] = true
	}
	form := r.PostForm
	groups := parseLayout(form["item"])
	formID := func(field string) (int64, bool) {
		value, err := strconv.ParseInt(form.Get(field), 10, 64)
		return value, err == nil
	}
	if added, ok := formID("add"); ok {
		// parseLayout leaves out a monitor that is on the page already.
		groups = parseLayout(append(slices.Clone(form["item"]), "c:", fmt.Sprintf("m:%d", added)))
	}
	if removed, ok := formID("remove"); ok {
		for g := range groups {
			groups[g].ids = slices.DeleteFunc(groups[g].ids, func(monitorID int64) bool { return monitorID == removed })
		}
	}
	if moved, ok := formID("up"); ok {
		moveMonitor(groups, moved, true)
	}
	if moved, ok := formID("down"); ok {
		moveMonitor(groups, moved, false)
	}
	if name := strings.TrimSpace(form.Get("new_category")); name != "" && len(name) <= 80 {
		if !slices.ContainsFunc(groups, func(g layoutGroup) bool { return g.name == name }) {
			groups = append(groups, layoutGroup{name: name})
		}
	}
	// The monitors of a removed category go back to having none.
	if name := form.Get("remove_category"); name != "" {
		if at := slices.IndexFunc(groups, func(g layoutGroup) bool { return g.name == name }); at > 0 {
			groups[0].ids = append(groups[0].ids, groups[at].ids...)
			groups = slices.Delete(groups, at, at+1)
		}
	}
	// A category moves among the named ones. The group without a name stays first.
	for field, step := range map[string]int{"category_up": -1, "category_down": 1} {
		name := form.Get(field)
		at := slices.IndexFunc(groups, func(g layoutGroup) bool { return g.name == name })
		if name != "" && at > 0 && at+step > 0 && at+step < len(groups) {
			groups[at], groups[at+step] = groups[at+step], groups[at]
		}
	}

	var categories []string
	var placements []store.Placement
	for _, group := range groups {
		if group.name != "" {
			categories = append(categories, group.name)
		}
		for _, monitorID := range group.ids {
			// A monitor that was deleted meanwhile is left out.
			if !known[monitorID] {
				continue
			}
			p := store.Placement{MonitorID: monitorID, Category: group.name, Display: form.Get(fmt.Sprintf("display-%d", monitorID))}
			if !slices.Contains(store.MonitorDisplays, p.Display) {
				p.Display = "bars"
			}
			if fill := form.Get(fmt.Sprintf("fill-%d", monitorID)); slices.Contains(store.GraphFills, fill) {
				p.GraphFill = fill
			}
			placements = append(placements, p)
		}
	}
	if err := s.store.SetPageLayout(r.Context(), id, categories, placements); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, editorURL(id), http.StatusSeeOther)
}

// handleSaveLook stores the look of a status page from its editor: colours,
// shapes, typeface and custom css. The page's other settings stay as they are.
func (s *Server) handleSaveLook(w http.ResponseWriter, r *http.Request) {
	page, err := s.projectPage(r)
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
	p.Theme = choice(form, "theme", themes, p.Theme, problems)
	p.AccentHue = formInt(form, "accent_hue", 0, 360, problems)
	if custom := strings.TrimSpace(form.Get("accent_hue_custom")); custom != "" {
		// A hue that is typed wins over the swatch.
		p.AccentHue = formInt(form, "accent_hue_custom", 0, 360, problems)
	}
	p.Surface = choice(form, "surface", surfaces, p.Surface, problems)
	p.Corners = choice(form, "corners", cornerStyles, p.Corners, problems)
	p.Font = choice(form, "font", fonts, p.Font, problems)
	p.GraphFill = scopedGraphFill(form, p.GraphFill, problems)
	p.ShowFace = form.Get("show_face") == "on"
	p.HideQuietDays = form.Get("hide_quiet_days") == "on"
	p.CustomCSS = form.Get("custom_css")
	if len(p.CustomCSS) > 20000 {
		problems["custom_css"] = "keep the custom css to 20000 characters."
	}
	page.Project = p
	if len(problems) > 0 {
		page.Problems = problems
		s.render(w, r, http.StatusUnprocessableEntity, "editor.html", page)
		return
	}
	if err := s.store.UpdateProject(r.Context(), p); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, editorURL(p.ID), http.StatusSeeOther)
}
