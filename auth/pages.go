package auth

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/garden-studio/jet-admin/internal/core"
	"github.com/garden-studio/jet-admin/internal/ui"
)

// The login and logout pages, mounted by the Site at BasePath/login and
// BasePath/logout. Both work as plain forms; Datastar shows a failed
// sign-in's message without reloading the page.

// badCredentials is the one message of every refused sign-in, so it never
// tells an unknown username from a wrong password or an inactive user.
const badCredentials = "Please enter a correct username and password. Both are case-sensitive."

const tooManyAttempts = "Too many failed sign-ins. Wait a minute and try again."

// LoginHandler serves the login page and signs in on its post.
func (a *Auth) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chrome := core.ChromeFrom(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next := core.SafeNext(chrome.BasePath, r.URL.Query().Get("next"))
			if _, ok := a.Authenticate(r); ok {
				http.Redirect(w, r, next, http.StatusSeeOther)
				return
			}
			a.renderLogin(w, r, http.StatusOK, ui.LoginPage{Next: next})
		case http.MethodPost:
			a.login(w, r, chrome)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request, chrome core.Chrome) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	password := r.PostForm.Get("password")
	next := core.SafeNext(chrome.BasePath, r.PostForm.Get("next"))
	keys := []string{"ip:" + a.clientIP(r), "user:" + strings.ToLower(username)}
	refuse := func(status int, message string) {
		if r.Header.Get("Datastar-Request") == "true" {
			if err := datastar.NewSSE(w, r).PatchElementTempl(ui.LoginError(message)); err != nil {
				a.logger.DebugContext(r.Context(), "jetadmin/auth: patch the login error", "error", err)
			}
			return
		}
		a.renderLogin(w, r, status, ui.LoginPage{Next: next, Username: username, Error: message})
	}
	if a.backoff.wait(keys...) > 0 {
		refuse(http.StatusTooManyRequests, tooManyAttempts)
		return
	}
	user, ok, err := verify(r.Context(), a.db, username, password)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "jetadmin/auth: check the password", "error", err)
		refuse(http.StatusInternalServerError, "Signing in failed. The error is in the server's log.")
		return
	}
	if !ok {
		a.backoff.fail(keys...)
		refuse(http.StatusUnauthorized, badCredentials)
		return
	}
	a.backoff.succeed(keys...)
	a.pruneSessions(r.Context())
	cookie, err := a.NewSession(r.Context(), user.ID, chrome.BasePath+"/")
	if err != nil {
		a.logger.ErrorContext(r.Context(), "jetadmin/auth: open a session", "error", err)
		refuse(http.StatusInternalServerError, "Signing in failed. The error is in the server's log.")
		return
	}
	if err := touchLastLogin(r.Context(), a.db, user.ID); err != nil {
		a.logger.WarnContext(r.Context(), "jetadmin/auth: record the sign-in", "error", err)
	}
	http.SetCookie(w, cookie)
	if r.Header.Get("Datastar-Request") == "true" {
		_ = datastar.NewSSE(w, r).Redirect(next)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *Auth) renderLogin(w http.ResponseWriter, r *http.Request, status int, page ui.LoginPage) {
	chrome := core.ChromeFrom(r.Context())
	page.Chrome = ui.Chrome{SiteTitle: chrome.Title, Base: chrome.BasePath, PageTitle: "Log in"}
	page.Action = chrome.BasePath + "/login"
	render(w, r, status, ui.Login(page), a)
}

// LogoutHandler asks to log out on a GET and logs out on a POST, so a
// link from another site cannot sign anyone out.
func (a *Auth) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chrome := core.ChromeFrom(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			page := ui.LoginPage{
				Chrome: ui.Chrome{SiteTitle: chrome.Title, Base: chrome.BasePath, PageTitle: "Log out"},
				Action: chrome.BasePath + "/logout",
			}
			render(w, r, http.StatusOK, ui.Logout(page), a)
		case http.MethodPost:
			if err := a.endSession(r); err != nil {
				a.logger.ErrorContext(r.Context(), "jetadmin/auth: end the session", "error", err)
			}
			a.clearCookie(w, chrome.BasePath+"/")
			http.Redirect(w, r, chrome.BasePath+"/login", http.StatusSeeOther)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})
}

func render(w http.ResponseWriter, r *http.Request, status int, page templ.Component, a *Auth) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := page.Render(r.Context(), w); err != nil {
		a.logger.DebugContext(r.Context(), "jetadmin/auth: render", "error", err)
	}
}
