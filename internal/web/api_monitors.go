package web

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/check"
	"uppies/internal/health"
	"uppies/internal/store"
)

type apiMonitor struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Tags           []string      `json:"tags"`
	Kind           string        `json:"kind"`
	IntervalS      int           `json:"interval_s"`
	TimeoutMs      int           `json:"timeout_ms"`
	Retries        int           `json:"retries"`
	RetryIntervalS int           `json:"retry_interval_s"`
	RecoveryChecks int           `json:"recovery_checks"`
	Paused         bool          `json:"paused"`
	Status         health.Status `json:"status"`
	StatusSince    time.Time     `json:"status_since"`
	LastCheckAt    *time.Time    `json:"last_check_at"`
	LastLatencyMs  *int64        `json:"last_latency_ms"`
	LastPushAt     *time.Time    `json:"last_push_at"`
	CertExpiresAt  *time.Time    `json:"cert_expires_at"`

	AutoCreateIncident  string `json:"auto_create_incident"`
	AutoResolveIncident string `json:"auto_resolve_incident"`
}

func (m apiMonitor) cursor() string { return m.ID }

func apiOptionalTime(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	at := time.UnixMilli(ms).UTC()
	return &at
}

func monitorJSON(m store.Monitor) apiMonitor {
	// Targets, keywords, raw errors and push credentials stay out of every API response.
	var latency *int64
	if m.LastCheckAtMs != 0 {
		latency = &m.LastLatencyMs
	}
	return apiMonitor{
		ID: strconv.FormatInt(m.ID, 10), Name: m.Name, Tags: monitorTags(m.Tags), Kind: m.Kind,
		IntervalS: m.IntervalS, TimeoutMs: m.TimeoutMs, Retries: m.Retries,
		RetryIntervalS: m.RetryIntervalS, RecoveryChecks: m.RecoveryChecks, Paused: m.Paused,
		AutoCreateIncident: m.AutoCreateIncident, AutoResolveIncident: m.AutoResolveIncident,
		Status: m.Tracker.Status, StatusSince: time.UnixMilli(m.StatusSinceMs).UTC(),
		LastCheckAt: apiOptionalTime(m.LastCheckAtMs), LastLatencyMs: latency,
		LastPushAt: apiOptionalTime(m.LastPushAtMs), CertExpiresAt: apiOptionalTime(m.CertExpiresAtMs),
	}
}

type monitorPatch struct {
	Name           *string   `json:"name"`
	Tags           *[]string `json:"tags"`
	Kind           *string   `json:"kind"`
	Target         *string   `json:"target"`
	Keyword        *string   `json:"keyword"`
	IntervalS      *int      `json:"interval_s"`
	TimeoutMs      *int      `json:"timeout_ms"`
	Retries        *int      `json:"retries"`
	RetryIntervalS *int      `json:"retry_interval_s"`
	RecoveryChecks *int      `json:"recovery_checks"`
	Paused         *bool     `json:"paused"`

	AutoCreateIncident  *string `json:"auto_create_incident"`
	AutoResolveIncident *string `json:"auto_resolve_incident"`
}

