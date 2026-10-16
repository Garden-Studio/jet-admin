package auth_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/auth"
	"github.com/garden-studio/jet-admin/internal/testdb"
)

// hostAccounts is a host's own sign-in: one admin, one member who is no
// admin, and a session cookie of its own.
type hostAccounts struct {
	signedOut int
}

const hostCookie = "site_session"

func (*hostAccounts) Authenticate(r *http.Request) (auth.User, bool) {
	c, err := r.Cookie(hostCookie)
	if err != nil || c.Value != "admin@example.com" {
		return auth.User{}, false
	}
	return auth.User{ID: uuid.New(), Username: c.Value}, true
}

func (*hostAccounts) SignIn(w http.ResponseWriter, _ *http.Request, username, password string) error {
	switch {
	case username == "broken@example.com":
		return errors.New("the database is down")
	case password != goodPassword:
		return &auth.Refusal{Message: "Wrong email or password."}
	case username != "admin@example.com":
		return &auth.Refusal{Status: http.StatusForbidden, Message: "Only administrators may sign in here."}
	}
	http.SetCookie(w, &http.Cookie{Name: hostCookie, Value: username, Path: "/"})
	return nil
}

func (a *hostAccounts) SignOut(w http.ResponseWriter, _ *http.Request) error {
	a.signedOut++
	http.SetCookie(w, &http.Cookie{Name: hostCookie, Path: "/", MaxAge: -1})
	return nil
}

func TestHostLogin(t *testing.T) {
	pool := testdb.New(t, jetadmin.Schema)
	accounts := &hostAccounts{}
	site, err := jetadmin.New(jetadmin.Config{
		DB:     pool,
		Auth:   auth.Host(accounts, auth.HostOptions{UsernameLabel: "Email", Logger: slog.New(slog.DiscardHandler)}),
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := site.Handler()
	serve := func(method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := serve(http.MethodGet, "/admin/", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
		t.Fatalf("signed out: %d to %q", w.Code, w.Header().Get("Location"))
	}
	page := serve(http.MethodGet, "/admin/login", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Email") {
		t.Fatalf("login page: %d, the field is not labelled Email", page.Code)
	}

	refused := []struct {
		name, username, password string
		status                   int
		message                  string
	}{
		{"a wrong password", "admin@example.com", "wrong", http.StatusUnauthorized, "Wrong email or password."},
		{"a member", "member@example.com", goodPassword, http.StatusForbidden, "Only administrators may sign in here."},
		{"a failure", "broken@example.com", goodPassword, http.StatusInternalServerError, "The error is in the server&#39;s log."},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			w := serve(http.MethodPost, "/admin/login", url.Values{"username": {tt.username}, "password": {tt.password}})
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.message) {
				t.Errorf("status %d, want %d with %q", w.Code, tt.status, tt.message)
			}
			if len(w.Result().Cookies()) != 0 {
				t.Error("a refused sign-in set a cookie")
			}
		})
	}

	w := serve(http.MethodPost, "/admin/login", url.Values{"username": {"admin@example.com"}, "password": {goodPassword}, "next": {"/admin/log"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/log" {
		t.Fatalf("sign-in: %d to %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != hostCookie {
		t.Fatalf("sign-in set %v, want the host's cookie only", cookies)
	}
	if w := serve(http.MethodGet, "/admin/", nil, cookies[0]); w.Code != http.StatusOK {
		t.Fatalf("signed in: %d", w.Code)
	}

	out := serve(http.MethodPost, "/admin/logout", url.Values{}, cookies[0])
	if out.Code != http.StatusSeeOther || out.Header().Get("Location") != "/admin/login" || accounts.signedOut != 1 {
		t.Fatalf("logout: %d to %q, %d sign-outs", out.Code, out.Header().Get("Location"), accounts.signedOut)
	}
}
