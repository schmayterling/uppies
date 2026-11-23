// Package web serves the public status pages and the admin interface.
package web

import (
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"uppies/internal/alert"
	"uppies/internal/store"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

// maxFormBytes caps the size of a posted form.
const maxFormBytes = 64 << 10

// Server holds what the handlers need.
type Server struct {
	store *store.Store
	log   *slog.Logger
	// monitorsChanged tells the check engine that monitors were added, edited or removed.
	monitorsChanged func()
	// mailer sends test emails. It is nil when no SMTP server is configured.
	mailer *alert.Mailer
	pages  map[string]*template.Template
	logins *loginLimiter
	now    func() time.Time
}

// New builds the server. monitorsChanged is called after every monitor
// change. mailer may be nil.
func New(st *store.Store, log *slog.Logger, monitorsChanged func(), mailer *alert.Mailer) (*Server, error) {
	s := &Server{
		store:           st,
		log:             log,
		monitorsChanged: monitorsChanged,
		mailer:          mailer,
		pages:           make(map[string]*template.Template),
		logins:          newLoginLimiter(),
		now:             time.Now,
	}
	names := []string{
		"index.html", "status.html", "error.html", "setup.html", "login.html", "dashboard.html",
		"project.html", "monitor_form.html", "incident_new.html", "incident.html", "settings.html",
	}
	for _, name := range names {
		page, err := template.New("base.html").Funcs(templateFuncs).
			ParseFS(templateFiles, "templates/base.html", "templates/"+name)
		if err != nil {
			return nil, err
		}
		s.pages[name] = page
	}
	return s, nil
}

// Handler returns the routes wrapped in the security middleware.
func (s *Server) Handler() (http.Handler, error) {
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()

	// Public entry points. Everything else below requires a signed-in admin.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /s/{slug}", s.handleStatusPage)
	mux.HandleFunc("GET /s/{slug}/theme.css", s.handleTheme)
	mux.HandleFunc("GET /s/{slug}/badge.svg", s.handleBadge)
	mux.HandleFunc("GET /s/{slug}/images/{kind}", s.handleImage)
	// A push monitor's job reports in here. The token in the path is the credential.
	mux.HandleFunc("GET /push/{token}", s.handlePush)
	mux.HandleFunc("POST /push/{token}", s.handlePush)
	mux.HandleFunc("GET /admin/setup", s.handleSetupForm)
	mux.HandleFunc("POST /admin/setup", s.handleSetup)
	mux.HandleFunc("GET /admin/login", s.handleLoginForm)
	mux.HandleFunc("POST /admin/login", s.handleLogin)

	admin := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.requireAdmin(handler))
	}
	admin("POST /admin/logout", s.handleLogout)
	admin("GET /admin", s.handleDashboard)
	admin("POST /admin/projects", s.handleCreateProject)
	admin("GET /admin/projects/{id}", s.handleProject)
	admin("POST /admin/projects/{id}/delete", s.handleDeleteProject)
	admin("GET /admin/projects/{id}/settings", s.handleSettings)
	admin("POST /admin/projects/{id}/settings", s.handleUpdateSettings)
	admin("POST /admin/projects/{id}/images", s.handleUpdateImages)
	admin("POST /admin/projects/{id}/channels", s.handleCreateChannel)
	admin("POST /admin/projects/{id}/channels/{channel}/delete", s.handleDeleteChannel)
	admin("POST /admin/projects/{id}/channels/{channel}/test", s.handleTestChannel)
	admin("GET /admin/projects/{id}/monitors/new", s.handleNewMonitorForm)
	admin("POST /admin/projects/{id}/monitors", s.handleCreateMonitor)
	admin("GET /admin/monitors/{id}", s.handleEditMonitorForm)
	admin("POST /admin/monitors/{id}", s.handleUpdateMonitor)
	admin("POST /admin/monitors/{id}/delete", s.handleDeleteMonitor)
	admin("GET /admin/projects/{id}/incidents/new", s.handleNewIncidentForm)
	admin("POST /admin/projects/{id}/incidents", s.handleCreateIncident)
	admin("GET /admin/incidents/{id}", s.handleIncident)
	admin("POST /admin/incidents/{id}/updates", s.handleAddIncidentUpdate)
	admin("POST /admin/incidents/{id}/publish", s.handlePublishIncident)
	admin("POST /admin/incidents/{id}/delete", s.handleDeleteIncident)

	crossOrigin := http.NewCrossOriginProtection()
	// Scheduled jobs are not browsers, and the token already proves who calls.
	crossOrigin.AddInsecureBypassPattern("POST /push/{token}")
	return crossOrigin.Handler(securityHeaders(limitBody(mux))), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxFormBytes)
		if strings.HasSuffix(r.URL.Path, "/images") {
			limit = maxUploadBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// render writes a page. data must have the fields base.html reads.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.pages[page].Execute(w, data); err != nil {
		s.log.Error("render page", "page", page, "path", r.URL.Path, "error", err)
	}
}

// errorPage is the data of error.html.
type errorPage struct {
	Chrome  chrome
	Heading string
	Message string
}

// fail answers a request that could not be served and logs unexpected errors.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.render(w, r, http.StatusNotFound, "error.html", errorPage{
			Chrome:  chrome{Title: "not found"},
			Heading: "nothing here.",
			Message: "this page does not exist, or it was removed.",
		})
	case errors.As(err, &tooLarge):
		s.render(w, r, http.StatusRequestEntityTooLarge, "error.html", errorPage{
			Chrome:  chrome{Title: "too large"},
			Heading: "that form is too large.",
			Message: "shorten the text and send it again.",
		})
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		s.render(w, r, http.StatusInternalServerError, "error.html", errorPage{
			Chrome:  chrome{Title: "error"},
			Heading: "something broke on our side.",
			Message: "the error was logged. try again in a moment.",
		})
	}
}

// pathID reads the {id} path value. A value that is not a number is a missing page.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, store.ErrNotFound
	}
	return id, nil
}

// chrome is what base.html needs on every page.
type chrome struct {
	Title string
	Admin bool // show the admin navigation
	// Unbranded drops the "uppies" suffix from the browser title.
	Unbranded bool
}

var templateFuncs = template.FuncMap{
	"utc": func(ms int64) string {
		return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04") + " utc"
	},
	// local shows a time in the given zone with its offset, which stays
	// unambiguous where zone abbreviations do not.
	"local": func(zone *time.Location, ms int64) string {
		if zone == time.UTC {
			return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04") + " utc"
		}
		return time.UnixMilli(ms).In(zone).Format("2006-01-02 15:04 -07:00")
	},
	"rfc3339": func(ms int64) string {
		return time.UnixMilli(ms).UTC().Format(time.RFC3339)
	},
	"contains": slices.Contains[[]int64],
	"kindLabel": func(kind string) string {
		return kindLabels[kind]
	},
	// daysUntil counts whole days from now to a time, for certificate expiry.
	"daysUntil": func(ms int64) int {
		return int(time.Until(time.UnixMilli(ms)).Hours() / 24)
	},
	"seconds": func(ms int) int {
		return ms / 1000
	},
}