func (input monitorPatch) apply(m *store.Monitor) error {
	creating := m.ID == 0
	if input.Name != nil {
		m.Name = strings.TrimSpace(*input.Name)
	}
	if creating || input.Name != nil {
		if err := apiText("name", m.Name, 1, 120); err != nil {
			return err
		}
	}

	if input.Tags != nil {
		tags := strings.Join(monitorTags(strings.Join(*input.Tags, ",")), ", ")
		if err := apiText("tags", tags, 0, 1000); err != nil {
			return err
		}
		m.Tags = tags
	}

	oldKind := m.Kind
	if input.Kind != nil {
		if !slices.Contains(check.Kinds, *input.Kind) {
			return invalidAPIField("kind", "Use a supported monitor kind.")
		}
		m.Kind = *input.Kind
	}
	if input.Target != nil {
		m.Target = strings.TrimSpace(*input.Target)
	}
	if m.Kind == check.KindPush {
		if input.Target != nil && m.Target != "" {
			return invalidAPIField("target", "Push monitors have no target.")
		}
		m.Target = check.KindPush
		if m.PushToken == "" {
			m.PushToken = rand.Text()
		}
	} else {
		m.PushToken = ""
		if oldKind != m.Kind && input.Target == nil {
			return invalidAPIField("target", "Supply a target when changing to this monitor kind.")
		}
		if creating || input.Target != nil {
			if err := apiText("target", m.Target, 1, 2048); err != nil {
				return err
			}
			if check.ValidateTarget(m.Kind, m.Target) != nil {
				return invalidAPIField("target", "Use a valid target for this monitor kind.")
			}
			if m.Kind == check.KindTCP {
				host, port, _ := net.SplitHostPort(m.Target) // ValidateTarget checked the split.
				number, err := strconv.Atoi(port)
				if err != nil || number < 1 || number > 65_535 || strings.ContainsAny(host, "/\\ \t\r\n") {
					return invalidAPIField("target", "Use a host and a numeric port from 1 to 65535.")
				}
			}
		}
	}
	if input.Keyword != nil {
		if m.Kind != check.KindHTTP && *input.Keyword != "" {
			return invalidAPIField("keyword", "Only HTTP monitors accept a keyword.")
		}
		if err := apiText("keyword", *input.Keyword, 0, 200); err != nil {
			return err
		}
		m.Keyword = *input.Keyword
	}
	if oldKind != m.Kind && m.Kind != check.KindHTTP {
		m.Keyword = ""
	}

	for _, field := range []struct {
		key          string
		value, dest  *int
		minimum, max int
	}{
		{"interval_s", input.IntervalS, &m.IntervalS, 20, 86_400},
		{"timeout_ms", input.TimeoutMs, &m.TimeoutMs, 1000, 60_000},
		{"retries", input.Retries, &m.Retries, 0, 10},
		{"retry_interval_s", input.RetryIntervalS, &m.RetryIntervalS, 1, 3600},
		{"recovery_checks", input.RecoveryChecks, &m.RecoveryChecks, 1, 10},
	} {
		if field.value == nil {
			continue
		}
		value := *field.value
		if value < field.minimum || value > field.max {
			return invalidAPIField(field.key, fmt.Sprintf("Use an integer from %d to %d.", field.minimum, field.max))
		}
		*field.dest = value
	}
	if input.TimeoutMs != nil && m.TimeoutMs%1000 != 0 {
		return invalidAPIField("timeout_ms", "Use a whole number of seconds expressed in milliseconds.")
	}
	if input.AutoCreateIncident != nil {
		if !slices.Contains(store.IncidentCreateModes, *input.AutoCreateIncident) {
			return invalidAPIField("auto_create_incident", "Use off, draft or publish.")
		}
		m.AutoCreateIncident = *input.AutoCreateIncident
	}
	if input.AutoResolveIncident != nil {
		if !slices.Contains(store.IncidentResolveModes, *input.AutoResolveIncident) {
			return invalidAPIField("auto_resolve_incident", "Use off, drafts or all.")
		}
		m.AutoResolveIncident = *input.AutoResolveIncident
	}
	if input.Paused != nil {
		m.Paused = *input.Paused
	}
	return nil
}

func (s *Server) authorizeMonitorIncidentPolicy(w http.ResponseWriter, r *http.Request, input monitorPatch) bool {
	if input.AutoCreateIncident != nil && *input.AutoCreateIncident == store.IncidentCreatePublish {
		if !s.checkAPIPermission(w, r, incidentsCreate) || !s.checkAPIPermission(w, r, incidentsPublish) {
			return false
		}
	}
	if input.AutoResolveIncident != nil && *input.AutoResolveIncident == store.IncidentResolveAll {
		return s.checkAPIPermission(w, r, incidentsUpdate)
	}
	return true
}

func (s *Server) apiListMonitors(w http.ResponseWriter, r *http.Request) {
	apiList(s, w, r, s.store.MonitorsAfter, monitorJSON)
}

func (s *Server) apiReadMonitor(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	m, err := s.store.Monitor(r.Context(), id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.apiJSON(w, r, http.StatusOK, monitorJSON(m))
}

func (s *Server) apiCreateMonitor(w http.ResponseWriter, r *http.Request) {
	var input monitorPatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	m := defaultMonitor()
	if err := input.apply(&m); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	if !s.authorizeMonitorIncidentPolicy(w, r, input) {
		return
	}
	id, err := s.store.CreateMonitor(r.Context(), m, s.now().UnixMilli())
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	s.apiCreated(w, r, "/api/v1/monitors", id)
}

func (s *Server) apiUpdateMonitor(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input monitorPatch
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	if !s.authorizeMonitorIncidentPolicy(w, r, input) {
		return
	}
	if err := s.store.EditMonitor(r.Context(), id, input.apply); err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiDeleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err == nil {
		err = s.store.DeleteMonitor(r.Context(), id)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	s.monitorsChanged()
	w.WriteHeader(http.StatusNoContent)
}

type apiMonitorChannels struct {
	ChannelIDs []string `json:"alert_channel_ids"`
}

func (s *Server) apiReadMonitorChannels(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	channels, err := s.store.MonitorChannelIDs(r.Context(), id)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	result := apiMonitorChannels{ChannelIDs: make([]string, len(channels))}
	for i, channelID := range channels {
		result.ChannelIDs[i] = strconv.FormatInt(channelID, 10)
	}
	s.apiJSON(w, r, http.StatusOK, result)
}

func (s *Server) apiWriteMonitorChannels(w http.ResponseWriter, r *http.Request) {
	id, err := apiPathID(r, "id")
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	var input apiMonitorChannels
	if !s.decodeAPIJSON(w, r, &input) {
		return
	}
	if input.ChannelIDs == nil {
		s.apiFailure(w, r, invalidAPIField("alert_channel_ids", "Supply an array of IDs. Use an empty array to clear assignments."))
		return
	}
	channels, err := apiIDs("alert_channel_ids", input.ChannelIDs)
	if err == nil {
		err = s.store.ReplaceMonitorChannels(r.Context(), id, channels)
	}
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
