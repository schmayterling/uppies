package web

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"uppies/internal/store"
)

const apiPrefix = "/api/v1"

type apiRoute struct {
	pattern, permission string
	handler             http.HandlerFunc
}

func (s *Server) registerAPI(mux *http.ServeMux) {
	routes := []apiRoute{
		{"GET /api/v1/status-pages", pagesRead, s.apiListPages},
		{"POST /api/v1/status-pages", pagesCreate, s.apiCreatePage},
		{"GET /api/v1/status-pages/{id}", pagesRead, s.apiReadPage},
		{"PATCH /api/v1/status-pages/{id}", pagesUpdate, s.apiUpdatePage},
		{"DELETE /api/v1/status-pages/{id}", pagesDelete, s.apiDeletePage},
		{"GET /api/v1/status-pages/{id}/layout", pagesRead, s.apiReadLayout},
		{"PUT /api/v1/status-pages/{id}/layout", pagesUpdate, s.apiWriteLayout},
		{"GET /api/v1/monitors", monitorsRead, s.apiListMonitors},
		{"POST /api/v1/monitors", monitorsCreate, s.apiCreateMonitor},
		{"GET /api/v1/monitors/{id}", monitorsRead, s.apiReadMonitor},
		{"PATCH /api/v1/monitors/{id}", monitorsUpdate, s.apiUpdateMonitor},
		{"DELETE /api/v1/monitors/{id}", monitorsDelete, s.apiDeleteMonitor},
		{"GET /api/v1/monitors/{id}/alert-channels", monitorsRead, s.apiReadMonitorChannels},
		{"PUT /api/v1/monitors/{id}/alert-channels", monitorsUpdate, s.apiWriteMonitorChannels},
		{"GET /api/v1/alert-channels", channelsRead, s.apiListChannels},
		{"POST /api/v1/alert-channels", channelsCreate, s.apiCreateChannel},
		{"GET /api/v1/alert-channels/{id}", channelsRead, s.apiReadChannel},
		{"PATCH /api/v1/alert-channels/{id}", channelsUpdate, s.apiUpdateChannel},
		{"DELETE /api/v1/alert-channels/{id}", channelsDelete, s.apiDeleteChannel},
		{"GET /api/v1/status-pages/{id}/maintenance", maintenanceRead, s.apiListMaintenance},
		{"POST /api/v1/status-pages/{id}/maintenance", maintenanceCreate, s.apiCreateMaintenance},
		{"GET /api/v1/status-pages/{id}/maintenance/{window}", maintenanceRead, s.apiReadMaintenance},
		{"PATCH /api/v1/status-pages/{id}/maintenance/{window}", maintenanceUpdate, s.apiUpdateMaintenance},
		{"DELETE /api/v1/status-pages/{id}/maintenance/{window}", maintenanceDelete, s.apiDeleteMaintenance},
		{"GET /api/v1/status-pages/{id}/incidents", incidentsRead, s.apiListIncidents},
		{"POST /api/v1/status-pages/{id}/incidents", incidentsCreate, s.apiCreateIncident},
		{"GET /api/v1/status-pages/{id}/incidents/{incident}", incidentsRead, s.apiReadIncident},
		{"PATCH /api/v1/status-pages/{id}/incidents/{incident}", incidentsUpdate, s.apiUpdateIncident},
		{"DELETE /api/v1/status-pages/{id}/incidents/{incident}", incidentsDelete, s.apiDeleteIncident},
		{"GET /api/v1/status-pages/{id}/incidents/{incident}/updates", incidentsRead, s.apiListIncidentUpdates},
		{"POST /api/v1/status-pages/{id}/incidents/{incident}/updates", incidentsUpdate, s.apiAddIncidentUpdate},
		{"POST /api/v1/status-pages/{id}/incidents/{incident}/publish", incidentsPublish, s.apiPublishIncident},
	}
	allowed := make(map[string][]string)
	for _, route := range routes {
		mux.HandleFunc(route.pattern, s.requireAPIPermission(route.permission, route.handler))
		method, path, _ := strings.Cut(route.pattern, " ")
		allowed[path] = append(allowed[path], method)
		if method == http.MethodGet {
			allowed[path] = append(allowed[path], http.MethodHead)
		}
	}
	// Methodless fallbacks let ServeMux match the known path while keeping API
	// errors in JSON and applying authentication to every request.
	for path, methods := range allowed {
		slices.Sort(methods)
		allow := strings.Join(methods, ", ")
		mux.HandleFunc(path, s.requireAPIPermission("", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			s.apiError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "This resource does not accept that method.")
		}))
	}
	notFound := s.requireAPIPermission("", func(w http.ResponseWriter, r *http.Request) {
		s.apiFailure(w, r, store.ErrNotFound)
	})
	mux.HandleFunc(apiPrefix, notFound)
	mux.HandleFunc(apiPrefix+"/", notFound)
}

