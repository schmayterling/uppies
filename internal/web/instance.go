package web

import (
	"errors"
	"io"
	"net/http"
	"strings"

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
}

func (s *Server) instancePage(r *http.Request, title string) (instancePage, error) {
	page := instancePage{Chrome: s.adminChrome(r, title), Themes: themes, Swatches: accentSwatches}
	var err error
	page.Instance, err = s.store.Instance(r.Context())
	page.Saved = r.URL.Query().Get("saved") == "1"
	return page, err
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
	email := "not set up. start uppies with -smtp-addr and -smtp-from."
	if s.mailer != nil {
		email = "sent through " + s.mailer.Addr + " as " + s.mailer.From
	}
	page.Facts = []figure{{Label: "email alerts", Value: email}}
	s.render(w, r, http.StatusOK, "instance.html", page)
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
	if page.Instance.Name == "" || len(page.Instance.Name) > 60 {
		page.Problems = map[string]string{"name": "give the instance a name of 1 to 60 characters."}
		s.render(w, r, http.StatusUnprocessableEntity, "instance.html", page)
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
