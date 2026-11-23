package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"uppies/internal/store"
)

const (
	apiTokenPrefix = "uppies_"
	apiTokenBytes  = 32

	pagesRead         = "status_pages:read"
	pagesCreate       = "status_pages:create"
	pagesUpdate       = "status_pages:update"
	pagesDelete       = "status_pages:delete"
	monitorsRead      = "monitors:read"
	monitorsCreate    = "monitors:create"
	monitorsUpdate    = "monitors:update"
	monitorsDelete    = "monitors:delete"
	incidentsRead     = "incidents:read"
	incidentsCreate   = "incidents:create"
	incidentsUpdate   = "incidents:update"
	incidentsPublish  = "incidents:publish"
	incidentsDelete   = "incidents:delete"
	maintenanceRead   = "maintenance:read"
	maintenanceCreate = "maintenance:create"
	maintenanceUpdate = "maintenance:update"
	maintenanceDelete = "maintenance:delete"
	channelsRead      = "alert_channels:read"
	channelsCreate    = "alert_channels:create"
	channelsUpdate    = "alert_channels:update"
	channelsDelete    = "alert_channels:delete"
)

type apiPermission struct {
	Key   string
	Label string
}

type apiPermissionGroup struct {
	Name        string
	Permissions []apiPermission
}

var apiPermissions = []apiPermissionGroup{
	{"status pages", []apiPermission{{pagesRead, "read"}, {pagesCreate, "create"}, {pagesUpdate, "update"}, {pagesDelete, "delete"}}},
	{"monitors", []apiPermission{
		{monitorsRead, "read"}, {monitorsCreate, "create"}, {monitorsUpdate, "update"}, {monitorsDelete, "delete"},
	}},
	{"incidents", []apiPermission{
		{incidentsRead, "read"}, {incidentsCreate, "create"}, {incidentsUpdate, "update"},
		{incidentsPublish, "publish"}, {incidentsDelete, "delete"},
	}},
	{"maintenance", []apiPermission{
		{maintenanceRead, "read"}, {maintenanceCreate, "create"}, {maintenanceUpdate, "update"}, {maintenanceDelete, "delete"},
	}},
	{"alert channels", []apiPermission{
		{channelsRead, "read"}, {channelsCreate, "create"}, {channelsUpdate, "update"}, {channelsDelete, "delete"},
	}},
}

type apiTokenRow struct {
	store.APIToken
	Status string
}

func makeAPITokenRow(token store.APIToken, nowMs int64) apiTokenRow {
	status := "active"
	switch {
	case token.RevokedAtMs != 0:
		status = "revoked"
	case token.ExpiresAtMs <= nowMs:
		status = "expired"
	}
	return apiTokenRow{APIToken: token, Status: status}
}

type apiTokensPage struct {
	Chrome    chrome
	Tokens    []apiTokenRow
	Token     *apiTokenRow
	Groups    []apiPermissionGroup
	Selected  map[string]bool
	Name      string
	Lifetime  string
	Notice    string
	Secret    string
	ExpiresMs int64
	Problems  map[string]string
}

func (s *Server) apiTokensPage(r *http.Request) apiTokensPage {
	chrome := s.adminChrome(r, "api tokens")
	chrome.Nav.Section = "api-tokens"
	return apiTokensPage{Chrome: chrome, Groups: apiPermissions}
}

func (s *Server) renderAPITokenList(w http.ResponseWriter, r *http.Request, status int, page apiTokensPage) {
	tokens, err := s.store.APITokens(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nowMs := s.now().UnixMilli()
	for _, token := range tokens {
		page.Tokens = append(page.Tokens, makeAPITokenRow(token, nowMs))
	}
	if r.URL.Query().Get("revoked") == "1" {
		page.Notice = "token revoked."
	}
	s.render(w, r, status, "api_tokens.html", page)
}

func (s *Server) handleAPITokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	page := s.apiTokensPage(r)
	page.Lifetime = "30"
	s.renderAPITokenList(w, r, http.StatusOK, page)
}

func selectAPIPermissions(values []string) (selected map[string]bool, permissions []string, valid bool) {
	selected = make(map[string]bool)
	for _, value := range values {
		selected[value] = true
	}
	for _, group := range apiPermissions {
		for _, permission := range group.Permissions {
			if selected[permission.Key] {
				permissions = append(permissions, permission.Key)
			}
		}
	}
	return selected, permissions, len(selected) == len(permissions)
}

func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	userID, _, signedIn, err := s.sessionUser(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !signedIn {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	page := s.apiTokensPage(r)
	page.Name = strings.TrimSpace(r.PostForm.Get("name"))
	page.Lifetime = r.PostForm.Get("lifetime")
	selected, permissions, valid := selectAPIPermissions(r.PostForm["permission"])
	page.Selected = selected
	page.Problems = make(map[string]string)
	if page.Name == "" || !utf8.ValidString(page.Name) || utf8.RuneCountInString(page.Name) > 80 || strings.ContainsRune(page.Name, 0) {
		page.Problems["name"] = "enter a name of 1 to 80 characters."
	}
	switch page.Lifetime {
	case "30", "90", "365":
	default:
		page.Problems["lifetime"] = "choose how long the token should last."
	}
	if !valid {
		page.Problems["permission"] = "choose permissions from the list."
	}
	if len(page.Problems) != 0 {
		s.renderAPITokenList(w, r, http.StatusUnprocessableEntity, page)
		return
	}

	var random [apiTokenBytes]byte
	rand.Read(random[:])
	secret := apiTokenPrefix + hex.EncodeToString(random[:])
	now := s.now()
	days, _ := strconv.Atoi(page.Lifetime) // Already validated above.
	expiresMs := now.Add(time.Duration(days) * 24 * time.Hour).UnixMilli()
	if err := s.store.CreateAPIToken(r.Context(), userID, page.Name, hashToken(secret), now.UnixMilli(), expiresMs, permissions); err != nil {
		s.fail(w, r, err)
		return
	}

	page.Secret, page.ExpiresMs = secret, expiresMs
	s.render(w, r, http.StatusCreated, "api_tokens.html", page)
}

func (s *Server) apiTokenDetail(r *http.Request) (apiTokensPage, error) {
	id, err := pathID(r)
	if err != nil {
		return apiTokensPage{}, err
	}
	token, permissions, err := s.store.APIToken(r.Context(), id)
	if err != nil {
		return apiTokensPage{}, err
	}
	page := s.apiTokensPage(r)
	row := makeAPITokenRow(token, s.now().UnixMilli())
	page.Token = &row
	page.Selected, _, _ = selectAPIPermissions(permissions)
	return page, nil
}

func (s *Server) handleAPIToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	page, err := s.apiTokenDetail(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("saved") == "1" {
		page.Notice = "permissions saved."
	}
	s.render(w, r, http.StatusOK, "api_tokens.html", page)
}

func (s *Server) handleAPITokenPermissions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	_, permissions, valid := selectAPIPermissions(r.PostForm["permission"])
	if !valid {
		page, err := s.apiTokenDetail(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.Problems = map[string]string{"permission": "choose permissions from the list."}
		s.render(w, r, http.StatusUnprocessableEntity, "api_tokens.html", page)
		return
	}
	if err := s.store.SetAPITokenPermissions(r.Context(), id, s.now().UnixMilli(), permissions); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/api-tokens/%d?saved=1", id), http.StatusSeeOther)
}

func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), id, s.now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/api-tokens?revoked=1", http.StatusSeeOther)
}
