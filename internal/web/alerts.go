package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/alert"
	"uppies/internal/store"
)

// testAlertDeadline bounds the wait when the admin sends a test alert.
const testAlertDeadline = 15 * time.Second

// alertsPage is the data of alerts.html: the install's alert channels.
type alertsPage struct {
	Chrome       chrome
	Channels     []store.AlertChannel
	ChannelKinds []string
	// NewChannel holds the add-channel form's values after a failed submit.
	NewChannel store.AlertChannel
	Problems   map[string]string
	// Notice reports the result of the last action, such as a test alert.
	Notice       string
	NoticeFailed bool
}

func (s *Server) alertsPage(r *http.Request) (alertsPage, error) {
	page := alertsPage{
		Chrome: s.adminChrome(r, "alerts"), ChannelKinds: store.AlertChannelKinds,
		NewChannel: store.AlertChannel{Kind: "webhook"},
	}
	var err error
	page.Channels, err = s.store.AlertChannels(r.Context())
	return page, err
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	page, err := s.alertsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "alerts.html", page)
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	page, err := s.alertsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	channel := store.AlertChannel{Kind: r.PostForm.Get("kind"), URL: strings.TrimSpace(r.PostForm.Get("url"))}
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
		s.render(w, r, http.StatusUnprocessableEntity, "alerts.html", page)
		return
	}
	if _, err := s.store.CreateAlertChannel(r.Context(), channel, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/alerts", http.StatusSeeOther)
}

// pathChannel finds the channel named by the path.
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
	page, err := s.alertsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	channel, err := pathChannel(r, page.Channels)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteAlertChannel(r.Context(), channel.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/alerts", http.StatusSeeOther)
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	page, err := s.alertsPage(r)
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
		Project: "test", Monitor: "test monitor", Target: "https://example.com",
		Down: true, Error: "this is a test alert from uppies", At: s.now(),
	}
	status := http.StatusOK
	page.Notice = "the test alert was delivered."
	if err := alert.Deliver(ctx, s.mailer, channel, event, time.Second); err != nil {
		status = http.StatusBadGateway
		page.NoticeFailed = true
		page.Notice = "the test alert could not be delivered: " + err.Error() + "."
	}
	s.render(w, r, status, "alerts.html", page)
}
