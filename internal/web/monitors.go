package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uppies/internal/check"
	"uppies/internal/store"
)

// monitorFormPage is the data of monitor_form.html: the form of a new monitor,
// or an existing monitor's overview and settings.
type monitorFormPage struct {
	Chrome  chrome
	Monitor store.Monitor
	Kinds   []string
	// Categories are the categories a monitor can be put in.
	Categories []string
	// Channels are all alert channels, and ChannelIDs the ones this monitor alerts.
	Channels   []store.AlertChannel
	ChannelIDs []int64
	// Pages are the status pages that show the monitor.
	Pages []store.Project
	// PlaceOn is the status page a new monitor is put on right away, 0 for none.
	PlaceOn  int64
	Editing  bool
	Saved    bool
	Problems map[string]string
	// PushURL is where a push monitor's job reports in, empty for other kinds.
	PushURL string
	// The overview of an existing monitor: its state, its detail view over
	// Lookback days and its latest checks, with times in Zone.
	State    string
	Word     string
	Detail   monitorDetail
	Checks   []store.Check
	Zone     *time.Location
	Lookback int
	// StateIcons names the icon of the notice for each state.
	StateIcons map[string]string
}

// stateIcons are the icons of the state notice on the monitor overview.
var stateIcons = map[string]string{stateOK: "circle-check", stateDown: "circle-x", stateWarn: "info", stateNone: "info"}

// latestChecks is how many checks the monitor overview lists.
const latestChecks = 20

// defaultLookbackDays is how far back a monitor's overview looks when no status page shows it.
const defaultLookbackDays = 90

// defaultMonitor holds the settings a new monitor starts with: a check every
// minute, down after the third failed check in a row, up after two good ones.
func defaultMonitor() store.Monitor {
	return store.Monitor{
		Kind: check.KindHTTP, IntervalS: 60, TimeoutMs: 10_000,
		Retries: 2, RetryIntervalS: 20, RecoveryChecks: 2,
	}
}

// monitorForm fills in what the monitor form offers: kinds, categories and alert channels.
func (s *Server) monitorForm(r *http.Request, title string, m store.Monitor) (monitorFormPage, error) {
	page := monitorFormPage{Chrome: s.adminChrome(r, title), Monitor: m, Kinds: check.Kinds, StateIcons: stateIcons}
	var err error
	if page.Categories, err = s.store.Categories(r.Context()); err != nil {
		return page, err
	}
	page.Channels, err = s.store.AlertChannels(r.Context())
	return page, err
}

// monitorOverview fills in the overview of an existing monitor. It is drawn
// the way the first status page that shows the monitor draws it, and with the
// install's defaults when none does.
func (s *Server) monitorOverview(r *http.Request, page *monitorFormPage) error {
	ctx := r.Context()
	page.Editing = true
	page.PushURL = pushURL(r, page.Monitor)
	page.State, page.Word = componentState(page.Monitor)
	pages, err := s.pagesByMonitor(ctx)
	if err != nil {
		return err
	}
	page.Pages = pages[page.Monitor.ID]
	instance, err := s.store.Instance(ctx)
	if err != nil {
		return err
	}
	look := detailLook{Days: defaultLookbackDays, Backfill: "none", Fill: instance.GraphFill}
	look.Zone, _ = pageZone(store.Project{Timezone: systemZoneName()}, "")
	if len(page.Pages) > 0 {
		first := page.Pages[0]
		zone, _ := pageZone(first, "")
		if look, err = s.pageLook(ctx, first, "", zone); err != nil {
			return err
		}
	}
	page.Zone, page.Lookback = look.Zone, look.Days
	if page.Detail, err = s.monitorDetail(ctx, page.Monitor, look); err != nil {
		return err
	}
	page.Checks, err = s.store.RecentChecks(ctx, page.Monitor.ID, latestChecks)
	return err
}

