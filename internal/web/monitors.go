package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
	// Badge is the 88x31 badge being put together on the badges tab.
	Badge badgeBuilder
}

// badgeBuilder holds the choices of the badge form and the address they give.
type badgeBuilder struct {
	Kinds    []string
	Swatches []int
	// The choices are the status page, metric, theme, accent, label and window.
	Slug, Show, Theme, Color, Label, Days string
	// Path is the badge's address with its options, and URL the same with this server's origin.
	Path, URL string
}

// newBadgeBuilder reads the badge form's choices from the address and works
// out the badge's own address. Without a status page there is no badge.
func newBadgeBuilder(r *http.Request, monitorID int64, pages []store.Project) badgeBuilder {
	query := r.URL.Query()
	b := badgeBuilder{
		Kinds: badgeKinds, Swatches: accentSwatches,
		Show: query.Get("b_show"), Theme: query.Get("b_theme"), Color: query.Get("b_color"),
		Label: badgeWord(query.Get("b_label"), 4), Days: query.Get("b_days"),
	}
	if !slices.Contains(badgeKinds, b.Show) {
		b.Show = "status"
	}
	if len(pages) == 0 {
		return b
	}
	b.Slug = pages[0].Slug
	for _, page := range pages {
		if page.Slug == query.Get("b_page") {
			b.Slug = page.Slug
		}
	}
	options := url.Values{"show": {b.Show}}
	if b.Theme == "light" {
		options.Set("theme", "light")
	}
	if _, err := strconv.Atoi(b.Color); err == nil {
		options.Set("color", b.Color)
	}
	if b.Label != "" {
		options.Set("label", b.Label)
	}
	if _, err := strconv.Atoi(b.Days); err == nil && b.Show == "uptime" {
		options.Set("days", b.Days)
	}
	b.Path = fmt.Sprintf("/s/%s/monitors/%d/88x31.svg?%s", b.Slug, monitorID, options.Encode())
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	b.URL = scheme + "://" + r.Host + b.Path
	return b
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
		AutoCreateIncident: store.IncidentCreateDraft, AutoResolveIncident: store.IncidentResolveDrafts,
	}
}

// monitorForm fills in what the monitor form offers: kinds and alert channels.
func (s *Server) monitorForm(r *http.Request, title string, m store.Monitor) (monitorFormPage, error) {
	page := monitorFormPage{Chrome: s.adminChrome(r, title), Monitor: m, Kinds: check.Kinds, StateIcons: stateIcons}
	var err error
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
	page.Badge = newBadgeBuilder(r, page.Monitor.ID, page.Pages)
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
	if err := s.store.SetMonitorChannels(ctx, monitorID, channelIDs); err != nil {
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
	if err := s.store.SetMonitorChannels(r.Context(), m.ID, channelIDs); err != nil {
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

// monitorsPage is the data of monitors.html: every monitor of the install, in the admin's order.
type monitorsPage struct {
	Chrome chrome
	Rows   []monitorListRow
	Kinds  []string
	Tags   []monitorTag
}

type monitorTag struct {
	Name, Key string
}

func monitorTags(text string) []string {
	var tags []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(text, ",") {
		name := strings.Join(strings.Fields(part), " ")
		key := strings.ToLower(name)
		if key != "" && !seen[key] {
			tags = append(tags, name)
			seen[key] = true
		}
	}
	return tags
}

type monitorListRow struct {
	monitorRow
	// Pages are the status pages that show the monitor.
	Pages   []store.Project
	TagList []monitorTag
}

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page := monitorsPage{Chrome: s.adminChrome(r, "monitors"), Kinds: check.Kinds}
	monitors, err := s.store.Monitors(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pages, err := s.pagesByMonitor(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	seen := make(map[string]bool)
	for _, row := range monitorRows(monitors) {
		view := monitorListRow{monitorRow: row, Pages: pages[row.ID]}
		for _, name := range monitorTags(row.Tags) {
			tag := monitorTag{Name: name, Key: url.QueryEscape(strings.ToLower(name))}
			view.TagList = append(view.TagList, tag)
			if !seen[tag.Key] {
				page.Tags = append(page.Tags, tag)
				seen[tag.Key] = true
			}
		}
		page.Rows = append(page.Rows, view)
	}
	slices.SortFunc(page.Tags, func(a, b monitorTag) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	s.render(w, r, http.StatusOK, "monitors.html", page)
}

// handleOrderMonitors stores the order of the list of all monitors. The form
// sends one item per monitor in the order they stand in: "m:" and its id.
func (s *Server) handleOrderMonitors(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	var ids []int64
	for _, item := range r.PostForm["item"] {
		if id, err := strconv.ParseInt(strings.TrimPrefix(item, "m:"), 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if err := s.store.SetMonitorOrder(r.Context(), ids); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/monitors", http.StatusSeeOther)
}
