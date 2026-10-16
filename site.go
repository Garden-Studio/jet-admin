// Package jetadmin is a Django-admin-style web admin for PostgreSQL tables
// described by go-jet/jet generated code. Register a page per table, mount
// Site.Handler, and the admin lists, searches, filters, adds, changes and
// deletes rows with Jet statements run through pgx.
//
// Pages are rendered with templ and work without JavaScript; Datastar,
// embedded and served by the Site, makes the search live, the filters and
// forms answer without a full reload, and confirmations open as dialogs.
package jetadmin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/garden-studio/jet-admin/internal/core"
)

// DB is what the admin runs statements on; *pgxpool.Pool satisfies it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// querier is a DB or a transaction on it.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// User is the admin signed in on a request.
type User = core.User

// Authenticator tells who is signed in on a request. The auth package has a
// database-backed one; a host with its own sign-in implements it over that.
type Authenticator = core.Authenticator

// LoginProvider is an Authenticator that also serves the sign-in and
// sign-out pages, mounted at BasePath/login and BasePath/logout.
type LoginProvider = core.LoginProvider

// Config is the Site's setup.
type Config struct {
	// DB runs every statement; a *pgxpool.Pool.
	DB DB
	// Title is the name in the header and the page titles: "Sumi admin".
	Title string
	// BasePath is where the Handler is mounted, without the trailing
	// slash: "/admin". The admin's pages live under BasePath + "/".
	BasePath string
	// Auth tells who is signed in. Required: the admin shows nothing to
	// anyone it does not authenticate.
	Auth Authenticator
	// Logger receives the failures the pages cannot show; slog.Default()
	// when nil.
	Logger *slog.Logger
	// AuditLog records every add, change and delete in admin_log (see
	// Schema) and shows them on the index, history and log pages.
	AuditLog bool
	// Stylesheets are linked on every page after the admin's own, in
	// order: the way to theme it by overriding its CSS variables (see the
	// README, Styling). Absolute paths or URLs.
	Stylesheets []string
}

// Site is an admin: its registered models and the Handler serving them.
type Site struct {
	db       DB
	title    string
	base     string
	auth     Authenticator
	logger   *slog.Logger
	auditLog bool

	mu     sync.RWMutex
	models map[string]*modelPage
	order  []string

	catalogMu sync.Mutex
	catalog   *catalog
	styles    []string
}

// New makes a Site. It fails without a DB or an Authenticator.
func New(cfg Config) (*Site, error) {
	if cfg.DB == nil {
		return nil, errors.New("jetadmin: Config.DB is required")
	}
	if cfg.Auth == nil {
		return nil, errors.New("jetadmin: Config.Auth is required")
	}
	base := strings.TrimRight(cfg.BasePath, "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		return nil, errors.New(`jetadmin: Config.BasePath must start with "/"`)
	}
	title := cfg.Title
	if title == "" {
		title = "Admin"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Site{
		db:       cfg.DB,
		title:    title,
		base:     base,
		auth:     cfg.Auth,
		logger:   logger,
		auditLog: cfg.AuditLog,
		styles:   cfg.Stylesheets,
		models:   map[string]*modelPage{},
	}, nil
}

// BasePath is the path the Site is mounted at, without the trailing slash.
func (s *Site) BasePath() string { return s.base }

// DB is the database the Site runs its statements on.
func (s *Site) DB() DB { return s.db }

// reservedSlugs are the Site's own pages.
var reservedSlugs = map[string]bool{"log": true, "login": true, "logout": true, "static": true}

func (s *Site) model(slug string) (*modelPage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.models[slug]
	return m, ok
}

// modelsInOrder are the registered models in the order of registration.
func (s *Site) modelsInOrder() []*modelPage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*modelPage, 0, len(s.order))
	for _, slug := range s.order {
		out = append(out, s.models[slug])
	}
	return out
}

