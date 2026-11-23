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
	mailer    *alert.Mailer
	pages     map[string]*template.Template
	logins    *loginLimiter
	now       func() time.Time
	startedAt time.Time
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
		startedAt:       time.Now(),
	}
	names := []string{
		"index.html", "status.html", "error.html", "setup.html", "login.html", "dashboard.html",
		"project.html", "monitor_form.html", "incident_new.html", "incident.html", "settings.html", "history.html", "monitor.html", "alerts.html", "monitors.html", "editor.html", "projects.html", "appearance.html", "instance.html", "users.html", "api_tokens.html",
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
	mux.HandleFunc("GET /theme.css", s.handleInstanceTheme)
	mux.HandleFunc("GET /sign-in-background", s.handleSignInBackground)
	mux.HandleFunc("GET /favicon.ico", s.handleInstanceFavicon)
	mux.HandleFunc("GET /s/{slug}", s.handleStatusPage)
	mux.HandleFunc("GET /s/{slug}/history", s.handleHistory)
	mux.HandleFunc("GET /s/{slug}/theme.css", s.handleTheme)
	mux.HandleFunc("GET /s/{slug}/badge.svg", s.handleBadge)
	mux.HandleFunc("GET /s/{slug}/88x31.svg", s.handlePageBadge)
	mux.HandleFunc("GET /s/{slug}/monitors/{id}", s.handleMonitorPage)
	mux.HandleFunc("GET /s/{slug}/monitors/{id}/{badge}", s.handleMonitorBadge)
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
	admin("GET /admin/appearance", s.handleAppearance)
	admin("POST /admin/appearance", s.handleSaveAppearance)
	admin("POST /admin/appearance/sign-in", s.handleSaveSignInPage)
	admin("POST /admin/appearance/favicon", s.handleSaveFavicon)
	admin("GET /admin/instance", s.handleInstance)
	admin("POST /admin/instance", s.handleSaveInstance)
	admin("GET /admin/users", s.handleUsers)
	admin("POST /admin/users", s.handleCreateUser)
	admin("POST /admin/users/password", s.handleChangePassword)
	admin("POST /admin/users/{id}/delete", s.handleDeleteUser)
	admin("GET /admin/api-tokens", s.handleAPITokens)
	admin("POST /admin/api-tokens", s.handleCreateAPIToken)
	admin("GET /admin/api-tokens/{id}", s.handleAPIToken)
	admin("POST /admin/api-tokens/{id}/permissions", s.handleAPITokenPermissions)
	admin("POST /admin/api-tokens/{id}/revoke", s.handleRevokeAPIToken)
	admin("GET /admin/projects", s.handleProjects)
	admin("POST /admin/projects", s.handleCreateProject)
	admin("POST /admin/projects/order", s.handleOrderProjects)
	admin("GET /admin/projects/{id}", s.handleProject)
	admin("GET /admin/projects/{id}/editor", s.handleEditor)
	admin("POST /admin/projects/{id}/delete", s.handleDeleteProject)
	admin("POST /admin/projects/{id}/placements", s.handleSavePlacements)
	admin("POST /admin/projects/{id}/look", s.handleSaveLook)
	admin("POST /admin/projects/{id}/maintenance", s.handleCreateMaintenance)
	admin("POST /admin/projects/{id}/maintenance/{window}/delete", s.handleDeleteMaintenance)
	admin("GET /admin/projects/{id}/settings", s.handleSettings)
	admin("POST /admin/projects/{id}/settings", s.handleUpdateSettings)
	admin("POST /admin/projects/{id}/images", s.handleUpdateImages)
	admin("GET /admin/alerts", s.handleAlerts)
	admin("POST /admin/alerts", s.handleCreateChannel)
	admin("POST /admin/alerts/{channel}/delete", s.handleDeleteChannel)
	admin("POST /admin/alerts/{channel}/test", s.handleTestChannel)
	admin("GET /admin/projects/{id}/monitors/new", s.handleNewMonitorOnPage)
	admin("GET /admin/monitors", s.handleMonitors)
	admin("GET /admin/monitors/new", s.handleNewMonitorForm)
	admin("POST /admin/monitors", s.handleCreateMonitor)
	admin("POST /admin/monitors/order", s.handleOrderMonitors)
	admin("GET /admin/monitors/{id}", s.handleEditMonitorForm)
	admin("POST /admin/monitors/{id}", s.handleUpdateMonitor)
	admin("POST /admin/monitors/{id}/delete", s.handleDeleteMonitor)
	admin("GET /admin/projects/{id}/incidents/new", s.handleNewIncidentForm)
	admin("POST /admin/projects/{id}/incidents", s.handleCreateIncident)
	admin("GET /admin/incidents/{id}", s.handleIncident)
	admin("POST /admin/incidents/{id}/updates", s.handleAddIncidentUpdate)
	admin("POST /admin/incidents/{id}/publish", s.handlePublishIncident)
	admin("POST /admin/incidents/{id}/delete", s.handleDeleteIncident)

	s.registerAPI(mux)
	crossOrigin := http.NewCrossOriginProtection()
	crossOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
			s.apiError(w, r, http.StatusForbidden, "cross_origin", "Cross-origin writes are not allowed.")
			return
		}
		http.Error(w, crossOrigin.Check(r).Error(), http.StatusForbidden)
	}))
	// Scheduled jobs are not browsers, and the token already proves who calls.
	crossOrigin.AddInsecureBypassPattern("POST /push/{token}")
	return crossOrigin.Handler(securityHeaders(limitBody(mux))), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxFormBytes)
		switch {
		case strings.HasSuffix(r.URL.Path, "/images"):
			limit = maxUploadBytes
		case r.URL.Path == "/admin/appearance/sign-in":
			limit = maxBackgroundUploadBytes
		case r.URL.Path == "/admin/appearance/favicon":
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
			Chrome:  chrome{Title: "not found", Auth: true},
			Heading: "nothing here.",
			Message: "this page does not exist, or it was removed.",
		})
	case errors.As(err, &tooLarge):
		s.render(w, r, http.StatusRequestEntityTooLarge, "error.html", errorPage{
			Chrome:  chrome{Title: "too large", Auth: true},
			Heading: "that form is too large.",
			Message: "shorten the text and send it again.",
		})
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		s.render(w, r, http.StatusInternalServerError, "error.html", errorPage{
			Chrome:  chrome{Title: "error", Auth: true},
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
	// Auth marks the pages that centre one card on the screen: sign-in, setup,
	// the public list and errors.
	Auth bool
	// SignInLink puts a sign-in link under the card in place of the link to the status pages.
	SignInLink bool
	// Unbranded drops the "uppies" suffix from the browser title.
	Unbranded bool
	// Nav is the sidebar of the admin pages.
	Nav nav
	// Brand is the instance name shown as the wordmark. Empty means "uppies".
	Brand string
	// AuthLayout and AuthBackground are the sign-in page choices from the
	// instance settings. Empty on the other card pages, which stay plain and centred.
	AuthLayout     string
	AuthBackground string
	// AuthImage is the version of the uploaded background, or 0 when there is none.
	AuthImage int64
	// Favicon is the address of the install's own favicon, empty when it has
	// none, in which case the built-in one is linked. A status page with a
	// favicon of its own links that one after it, and the later link wins.
	Favicon string
	// Description is the page's description for search engines and link
	// previews. Empty means the title is used.
	Description string
}

// nav is the admin sidebar: where you are, and every project with its state.
type nav struct {
	// Section is the sidebar entry of the current page: "overview",
	// "appearance", "instance" or "users". Empty on a status page's own pages.
	Section  string
	Projects []navProject
}

type navProject struct {
	store.Project
	State   string // the worst state among the project's monitors
	Down    int
	Current bool
	// LogoVersion is set when the project has a logo to show as its icon.
	LogoVersion int64
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
	// pair passes two values to a template that takes one.
	"pair": func(first, second any) struct{ First, Second any } {
		return struct{ First, Second any }{first, second}
	},
	"kindIcon": func(kind string) string {
		return kindIcons[kind]
	},
	"channelIcon": func(kind string) string {
		return channelIcons[kind]
	},
	"displayLabel": func(display string) string {
		return displayLabels[display]
	},
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