func isAPIPath(path string) bool {
	return path == apiPrefix || strings.HasPrefix(path, apiPrefix+"/")
}

func bearerToken(r *http.Request) string {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return ""
	}
	scheme, secret, _ := strings.Cut(headers[0], " ")
	token := strings.TrimLeft(secret, " ")
	if !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, apiTokenPrefix) {
		return ""
	}
	suffix := strings.TrimPrefix(token, apiTokenPrefix)
	if len(suffix) != 2*apiTokenBytes {
		return ""
	}
	var data [apiTokenBytes]byte
	if _, err := hex.Decode(data[:], []byte(suffix)); err != nil {
		return ""
	}
	return token
}

func (s *Server) requireAPIPermission(permission string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := bearerToken(r)
		if token == "" {
			s.apiUnauthorized(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		allowed, err := s.store.APITokenAllows(ctx, hashToken(token), permission, s.now().UnixMilli())
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.apiUnauthorized(w, r)
		case err != nil:
			s.apiFailure(w, r, err)
		case permission != "" && !allowed:
			s.apiError(w, r, http.StatusForbidden, "insufficient_permission", "This token requires "+permission+".")
		default:
			next(w, r)
		}
	}
}

func (s *Server) apiUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="uppies"`)
	s.apiError(w, r, http.StatusUnauthorized, "invalid_token", "Send a valid API token in the Authorization header.")
}

type apiProblem struct {
	status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (p *apiProblem) Error() string { return p.Message }

type apiErrorResponse struct {
	Error apiProblem `json:"error"`
}

func (s *Server) apiJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		s.log.Error("encode API response", "route", r.Pattern, "error_type", fmt.Sprintf("%T", err))
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"internal_error","message":"The request could not be completed."}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		s.log.Error("write API response", "route", r.Pattern, "error_type", fmt.Sprintf("%T", err))
	}
}

func (s *Server) apiError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.apiJSON(w, r, status, apiErrorResponse{Error: apiProblem{Code: code, Message: message}})
}

func (s *Server) apiFailure(w http.ResponseWriter, r *http.Request, err error) {
	if problem, ok := errors.AsType[*apiProblem](err); ok {
		s.apiJSON(w, r, problem.status, apiErrorResponse{Error: *problem})
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.apiError(w, r, http.StatusNotFound, "not_found", "The requested resource was not found.")
	case errors.Is(err, store.ErrUnknownMonitor):
		s.apiError(w, r, http.StatusUnprocessableEntity, "unknown_monitor", "A referenced monitor is unavailable for this resource.")
	case errors.Is(err, store.ErrUnknownChannel):
		s.apiError(w, r, http.StatusUnprocessableEntity, "unknown_alert_channel", "A referenced alert channel does not exist.")
	case errors.Is(err, store.ErrSlugTaken):
		s.apiError(w, r, http.StatusConflict, "slug_taken", "Another status page uses that slug.")
	case errors.Is(err, store.ErrDomainTaken):
		s.apiError(w, r, http.StatusConflict, "domain_taken", "Another status page uses that domain.")
	case errors.Is(err, context.DeadlineExceeded):
		s.apiError(w, r, http.StatusServiceUnavailable, "unavailable", "The request timed out.")
	default:
		// Database and validation errors can contain credentials supplied in requests.
		s.log.Error("API request failed", "route", r.Pattern, "error_type", fmt.Sprintf("%T", err))
		s.apiError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}

func (s *Server) decodeAPIJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		s.apiError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Use Content-Type: application/json.")
		return false
	}
	body, err := io.ReadAll(r.Body)
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		s.apiError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "Use a JSON body of at most 64 KiB.")
		return false
	}
	if err != nil || !nonNullJSONObject(body) || json.Unmarshal(body, value, json.RejectUnknownMembers(true)) != nil {
		s.apiError(w, r, http.StatusBadRequest, "invalid_json", "Send one JSON object with supported, non-null fields and types.")
		return false
	}
	return true
}

func nonNullJSONObject(data []byte) bool {
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	if decoder.PeekKind() != '{' {
		return false
	}
	for {
		token, err := decoder.ReadToken()
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil || token.Kind() == 'n' {
			return false
		}
	}
}

func invalidAPIField(field, message string) error {
	return &apiProblem{status: http.StatusUnprocessableEntity, Code: "invalid_field", Field: field, Message: message}
}

func apiText(field, value string, minLen, maxLen int) error {
	size := utf8.RuneCountInString(value)
	if size < minLen || size > maxLen || strings.ContainsRune(value, 0) {
		return invalidAPIField(field, fmt.Sprintf("Use text between %d and %d characters.", minLen, maxLen))
	}
	return nil
}

func apiID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}

func apiIDs(field string, values []string) ([]int64, error) {
	ids := make([]int64, 0, len(values))
	seen := make(map[int64]bool, len(values))
	for _, raw := range values {
		id, ok := apiID(raw)
		if !ok || seen[id] {
			return nil, invalidAPIField(field, "Use distinct positive decimal IDs.")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func apiPathID(r *http.Request, name string) (int64, error) {
	id, ok := apiID(r.PathValue(name))
	if !ok {
		return 0, store.ErrNotFound
	}
	return id, nil
}

func apiPageResourceIDs(r *http.Request, resource string) (int64, int64, error) {
	pageID, err := apiPathID(r, "id")
	if err != nil {
		return 0, 0, err
	}
	id, err := apiPathID(r, resource)
	return pageID, id, err
}

func (s *Server) apiCreated(w http.ResponseWriter, r *http.Request, location string, id int64) {
	value := strconv.FormatInt(id, 10)
	w.Header().Set("Location", location+"/"+value)
	s.apiJSON(w, r, http.StatusCreated, struct {
		ID string `json:"id"`
	}{ID: value})
}

const apiDefaultLimit = 50
const apiMaxLimit = 200

func apiListOptions(query string) (after int64, limit int, err error) {
	values, parseErr := url.ParseQuery(query)
	limit = apiDefaultLimit
	if parseErr != nil {
		return 0, 0, &apiProblem{status: http.StatusBadRequest, Code: "invalid_query", Message: "Use valid query parameters."}
	}
	for key, list := range values {
		if len(list) != 1 || (key != "after" && key != "limit") {
			return 0, 0, &apiProblem{status: http.StatusBadRequest, Code: "invalid_query", Message: "Use only after and limit."}
		}
		value, ok := apiID(list[0])
		if !ok {
			return 0, 0, &apiProblem{
				status: http.StatusBadRequest, Code: "invalid_query", Field: key, Message: "Use a positive decimal integer.",
			}
		}
		if key == "after" {
			after = value
		} else {
			limit = int(min(value, apiMaxLimit))
		}
	}
	return after, limit, nil
}

type apiListItem interface{ cursor() string }

func apiList[T any, V apiListItem](
	s *Server, w http.ResponseWriter, r *http.Request,
	query func(context.Context, int64, int) ([]T, error), view func(T) V,
) {
	after, limit, err := apiListOptions(r.URL.RawQuery)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	rows, err := query(r.Context(), after, limit+1)
	if err != nil {
		s.apiFailure(w, r, err)
		return
	}
	size := min(len(rows), limit)
	items := make([]V, size)
	for i, item := range rows[:size] {
		items[i] = view(item)
	}
	var next *string
	if len(rows) > size {
		cursor := items[size-1].cursor()
		next = &cursor
	}
	s.apiJSON(w, r, http.StatusOK, struct {
		Items     []V     `json:"items"`
		NextAfter *string `json:"next_after"`
	}{items, next})
}
