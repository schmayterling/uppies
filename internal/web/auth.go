package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"uppies/internal/store"
)

const (
	sessionCookie   = "uppies_session"
	sessionLifetime = 30 * 24 * time.Hour
	minPasswordLen  = 10

	maxLoginFailures   = 5
	loginFailureWindow = 5 * time.Minute
	// maxTrackedClients bounds the limiter's memory. Past it, old entries are dropped.
	maxTrackedClients = 10_000
)

// loginLimiter slows password guessing: after maxLoginFailures failed
// sign-ins from one address, that address waits out the window.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string][]time.Time)}
}

// blocked reports whether client has used up its attempts.
func (l *loginLimiter) blocked(client string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(client, now)) >= maxLoginFailures
}

func (l *loginLimiter) recordFailure(client string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) >= maxTrackedClients {
		clear(l.failures)
	}
	l.failures[client] = append(l.recent(client, now), now)
}

// recent returns client's failures inside the window. The caller holds mu.
func (l *loginLimiter) recent(client string, now time.Time) []time.Time {
	var kept []time.Time
	for _, at := range l.failures[client] {
		if now.Sub(at) < loginFailureWindow {
			kept = append(kept, at)
		}
	}
	return kept
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// signedIn reports whether the request carries a live session.
func (s *Server) signedIn(r *http.Request) (bool, error) {
	_, _, ok, err := s.sessionUser(r)
	return ok, err
}

// sessionUser returns the account behind the request's session and the hash
// of its session token. ok is false when there is no live session.
func (s *Server) sessionUser(r *http.Request) (userID int64, tokenHash string, ok bool, err error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return 0, "", false, nil
	}
	tokenHash = hashToken(cookie.Value)
	userID, err = s.store.SessionUser(r.Context(), tokenHash, s.now().UnixMilli())
	if errors.Is(err, store.ErrNotFound) {
		return 0, "", false, nil
	}
	return userID, tokenHash, err == nil, err
}

// requireAdmin lets signed-in requests through and sends the rest to sign in.
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.signedIn(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	})
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int64) error {
	token := rand.Text()
	now := s.now()
	expires := now.Add(sessionLifetime)
	if err := s.store.CreateSession(ctx, hashToken(token), userID, now.UnixMilli(), expires.UnixMilli()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// A reverse proxy that terminates TLS reports the original scheme in this header.
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	return nil
}

// signInChrome is the chrome of the sign-in and setup pages, laid out the way
// the instance settings say. When the settings cannot be read the page still
// has to open, so it falls back to the plain centred card.
func (s *Server) signInChrome(r *http.Request, title string) chrome {
	c := chrome{Title: title, Auth: true, AuthLayout: "centered", AuthBackground: "plain"}
	instance, err := s.store.Instance(r.Context())
	if err != nil {
		s.log.Error("read instance settings", "path", r.URL.Path, "error", err)
		return c
	}
	c.Brand, c.AuthLayout, c.AuthBackground = instance.Name, instance.LoginLayout, instance.LoginBackground
	if c.AuthBackground != "image" {
		return c
	}
	c.AuthImage, err = s.store.InstanceImageVersion(r.Context(), store.ImageLoginBackground)
	if err != nil {
		s.log.Error("read sign-in background", "path", r.URL.Path, "error", err)
	}
	if c.AuthImage == 0 {
		c.AuthBackground = "gradient"
	}
	return c
}

// credentialsPage is the data of setup.html and login.html.
type credentialsPage struct {
	Chrome   chrome
	Username string
	Problem  string
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	exists, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if exists {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "setup.html", credentialsPage{Chrome: s.signInChrome(r, "set up uppies")})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	password := r.PostForm.Get("password")
	page := credentialsPage{Chrome: s.signInChrome(r, "set up uppies"), Username: username}
	switch {
	case username == "" || len(username) > 64:
		page.Problem = "choose a username of 1 to 64 characters."
	case len(password) < minPasswordLen:
		page.Problem = "choose a password of at least 10 characters."
	}
	if page.Problem != "" {
		s.render(w, r, http.StatusUnprocessableEntity, "setup.html", page)
		return
	}

	created, err := s.store.CreateFirstUser(r.Context(), username, hashPassword(password), s.now().UnixMilli())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !created {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	userID, _, err := s.store.UserPasswordHash(r.Context(), username)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(r.Context(), w, r, userID); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	exists, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", credentialsPage{Chrome: s.signInChrome(r, "sign in")})
}

// decoyHash is checked when the username is unknown, so that a wrong username
// takes as long to reject as a wrong password.
var decoyHash = hashPassword("uppies decoy password")

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	page := credentialsPage{Chrome: s.signInChrome(r, "sign in"), Username: username}
	client := clientAddress(r)
	if s.logins.blocked(client, s.now()) {
		page.Problem = "too many failed attempts. wait five minutes and try again."
		w.Header().Set("Retry-After", "300")
		s.render(w, r, http.StatusTooManyRequests, "login.html", page)
		return
	}

	userID, hash, err := s.store.UserPasswordHash(r.Context(), username)
	unknownUser := errors.Is(err, store.ErrNotFound)
	if err != nil && !unknownUser {
		s.fail(w, r, err)
		return
	}
	if unknownUser {
		hash = decoyHash
	}
	matches, err := verifyPassword(hash, r.PostForm.Get("password"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if unknownUser || !matches {
		s.logins.recordFailure(client, s.now())
		page.Problem = "that username and password do not match."
		s.render(w, r, http.StatusUnauthorized, "login.html", page)
		return
	}
	if err := s.startSession(r.Context(), w, r, userID); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.DeleteSession(r.Context(), hashToken(cookie.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
