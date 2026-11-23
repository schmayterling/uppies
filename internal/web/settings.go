package web

import (
	"context"
	"fmt"
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

type settingsPage struct {
	Chrome       chrome
	Project      store.Project
	Channels     []store.AlertChannel
	ChannelKinds []string
	// NewChannel holds the add-channel form's values after a failed submit.
	NewChannel store.AlertChannel
	Problems   map[string]string
	// Notice reports the result of the last action, such as a test alert.
	Notice string
}

func (s *Server) settingsPage(r *http.Request) (settingsPage, error) {
	page := settingsPage{ChannelKinds: store.AlertChannelKinds, NewChannel: store.AlertChannel{Kind: "webhook"}}
	id, err := pathID(r)
	if err != nil {
		return page, err
	}
	page.Project, err = s.store.ProjectByID(r.Context(), id)
	if err != nil {
		return page, err
	}
	page.Chrome = chrome{Title: page.Project.Name + " settings", Admin: true}
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
	if len(channel.URL) > 2048 {
		problems["url"] = "the url is too long."
	} else if err := alert.ValidateURL(channel.URL); err != nil {
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
	if err := alert.Send(ctx, channel, event, time.Second); err != nil {
		status = http.StatusBadGateway
		page.Notice = "the test alert could not be delivered: " + err.Error() + "."
	}
	s.render(w, r, status, "settings.html", page)
}
