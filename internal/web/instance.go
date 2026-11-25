package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"uppies/internal/store"
)

// instancePage is the data of appearance.html and instance.html.
type instancePage struct {
	Chrome   chrome
	Instance store.InstanceSettings
	Themes   []string
	Swatches []int
	// Facts are read-only lines about this install.
	Facts    []figure
	Problems map[string]string
	Saved    bool
	// Layouts and Backgrounds are the sign-in page choices.
	Layouts     []string
	Backgrounds []string
	// ListLayouts are the layouts of the public list of status pages.
	ListLayouts        []string
	AnnouncementColors []string
	// GraphFills are the ways a graph can be filled.
	GraphFills []string
	// BackgroundVersion is set when a sign-in background image is uploaded,
	// FaviconVersion when the install has a favicon, and LogoVersion when it
	// has a logo.
	BackgroundVersion int64
	FaviconVersion    int64
	LogoVersion       int64
	// ZoneName is the install's default timezone, and Zones the choices for it.
	ZoneName string
	Zones    []string
}

func (s *Server) instancePage(r *http.Request, title string) (instancePage, error) {
	page := instancePage{Chrome: s.adminChrome(r, title), Themes: themes, Swatches: accentSwatches}
	var err error
	page.Layouts, page.Backgrounds, page.ListLayouts = store.LoginLayouts, store.LoginBackgrounds, store.ListLayouts
	page.GraphFills = store.GraphFills
	page.AnnouncementColors = announcementColors
	page.Saved = r.URL.Query().Get("saved") == "1"
	page.Instance, err = s.store.Instance(r.Context())
	if err != nil {
		return page, err
	}
	page.BackgroundVersion, err = s.store.InstanceImageVersion(r.Context(), store.ImageLoginBackground)
	if err != nil {
		return page, err
	}
	page.FaviconVersion, err = s.store.InstanceImageVersion(r.Context(), store.ImageInstanceFavicon)
	if err != nil {
		return page, err
	}
	page.LogoVersion, err = s.store.InstanceImageVersion(r.Context(), store.ImageInstanceLogo)
	return page, err
}

// faviconAddress returns where the install's favicon is served, with its
// version so that a new upload shows at once, or "" when there is none. A
// page still has to open when this cannot be read, so a failure is logged.
func (s *Server) faviconAddress(ctx context.Context) string {
	version, err := s.store.InstanceImageVersion(ctx, store.ImageInstanceFavicon)
	if err != nil {
		s.log.Error("read instance favicon", "error", err)
	}
	if version == 0 {
		return ""
	}
	return fmt.Sprintf("/favicon.ico?v=%d", version)
}

func (s *Server) projectFaviconAddress(ctx context.Context, slug string, images map[string]int64) string {
	if version := images[store.ImageFavicon]; version != 0 {
		return fmt.Sprintf("/s/%s/images/favicon?v=%d", slug, version)
	}
	return s.faviconAddress(ctx)
}

func (s *Server) handleSaveFavicon(w http.ResponseWriter, r *http.Request) {
	s.saveInstanceImage(w, r, store.ImageInstanceFavicon)
}

func (s *Server) handleSaveLogo(w http.ResponseWriter, r *http.Request) {
	s.saveInstanceImage(w, r, store.ImageInstanceLogo)
}

