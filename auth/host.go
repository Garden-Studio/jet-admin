package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/starfederation/datastar-go/datastar"

	"github.com/garden-studio/jet-admin/internal/core"
	"github.com/garden-studio/jet-admin/internal/ui"
)

// HostAccounts is a host's own sign-in: its users, password checks and
// sessions. Host serves the admin's login and logout pages over it, so a
// host that already has accounts needs neither admin_users nor
// admin_sessions, and its admins sign in once for the site and the admin.
type HostAccounts interface {
	// Authenticate is the admin signed in on r, by the host's session.
	Authenticate(r *http.Request) (User, bool)
	// SignIn checks the credentials posted to the login page and, when they
	// are an admin's, starts the host's session on w (sets its cookie). A
	// *Refusal is shown on the form; any other error is logged and the form
	// says signing in failed.
	SignIn(w http.ResponseWriter, r *http.Request, username, password string) error
	// SignOut ends the session of r.
	SignOut(w http.ResponseWriter, r *http.Request) error
}

// Refusal is a sign-in HostAccounts.SignIn turned down: Message is shown on
// the login form, answered with Status (401 when zero).
type Refusal struct {
	Status  int
	Message string
}

func (e *Refusal) Error() string { return e.Message }

// HostOptions are Host's settings.
type HostOptions struct {
	// UsernameLabel names the login form's first field; "Username" by
	// default ("Email" for a host whose accounts are emails).
	UsernameLabel string
	// Logger receives the failures the pages cannot show.
	Logger *slog.Logger
}

// Host is a LoginProvider over the host's own accounts: the Site mounts
// the admin's login and logout pages, which hand the credentials to
// accounts.SignIn and the sign-out to accounts.SignOut. Throttling failed
// sign-ins is the host's, as everything else about its accounts.
func Host(accounts HostAccounts, opts HostOptions) LoginProvider {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &host{accounts: accounts, label: opts.UsernameLabel, logger: opts.Logger}
}

type host struct {
	accounts HostAccounts
	label    string
	logger   *slog.Logger
}

func (h *host) Authenticate(r *http.Request) (User, bool) { return h.accounts.Authenticate(r) }

func (h *host) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chrome := core.ChromeFrom(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next := core.SafeNext(chrome.BasePath, r.URL.Query().Get("next"))
			if _, ok := h.accounts.Authenticate(r); ok {
				http.Redirect(w, r, next, http.StatusSeeOther)
				return
			}
			h.renderLogin(w, r, http.StatusOK, ui.LoginPage{Next: next})
		case http.MethodPost:
			h.login(w, r, chrome)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (h *host) login(w http.ResponseWriter, r *http.Request, chrome core.Chrome) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	next := core.SafeNext(chrome.BasePath, r.PostForm.Get("next"))
	err := h.accounts.SignIn(w, r, username, r.PostForm.Get("password"))
	if err != nil {
		status, message := http.StatusInternalServerError, "Signing in failed. The error is in the server's log."
		var refusal *Refusal
		if errors.As(err, &refusal) {
			status, message = refusal.Status, refusal.Message
			if status == 0 {
				status = http.StatusUnauthorized
			}
		} else {
			h.logger.ErrorContext(r.Context(), "jetadmin/auth: sign in", "error", err)
		}
		if r.Header.Get("Datastar-Request") == "true" {
			if err := datastar.NewSSE(w, r).PatchElementTempl(ui.LoginError(message)); err != nil {
				h.logger.DebugContext(r.Context(), "jetadmin/auth: patch the login error", "error", err)
			}
			return
		}
		h.renderLogin(w, r, status, ui.LoginPage{Next: next, Username: username, Error: message})
		return
	}
	if r.Header.Get("Datastar-Request") == "true" {
		_ = datastar.NewSSE(w, r).Redirect(next)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (h *host) renderLogin(w http.ResponseWriter, r *http.Request, status int, page ui.LoginPage) {
	chrome := core.ChromeFrom(r.Context())
	page.Chrome = ui.Chrome{SiteTitle: chrome.Title, Base: chrome.BasePath, Styles: chrome.Stylesheets, PageTitle: "Log in"}
	page.Action = chrome.BasePath + "/login"
	page.UsernameLabel = h.label
	render(w, r, status, ui.Login(page), h.logger)
}

// LogoutHandler asks to log out on a GET and logs out on a POST, as Auth's.
func (h *host) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chrome := core.ChromeFrom(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			page := ui.LoginPage{
				Chrome: ui.Chrome{SiteTitle: chrome.Title, Base: chrome.BasePath, Styles: chrome.Stylesheets, PageTitle: "Log out"},
				Action: chrome.BasePath + "/logout",
			}
			render(w, r, http.StatusOK, ui.Logout(page), h.logger)
		case http.MethodPost:
			if err := h.accounts.SignOut(w, r); err != nil {
				h.logger.ErrorContext(r.Context(), "jetadmin/auth: sign out", "error", err)
			}
			http.Redirect(w, r, chrome.BasePath+"/login", http.StatusSeeOther)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})
}
