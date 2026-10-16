// Package auth is a database-backed sign-in for jet-admin, like Django's
// superusers: admin_users holds who may sign in (bcrypt password hashes),
// admin_sessions the open sessions, keyed by the SHA-256 of the random
// token in the cookie. jetadmin.Schema creates both tables.
//
//	a := auth.New(pool, auth.Options{Secure: true})
//	site, err := jetadmin.New(jetadmin.Config{DB: pool, Auth: a, BasePath: "/admin"})
//	err = auth.Register(site) // the admin users, in the admin itself
//
// The host's command line creates the first user with CreateUser.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/garden-studio/jet-admin/internal/core"
	"github.com/garden-studio/jet-admin/internal/db/jet/table"
)

// User is the admin signed in on a request.
type User = core.User

// Authenticator tells who is signed in on a request.
type Authenticator = core.Authenticator

// LoginProvider is an Authenticator that also serves the sign-in and
// sign-out pages.
type LoginProvider = core.LoginProvider

// DB is what the package runs its statements on; *pgxpool.Pool and
// jetadmin.DB satisfy it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options are the session cookie's settings.
type Options struct {
	// CookieName is the session cookie's name; "jetadmin_session" by
	// default.
	CookieName string
	// Secure sends the cookie over HTTPS only. Set it in production.
	Secure bool
	// SessionTTL is how long a session lasts from sign-in; 14 days by
	// default.
	SessionTTL time.Duration
	// ClientIP is the address failed sign-ins are counted against; the
	// connection's by default. Behind a proxy, return the client's.
	ClientIP func(*http.Request) string
	// Logger receives the failures the pages cannot show.
	Logger *slog.Logger
}

// Auth signs admins in against admin_users. It is a jetadmin
// LoginProvider: the Site mounts its login and logout pages.
type Auth struct {
	db       DB
	cookie   string
	secure   bool
	ttl      time.Duration
	clientIP func(*http.Request) string
	logger   *slog.Logger
	backoff  *backoff
}

// New makes an Auth over db.
func New(db DB, opts Options) *Auth {
	a := &Auth{
		db:       db,
		cookie:   opts.CookieName,
		secure:   opts.Secure,
		ttl:      opts.SessionTTL,
		clientIP: opts.ClientIP,
		logger:   opts.Logger,
		backoff:  newBackoff(),
	}
	if a.cookie == "" {
		a.cookie = "jetadmin_session"
	}
	if a.ttl <= 0 {
		a.ttl = 14 * 24 * time.Hour
	}
	if a.clientIP == nil {
		a.clientIP = remoteIP
	}
	if a.logger == nil {
		a.logger = slog.Default()
	}
	return a
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// The tables, named without a schema so they are found on the host's
// search_path.
var (
	users    = table.AdminUsers.FromSchema("")
	sessions = table.AdminSessions.FromSchema("")
)

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Authenticate is the active user whose unexpired session the request's
// cookie names.
func (a *Auth) Authenticate(r *http.Request) (User, bool) {
	c, err := r.Cookie(a.cookie)
	if err != nil || c.Value == "" {
		return User{}, false
	}
	stmt := pg.SELECT(users.ID, users.Username).
		FROM(sessions.INNER_JOIN(users, users.ID.EQ(sessions.UserID))).
		WHERE(pg.AND(
			sessions.TokenHash.EQ(pg.Bytea(tokenHash(c.Value))),
			sessions.ExpiresAt.GT(pg.NOW()),
			users.IsActive.IS_TRUE(),
		))
	query, args := stmt.Sql()
	var u User
	if err := a.db.QueryRow(r.Context(), query, args...).Scan(&u.ID, &u.Username); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logger.ErrorContext(r.Context(), "jetadmin/auth: read the session", "error", err)
		}
		return User{}, false
	}
	return u, true
}

// NewSession opens a session for the user and returns its cookie, for the
// login page and for a host that signs admins in some other way (a test,
// a one-time link). path is where the admin is mounted plus "/".
func (a *Auth) NewSession(ctx context.Context, userID uuid.UUID, path string) (*http.Cookie, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(a.ttl)
	stmt := sessions.INSERT(sessions.TokenHash, sessions.UserID, sessions.ExpiresAt).
		VALUES(pg.Bytea(tokenHash(token)), pg.UUID(userID), pg.TimestampzT(expires))
	query, args := stmt.Sql()
	if _, err := a.db.Exec(ctx, query, args...); err != nil {
		return nil, fmt.Errorf("open a session: %w", err)
	}
	if path == "" {
		path = "/"
	}
	return &http.Cookie{
		Name:     a.cookie,
		Value:    token,
		Path:     path,
		Expires:  expires,
		MaxAge:   int(a.ttl / time.Second),
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	}, nil
}

// endSession deletes the request's session, if any.
func (a *Auth) endSession(r *http.Request) error {
	c, err := r.Cookie(a.cookie)
	if err != nil || c.Value == "" {
		return nil
	}
	stmt := sessions.DELETE().WHERE(sessions.TokenHash.EQ(pg.Bytea(tokenHash(c.Value))))
	query, args := stmt.Sql()
	_, err = a.db.Exec(r.Context(), query, args...)
	return err
}

func (a *Auth) clearCookie(w http.ResponseWriter, path string) {
	http.SetCookie(w, &http.Cookie{Name: a.cookie, Path: path, MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

// pruneSessions deletes the expired sessions; run on each sign-in, it
// keeps the table to the sessions that can still be used.
func (a *Auth) pruneSessions(ctx context.Context) {
	stmt := sessions.DELETE().WHERE(sessions.ExpiresAt.LT_EQ(pg.NOW()))
	query, args := stmt.Sql()
	if _, err := a.db.Exec(ctx, query, args...); err != nil {
		a.logger.WarnContext(ctx, "jetadmin/auth: delete expired sessions", "error", err)
	}
}