// saveInstanceImage stores or removes the install's favicon or logo from the
// appearance form whose file field and remove box are named after the kind.
func (s *Server) saveInstanceImage(w http.ResponseWriter, r *http.Request, kind string) {
	page, err := s.instancePage(r, "appearance")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		s.fail(w, r, err)
		return
	}
	data, contentType, problem, err := readUpload(r, kind, store.MaxImageBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if problem != "" {
		page.Problems = map[string]string{kind: problem}
		s.render(w, r, http.StatusUnprocessableEntity, "appearance.html", page)
		return
	}
	if data != nil {
		err = s.store.SetInstanceImage(r.Context(), kind, contentType, data, s.now().UnixMilli())
	} else if r.PostForm.Get("remove_"+kind) == "on" {
		err = s.store.DeleteInstanceImage(r.Context(), kind)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/appearance?saved=1", http.StatusSeeOther)
}

// handleInstanceFavicon serves the install's favicon. Browsers ask for this
// address on their own, so a status page without a favicon gets it too.
func (s *Server) handleInstanceFavicon(w http.ResponseWriter, r *http.Request) {
	contentType, data, err := s.store.InstanceImage(r.Context(), store.ImageInstanceFavicon)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeImage(w, r, contentType, data)
}

// logoVersion returns the version of the install's logo, or 0 when there is
// none. A page still opens without its logo, so a failed read is only logged.
func (s *Server) logoVersion(ctx context.Context) int64 {
	version, err := s.store.InstanceImageVersion(ctx, store.ImageInstanceLogo)
	if err != nil {
		s.log.Error("read instance logo", "error", err)
	}
	return version
}

// handleInstanceLogo serves the logo that the public list shows beside the site name.
func (s *Server) handleInstanceLogo(w http.ResponseWriter, r *http.Request) {
	contentType, data, err := s.store.InstanceImage(r.Context(), store.ImageInstanceLogo)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeImage(w, r, contentType, data)
}

// maxBackgroundUploadBytes caps the sign-in page form: one image at the size limit plus form overhead.
const maxBackgroundUploadBytes = store.MaxBackgroundBytes + 64<<10

func (s *Server) handleSaveSignInPage(w http.ResponseWriter, r *http.Request) {
	page, err := s.instancePage(r, "appearance")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseMultipartForm(maxBackgroundUploadBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			s.fail(w, r, err)
			return
		}
		page.Problems = map[string]string{"background": "the file is larger than 2048 KB."}
		s.render(w, r, http.StatusUnprocessableEntity, "appearance.html", page)
		return
	}
	problems := make(map[string]string)
	page.Instance.LoginLayout = choice(r.PostForm, "login_layout", store.LoginLayouts, page.Instance.LoginLayout, problems)
	page.Instance.LoginBackground = choice(r.PostForm, "login_background", store.LoginBackgrounds, page.Instance.LoginBackground, problems)
	data, contentType, problem, err := readUpload(r, "background", store.MaxBackgroundBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	remove := data == nil && r.PostForm.Get("remove_background") == "on"
	hasImage := data != nil || (page.BackgroundVersion != 0 && !remove)
	switch {
	case problem != "":
		problems["background"] = problem
	case page.Instance.LoginBackground == "image" && !hasImage:
		problems["background"] = "choose an image to upload, or pick another background."
	}
	if len(problems) > 0 {
		page.Problems = problems
		s.render(w, r, http.StatusUnprocessableEntity, "appearance.html", page)
		return
	}
	if data != nil {
		err = s.store.SetInstanceImage(r.Context(), store.ImageLoginBackground, contentType, data, s.now().UnixMilli())
	} else if remove {
		err = s.store.DeleteInstanceImage(r.Context(), store.ImageLoginBackground)
	}
	if err == nil {
		err = s.store.SaveInstance(r.Context(), page.Instance)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/appearance?saved=1", http.StatusSeeOther)
}

// handleSignInBackground serves the uploaded background of the sign-in page.
// It is public, because the page that shows it is.
func (s *Server) handleSignInBackground(w http.ResponseWriter, r *http.Request) {
	contentType, data, err := s.store.InstanceImage(r.Context(), store.ImageLoginBackground)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeImage(w, r, contentType, data)
}

// handleInstanceTheme serves the admin's look as a stylesheet, for the same
// reason a project's look is one: the page allows no inline styles.
func (s *Server) handleInstanceTheme(w http.ResponseWriter, r *http.Request) {
	instance, err := s.store.Instance(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	css := themeCSS(store.Project{Theme: instance.Theme, AccentHue: instance.AccentHue})
	if _, err := io.WriteString(w, css); err != nil {
		s.log.Error("write instance theme", "error", err)
	}
}

func (s *Server) handleAppearance(w http.ResponseWriter, r *http.Request) {
	page, err := s.instancePage(r, "appearance")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "appearance.html", page)
}

func (s *Server) handleSaveAppearance(w http.ResponseWriter, r *http.Request) {
	page, err := s.instancePage(r, "appearance")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	problems := make(map[string]string)
	page.Instance.Theme = choice(r.PostForm, "theme", themes, page.Instance.Theme, problems)
	page.Instance.AccentHue = formInt(r.PostForm, "accent_hue", 0, 360, problems)
	page.Instance.ListLayout = choice(r.PostForm, "list_layout", store.ListLayouts, page.Instance.ListLayout, problems)
	page.Instance.GraphFill = choice(r.PostForm, "graph_fill", store.GraphFills, page.Instance.GraphFill, problems)
	if len(problems) > 0 {
		page.Problems = problems
		s.render(w, r, http.StatusUnprocessableEntity, "appearance.html", page)
		return
	}
	if err := s.store.SaveInstance(r.Context(), page.Instance); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/appearance?saved=1", http.StatusSeeOther)
}

func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	page, err := s.instancePage(r, "instance")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderInstance(w, r, http.StatusOK, page)
}

func (s *Server) renderInstance(w http.ResponseWriter, r *http.Request, status int, page instancePage) {
	facts, err := s.store.InstanceFacts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	email := "not set up. start uppies with -smtp-addr and -smtp-from."
	if s.mailer != nil {
		email = "sent through " + s.mailer.Addr + " as " + s.mailer.From
	}
	uptime := max(0, s.now().Sub(s.startedAt)).Truncate(time.Second)
	page.Facts = []figure{
		{Label: "server uptime", Value: uptime.String()},
		{Label: "server timezone", Value: systemZoneName()},
		{Label: "runtime", Value: runtime.Version() + " · " + runtime.GOOS + "/" + runtime.GOARCH},
		{Label: "database", Value: fmt.Sprintf("%s · schema %d", s.store.Backend(), facts.Schema)},
		{Label: "status pages", Value: strconv.Itoa(facts.StatusPages)},
		{Label: "monitors", Value: fmt.Sprintf("%d total, %d paused", facts.Monitors, facts.Paused)},
		{Label: "alert channels", Value: strconv.Itoa(facts.Channels)},
		{Label: "admin accounts", Value: strconv.Itoa(facts.Users)},
		{Label: "email alerts", Value: email},
	}
	page.ZoneName = instanceZone(page.Instance).Name
	page.Zones = zoneChoices(page.ZoneName, page.ZoneName)
	s.render(w, r, status, "instance.html", page)
}

func (s *Server) handleSaveInstance(w http.ResponseWriter, r *http.Request) {
	page, err := s.instancePage(r, "instance")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	page.Instance.Name = strings.TrimSpace(r.PostForm.Get("name"))
	page.Instance.PublicList = r.PostForm.Get("public_list") == "on"
	page.Problems = make(map[string]string)
	if page.Instance.Name == "" || len(page.Instance.Name) > 60 {
		page.Problems["name"] = "give the site a name of 1 to 60 characters."
	}
	if r.PostForm.Has("public_url") {
		page.Instance.PublicURL = strings.TrimSpace(r.PostForm.Get("public_url"))
		if page.Instance.PublicURL != "" {
			origin, ok := parsePublicOrigin(page.Instance.PublicURL)
			if !ok || len(page.Instance.PublicURL) > 2048 {
				page.Problems["public_url"] = "enter an http or https origin without a path, such as https://status.example.com."
			} else {
				page.Instance.PublicURL = origin.String()
			}
		}
	}
	page.Instance.ListAnnouncement = announcementText(r.PostForm, "list_announcement", page.Instance.ListAnnouncement, page.Problems)
	page.Instance.ListAnnouncementColor = choice(r.PostForm, "list_announcement_color", announcementColors, page.Instance.ListAnnouncementColor, page.Problems)
	if r.PostForm.Has("list_footer_text") {
		footer := strings.TrimSpace(r.PostForm.Get("list_footer_text"))
		if !utf8.ValidString(footer) || apiText("list_footer_text", footer, 0, 500) != nil {
			page.Problems["list_footer_text"] = "use up to 500 characters."
		}
		page.Instance.ListFooterText = footer
	}
	if r.PostForm.Has("list_show_powered_by") {
		page.Instance.ListShowPoweredBy = r.PostForm.Get("list_show_powered_by") == "on"
	}
	if r.PostForm.Has("timezone") {
		name := strings.TrimSpace(r.PostForm.Get("timezone"))
		if _, ok := loadZone(name); ok {
			page.Instance.Timezone = name
		} else {
			page.Problems["timezone"] = "choose a timezone."
		}
	}
	if r.PostForm.Has("nav_links") {
		page.Instance.NavLinks = strings.TrimSpace(r.PostForm.Get("nav_links"))
		if len(page.Instance.NavLinks) > 2000 {
			page.Problems["nav_links"] = "keep the navbar links to 2000 characters."
		} else if _, problem := parseNavLinks(page.Instance.NavLinks); problem != "" {
			page.Problems["nav_links"] = problem
		}
	}
	if len(page.Problems) > 0 {
		s.renderInstance(w, r, http.StatusUnprocessableEntity, page)
		return
	}
	if err := s.store.SaveInstance(r.Context(), page.Instance); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/instance?saved=1", http.StatusSeeOther)
}

type usersPage struct {
	Chrome   chrome
	Users    []store.User
	Me       int64
	Username string
	Problems map[string]string
	Notice   string
}

func (s *Server) usersPage(r *http.Request) (usersPage, error) {
	page := usersPage{Chrome: s.adminChrome(r, "users")}
	var err error
	page.Me, _, _, err = s.sessionUser(r)
	if err != nil {
		return page, err
	}
	page.Users, err = s.store.Users(r.Context())
	notices := map[string]string{"added": "the account was added.", "removed": "the account was removed.", "password": "your password was changed. other devices were signed out."}
	page.Notice = notices[r.URL.Query().Get("done")]
	return page, err
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	page, err := s.usersPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "users.html", page)
}

