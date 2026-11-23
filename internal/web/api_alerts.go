package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"uppies/internal/alert"
	"uppies/internal/store"
)

type apiChannel struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

func (c apiChannel) cursor() string { return c.ID }

func channelJSON(c store.AlertChannel) apiChannel {
	return apiChannel{ID: strconv.FormatInt(c.ID, 10), Kind: c.Kind}
}

type channelPatch struct {
	Kind        *string `json:"kind"`
	Destination *string `json:"destination"`
}

func (input channelPatch) apply(c *store.AlertChannel) error {
	creating := c.ID == 0
	oldKind := c.Kind
	if input.Kind != nil {
		c.Kind = *input.Kind
	}
	if creating || input.Kind != nil {
		if !slices.Contains(store.AlertChannelKinds, c.Kind) {
			return invalidAPIField("kind", "Use a supported alert channel kind.")
		}
	}
	if input.Destination != nil {
		c.URL = strings.TrimSpace(*input.Destination)
	} else if creating || oldKind != c.Kind {
		return invalidAPIField("destination", "Supply a destination for the alert channel.")
	}
	if input.Destination == nil {
		return nil
	}
	if err := apiText("destination", c.URL, 1, 2048); err != nil {
		return err
	}
	if c.Kind == "email" {
		if alert.ValidateEmail(c.URL) != nil {
			return invalidAPIField("destination", "Use a valid email address.")
		}
	} else if alert.ValidateURL(c.URL) != nil {
		return invalidAPIField("destination", "Use an HTTP or HTTPS URL with a host.")
	}
	return nil
}

func (s *Server) apiListChannels(w http.ResponseWriter, r *http.Request) {
	apiList(s, w, r, s.store.AlertChannelsAfter, channelJSON)
}

func (s *Server) apiReadChannel(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	channel, err := s.store.AlertChannel(r.Context(), id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiJSON(w, r, http.StatusOK, channelJSON(channel))
}

func (s *Server) apiCreateChannel(w http.ResponseWriter, r *http.Request) {
	var input channelPatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	var c store.AlertChannel
	if err := input.apply(&c); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	id, err := s.store.CreateUnassignedAlertChannel(r.Context(), c, s.now().UnixMilli())
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiCreated(w, r, "/api/v1/alert-channels", id)
}

func (s *Server) apiUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input channelPatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	if err := s.store.EditAlertChannel(r.Context(), id, input.apply); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err == nil {
		err = s.store.DeleteAlertChannel(r.Context(), id)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