// formIDs reads the whole numbers of a repeated form field. Anything else is left out.
func formIDs(values []string) []int64 {
	var ids []int64
	for _, value := range values {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Server) handleNewMonitorForm(w http.ResponseWriter, r *http.Request) {
	page, err := s.monitorForm(r, "new monitor", defaultMonitor())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A new monitor alerts every channel until told otherwise.
	for _, c := range page.Channels {
		page.ChannelIDs = append(page.ChannelIDs, c.ID)
	}
	// A missing or malformed page means the monitor starts on no status page.
	page.PlaceOn, _ = strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	s.render(w, r, http.StatusOK, "monitor_form.html", page)
}

// handleNewMonitorOnPage keeps the old address of the form working.
func (s *Server) handleNewMonitorOnPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/monitors/new?page="+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) handleCreateMonitor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	m, problems := parseMonitorForm(r.PostForm, defaultMonitor())
	channelIDs := formIDs(r.PostForm["channel"])
	placeOn, _ := strconv.ParseInt(r.PostForm.Get("page"), 10, 64)
	if len(problems) > 0 {
		page, err := s.monitorForm(r, "new monitor", m)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.Problems, page.ChannelIDs, page.PlaceOn = problems, channelIDs, placeOn
		s.render(w, r, http.StatusUnprocessableEntity, "monitor_form.html", page)
		return
	}
	monitorID, err := s.store.CreateMonitor(ctx, m, s.now().UnixMilli())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.saveMonitorExtras(r, m, monitorID, channelIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	if placeOn > 0 {
		if _, err := s.store.ProjectByID(ctx, placeOn); err != nil {
			s.fail(w, r, err)
			return
		}
		if err := s.store.PlaceMonitor(ctx, placeOn, monitorID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.monitorsChanged()
	switch {
	case m.Kind == check.KindPush:
		// Go to the monitor's page, which shows the address the job must call.
		http.Redirect(w, r, fmt.Sprintf("/admin/monitors/%d#pane-settings", monitorID), http.StatusSeeOther)
	case placeOn > 0:
		http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d", placeOn), http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/admin/monitors", http.StatusSeeOther)
	}
}

// saveMonitorExtras stores what belongs to a monitor outside its own row:
// the channels it alerts, and its category in the list of categories.
func (s *Server) saveMonitorExtras(r *http.Request, m store.Monitor, monitorID int64, channelIDs []int64) error {
	if err := s.store.SetMonitorChannels(r.Context(), monitorID, channelIDs); err != nil {
		return err
	}
	if m.Category == "" {
		return nil
	}
	return s.store.CreateCategory(r.Context(), m.Category)
}

// pathMonitor loads the monitor named by the path.
func (s *Server) pathMonitor(r *http.Request) (store.Monitor, error) {
	id, err := pathID(r)
	if err != nil {
		return store.Monitor{}, err
	}
	return s.store.Monitor(r.Context(), id)
}

func (s *Server) handleEditMonitorForm(w http.ResponseWriter, r *http.Request) {
	m, err := s.pathMonitor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.monitorForm(r, m.Name, m)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	alerted, err := s.store.MonitorChannels(r.Context(), m.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, c := range alerted {
		page.ChannelIDs = append(page.ChannelIDs, c.ID)
	}
	page.Saved = r.URL.Query().Get("saved") == "1"
	if err := s.monitorOverview(r, &page); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "monitor_form.html", page)
}

func (s *Server) handleUpdateMonitor(w http.ResponseWriter, r *http.Request) {
	current, err := s.pathMonitor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	m, problems := parseMonitorForm(r.PostForm, current)
	channelIDs := formIDs(r.PostForm["channel"])
	if len(problems) > 0 {
		page, err := s.monitorForm(r, current.Name, m)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.Problems, page.ChannelIDs = problems, channelIDs
		if err := s.monitorOverview(r, &page); err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, http.StatusUnprocessableEntity, "monitor_form.html", page)
		return
	}
	if err := s.store.UpdateMonitor(r.Context(), m); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.saveMonitorExtras(r, m, m.ID, channelIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, fmt.Sprintf("/admin/monitors/%d?saved=1#pane-settings", m.ID), http.StatusSeeOther)
}

func (s *Server) handleDeleteMonitor(w http.ResponseWriter, r *http.Request) {
	m, err := s.pathMonitor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteMonitor(r.Context(), m.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.monitorsChanged()
	http.Redirect(w, r, "/admin/monitors", http.StatusSeeOther)
}

// monitorsPage is the data of monitors.html: every monitor of the install, by category.
type monitorsPage struct {
	Chrome chrome
	Groups []monitorGroup
	Total  int
	// NewCategory holds the add-category form's value after a failed submit.
	NewCategory string
	Problems    map[string]string
}

// monitorGroup is one category with its monitors. The group without a name
// holds the monitors that have no category.
type monitorGroup struct {
	Category string
	Rows     []monitorListRow
}

type monitorListRow struct {
	monitorRow
	// Pages are the status pages that show the monitor.
	Pages []store.Project
}

func (s *Server) monitorsPage(r *http.Request) (monitorsPage, error) {
	ctx := r.Context()
	page := monitorsPage{Chrome: s.adminChrome(r, "monitors")}
	monitors, err := s.store.Monitors(ctx)
	if err != nil {
		return page, err
	}
	categories, err := s.store.Categories(ctx)
	if err != nil {
		return page, err
	}
	pages, err := s.pagesByMonitor(ctx)
	if err != nil {
		return page, err
	}
	page.Total = len(monitors)
	// Monitors without a category come first, then each category in its
	// order. A category shows even when it is empty, so that it can be filled.
	index := map[string]int{"": 0}
	page.Groups = []monitorGroup{{}}
	for _, name := range categories {
		index[name] = len(page.Groups)
		page.Groups = append(page.Groups, monitorGroup{Category: name})
	}
	for _, row := range monitorRows(monitors) {
		at, listed := index[row.Category]
		if !listed {
			// A category that is on a monitor and not in the list still gets its group.
			at = len(page.Groups)
			index[row.Category] = at
			page.Groups = append(page.Groups, monitorGroup{Category: row.Category})
		}
		page.Groups[at].Rows = append(page.Groups[at].Rows, monitorListRow{monitorRow: row, Pages: pages[row.ID]})
	}
	if len(page.Groups[0].Rows) == 0 {
		page.Groups = page.Groups[1:]
	}
	return page, nil
}

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	page, err := s.monitorsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "monitors.html", page)
}

func (s *Server) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if name == "" || len(name) > 80 {
		page, err := s.monitorsPage(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.NewCategory = name
		page.Problems = map[string]string{"name": "give the category a name of 1 to 80 characters."}
		s.render(w, r, http.StatusUnprocessableEntity, "monitors.html", page)
		return
	}
	if err := s.store.CreateCategory(r.Context(), name); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/monitors", http.StatusSeeOther)
}

func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteCategory(r.Context(), r.PostForm.Get("name")); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/monitors", http.StatusSeeOther)
}