// renderUserProblems shows the users page again with what was wrong.
func (s *Server) renderUserProblems(w http.ResponseWriter, r *http.Request, username string, problems map[string]string) {
	page, err := s.usersPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page.Username, page.Problems = username, problems
	s.render(w, r, http.StatusUnprocessableEntity, "users.html", page)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	password := r.PostForm.Get("password")
	problems := make(map[string]string)
	if username == "" || len(username) > 64 {
		problems["username"] = "choose a username of 1 to 64 characters."
	}
	if len(password) < minPasswordLen {
		problems["password"] = "choose a password of at least 10 characters."
	}
	if len(problems) == 0 {
		err := s.store.CreateUser(r.Context(), username, hashPassword(password), s.now().UnixMilli())
		if errors.Is(err, store.ErrUsernameTaken) {
			problems["username"] = "another account already has this username."
		} else if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if len(problems) > 0 {
		s.renderUserProblems(w, r, username, problems)
		return
	}
	http.Redirect(w, r, "/admin/users?done=added", http.StatusSeeOther)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID, tokenHash, _, err := s.sessionUser(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	hash, err := s.store.PasswordHashByID(r.Context(), userID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	matches, err := verifyPassword(hash, r.PostForm.Get("current"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	problems := make(map[string]string)
	if !matches {
		problems["current"] = "that is not your current password."
	}
	if len(r.PostForm.Get("new")) < minPasswordLen {
		problems["new"] = "choose a password of at least 10 characters."
	}
	if len(problems) > 0 {
		s.renderUserProblems(w, r, "", problems)
		return
	}
	if err := s.store.SetPassword(r.Context(), userID, hashPassword(r.PostForm.Get("new")), tokenHash); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/users?done=password", http.StatusSeeOther)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	me, _, _, err := s.sessionUser(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// You cannot remove the account you are signed in with, and the last one always stays.
	if id == me {
		s.renderUserProblems(w, r, "", map[string]string{"delete": "you cannot remove your own account. sign in as someone else to do that."})
		return
	}
	err = s.store.DeleteUser(r.Context(), id)
	if errors.Is(err, store.ErrLastUser) {
		s.renderUserProblems(w, r, "", map[string]string{"delete": "the last account cannot be removed."})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/users?done=removed", http.StatusSeeOther)
}
