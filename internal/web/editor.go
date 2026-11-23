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

// editorURL is where the editor of a status page lives.
func editorURL(projectID int64) string {
	return fmt.Sprintf("/admin/projects/%d?saved=1#pane-editor", projectID)
}

// handleSavePlacements stores the list of monitors a status page shows. The
// form sends every monitor in its order with its look, and at most one
// change to that list: a monitor to add, to remove or to move by one place.
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
	order := formIDs(form["monitor"])
	if added, err := strconv.ParseInt(form.Get("add"), 10, 64); err == nil {
		order = append(order, added)
	}
	if removed, err := strconv.ParseInt(form.Get("remove"), 10, 64); err == nil {
		order = slices.DeleteFunc(order, func(monitorID int64) bool { return monitorID == removed })
	}
	// "move" names a monitor and a direction, such as "12-up".
	if moved, direction, found := strings.Cut(form.Get("move"), "-"); found {
		movedID, _ := strconv.ParseInt(moved, 10, 64)
		from := slices.Index(order, movedID)
		to := from + 1
		if direction == "up" {
			to = from - 1
		}
		if from >= 0 && to >= 0 && to < len(order) {
			order[from], order[to] = order[to], order[from]
		}
	}

	var placements []store.Placement
	listed := make(map[int64]bool)
	for _, monitorID := range order {
		// A monitor that was deleted meanwhile, or named twice, is left out.
		if !known[monitorID] || listed[monitorID] {
			continue
		}
		listed[monitorID] = true
		p := store.Placement{MonitorID: monitorID, Display: form.Get(fmt.Sprintf("display-%d", monitorID))}
		if !slices.Contains(store.MonitorDisplays, p.Display) {
			p.Display = "bars"
		}
		if fill := form.Get(fmt.Sprintf("fill-%d", monitorID)); slices.Contains(store.GraphFills, fill) {
			p.GraphFill = fill
		}
		placements = append(placements, p)
	}
	if err := s.store.SetPlacements(r.Context(), id, placements); err != nil {
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
		s.render(w, r, http.StatusUnprocessableEntity, "project.html", page)
		return
	}
	if err := s.store.UpdateProject(r.Context(), p); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, editorURL(p.ID), http.StatusSeeOther)
}

// handleMoveProject moves a status page one place up or down in the order
// the sidebar and the public list show them in.
func (s *Server) handleMoveProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	projects, err := s.store.Projects(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	order := make([]int64, len(projects))
	for i, p := range projects {
		order[i] = p.ID
	}
	from := slices.Index(order, id)
	if from < 0 {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	to := from + 1
	if r.PostForm.Get("direction") == "up" {
		to = from - 1
	}
	if to >= 0 && to < len(order) {
		order[from], order[to] = order[to], order[from]
		if err := s.store.SetProjectOrder(r.Context(), order); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", id), http.StatusSeeOther)
}
