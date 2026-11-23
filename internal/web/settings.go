package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/alert"
	"uppies/internal/store"
)

// testAlertDeadline bounds the wait when the admin sends a test alert.
const testAlertDeadline = 15 * time.Second

type settingsPage struct {
	Chrome       chrome
	Project      store.Project
	Channels     []store.AlertChannel
	ChannelKinds []string
	Themes       []string
	HistoryViews []int
	Swatches     []int
	// CustomHue is true when the accent is not one of the swatches.
	CustomHue bool
	Surfaces  []string
	Corners   []string
	Fonts     []string
	// Images maps each image kind the project has to its version.
	Images map[string]int64
	// NewChannel holds the add-channel form's values after a failed submit.
	NewChannel store.AlertChannel
	Problems   map[string]string
	// Notice reports the result of the last action, such as a test alert.
	Notice       string
	NoticeFailed bool
}

func (s *Server) settingsPage(r *http.Request) (settingsPage, error) {
	page := settingsPage{
		ChannelKinds: store.AlertChannelKinds,
		Themes:       themes,
		HistoryViews: historyViews,
		Swatches:     accentSwatches,
		Surfaces:     surfaces,
		Corners:      cornerStyles,
		Fonts:        fonts,
		NewChannel:   store.AlertChannel{Kind: "webhook"},
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
	page.CustomHue = !slices.Contains(accentSwatches, page.Project.AccentHue)
	page.Images, err = s.store.ProjectImageVersions(r.Context(), id)
	if err != nil {
		return page, err
	}
	page.Channels, err = s.store.AlertChannels(r.Context(), id)
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
	historyViews  = []int{30, 60, 90}
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
	p.CustomCSS = form.Get("custom_css")
	p.FooterText = strings.TrimSpace(form.Get("footer_text"))
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
	p.Font = choice(form, "font", fonts, p.Font, problems)
	// A value that is not a number fails the membership check below.
	p.HistoryDays, _ = strconv.Atoi(form.Get("history_days"))

	if p.Name == "" || len(p.Name) > 120 {
		problems["name"] = "give the project a name of 1 to 120 characters."
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
	if !slices.Contains(historyViews, p.HistoryDays) {
		problems["history_days"] = "choose 30, 60 or 90 days."
	}
	if len(p.CustomCSS) > 20000 {
		problems["custom_css"] = "keep the custom css to 20000 characters."
	}
	if len(p.FooterText) > 500 {
		problems["footer_text"] = "keep the footer text to 500 characters."
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" || len(p.Timezone) > 64 {
		problems["timezone"] = "enter a timezone name such as UTC, Asia/Manila or Europe/Berlin."
	}
	if p.Domain != "" && (len(p.Domain) > 253 || !domainPattern.MatchString(p.Domain)) {
		problems["domain"] = "enter a hostname without http:// or a path, for example status.example.com."
	}
	page.Project = p
	if len(problems) == 0 {
		err := s.store.UpdateProject(r.Context(), p)
		if errors.Is(err, store.ErrDomainTaken) {
			problems["domain"] = "another project already uses this domain."
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

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	channel := store.AlertChannel{
		ProjectID: page.Project.ID,
		Kind:      r.PostForm.Get("kind"),
		URL:       strings.TrimSpace(r.PostForm.Get("url")),
	}
	problems := make(map[string]string)
	if !slices.Contains(store.AlertChannelKinds, channel.Kind) {
		problems["kind"] = "choose a kind."
	}
	validate := alert.ValidateURL
	if channel.Kind == "email" {
		validate = alert.ValidateEmail
	}
	if len(channel.URL) > 2048 {
		problems["url"] = "that is too long."
	} else if err := validate(channel.URL); err != nil {
		problems["url"] = err.Error() + "."
	}
	if len(problems) > 0 {
		page.NewChannel, page.Problems = channel, problems
		s.render(w, r, http.StatusUnprocessableEntity, "settings.html", page)
		return
	}
	if _, err := s.store.CreateAlertChannel(r.Context(), channel, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d/settings", page.Project.ID), http.StatusSeeOther)
}

// pathChannel finds the channel named by the path among the project's channels.
func pathChannel(r *http.Request, channels []store.AlertChannel) (store.AlertChannel, error) {
	channelID, err := strconv.ParseInt(r.PathValue("channel"), 10, 64)
	if err != nil {
		return store.AlertChannel{}, store.ErrNotFound
	}
	for _, c := range channels {
		if c.ID == channelID {
			return c, nil
		}
	}
	return store.AlertChannel{}, store.ErrNotFound
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	channel, err := pathChannel(r, page.Channels)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteAlertChannel(r.Context(), page.Project.ID, channel.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d/settings", page.Project.ID), http.StatusSeeOther)
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	channel, err := pathChannel(r, page.Channels)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testAlertDeadline)
	defer cancel()
	event := alert.Event{
		Project: page.Project.Name, Monitor: "test monitor", Target: "https://example.com",
		Down: true, Error: "this is a test alert from uppies", At: s.now(),
	}
	status := http.StatusOK
	page.Notice = "the test alert was delivered."
	if err := alert.Deliver(ctx, s.mailer, channel, event, time.Second); err != nil {
		status = http.StatusBadGateway
		page.NoticeFailed = true
		page.Notice = "the test alert could not be delivered: " + err.Error() + "."
	}
	s.render(w, r, status, "settings.html", page)
}