// Handler serves every page under BasePath. Mount it at BasePath + "/":
//
//	mux.Handle("/admin/", site.Handler())
//
// It refuses cross-origin form posts (http.CrossOriginProtection), sends
// whoever is not signed in to the login page (or answers 403 when the
// Authenticator serves none) and serves the embedded stylesheet and
// Datastar under BasePath/static/.
func (s *Site) Handler() http.Handler {
	mux := http.NewServeMux()
	b := s.base
	if p, ok := s.auth.(LoginProvider); ok {
		mux.Handle(b+"/login", p.LoginHandler())
		mux.Handle(b+"/logout", p.LogoutHandler())
	}
	mux.Handle("GET "+b+"/{$}", s.signedIn(s.index))
	mux.Handle("GET "+b+"/log", s.signedIn(s.logPage))
	mux.Handle("GET "+b+"/{slug}/{$}", s.signedIn(s.withModel(s.list)))
	mux.Handle("POST "+b+"/{slug}/{$}", s.signedIn(s.withModel(s.bulkDelete)))
	mux.Handle("GET "+b+"/{slug}/add", s.signedIn(s.withModel(s.addForm)))
	mux.Handle("POST "+b+"/{slug}/add", s.signedIn(s.withModel(s.addSubmit)))
	mux.Handle("GET "+b+"/{slug}/{pk}/{$}", s.signedIn(s.withModel(s.changeForm)))
	mux.Handle("POST "+b+"/{slug}/{pk}/{$}", s.signedIn(s.withModel(s.changeSubmit)))
	mux.Handle("GET "+b+"/{slug}/{pk}/delete", s.signedIn(s.withModel(s.deleteConfirm)))
	mux.Handle("POST "+b+"/{slug}/{pk}/delete", s.signedIn(s.withModel(s.deleteSubmit)))
	mux.Handle("GET "+b+"/{slug}/{pk}/history", s.signedIn(s.withModel(s.history)))
	if b != "" {
		// BasePath alone is the index too, by a redirect, as Django does.
		mux.Handle("GET "+b, http.RedirectHandler(b+"/", http.StatusMovedPermanently))
	}
	mux.Handle("/", s.signedIn(s.notFound))

	protected := http.NewCrossOriginProtection().Handler(mux)
	static := s.staticFiles()
	chrome := core.Chrome{Title: s.title, BasePath: s.base, Stylesheets: s.styles}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The static files are routed here rather than by the mux, where
		// BasePath/static/ would overlap BasePath/{slug}/.
		if strings.HasPrefix(r.URL.Path, b+"/static/") {
			static.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r.WithContext(core.WithChrome(r.Context(), chrome)))
	})
}

// handler is a page of a signed-in user.
type handler func(w http.ResponseWriter, r *http.Request, u User)

// modelHandler is a page of one model.
type modelHandler func(w http.ResponseWriter, r *http.Request, u User, m *modelPage)

func (s *Site) signedIn(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.auth.Authenticate(r)
		if !ok {
			s.refuse(w, r)
			return
		}
		if err := s.ensureCatalog(r.Context()); err != nil {
			// The pages still work without it: jsonb text fields are
			// plain text and blank keys are left to the database.
			s.logger.WarnContext(r.Context(), "jetadmin: read the column catalog", "error", err)
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r, user)
	})
}

func (s *Site) withModel(h modelHandler) handler {
	return func(w http.ResponseWriter, r *http.Request, u User) {
		m, ok := s.model(r.PathValue("slug"))
		if !ok {
			s.notFound(w, r, u)
			return
		}
		h(w, r, u, m)
	}
}

// refuse answers a request nobody is signed in on: the login page when
// there is one, coming back afterwards, else 403.
func (s *Site) refuse(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth.(LoginProvider); !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	location := s.base + "/login"
	if r.Method == http.MethodGet && r.URL.Path != s.base+"/" {
		location += "?next=" + urlQueryEscape(r.URL.RequestURI())
	}
	s.redirect(w, r, location)
}
