package auth_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/auth"
	"github.com/garden-studio/jet-admin/internal/db/jet/table"
	"github.com/garden-studio/jet-admin/internal/testdb"
)

var (
	users    = table.AdminUsers.FromSchema("")
	sessions = table.AdminSessions.FromSchema("")
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	auth *auth.Auth
	h    http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t, jetadmin.Schema)
	a := auth.New(pool, auth.Options{Secure: true, Logger: slog.New(slog.DiscardHandler)})
	site, err := jetadmin.New(jetadmin.Config{DB: pool, Auth: a, BasePath: "/admin", Title: "Auth test", Logger: slog.New(slog.DiscardHandler), AuditLog: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Register(site); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, pool: pool, auth: a, h: site.Handler()}
}

func (f *fixture) serve(method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
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
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) login(username, password, next string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.serve(http.MethodPost, "/admin/login", url.Values{"username": {username}, "password": {password}, "next": {next}})
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "jetadmin_session" {
			return c
		}
	}
	return nil
}

func (f *fixture) exec(stmt pg.Statement) {
	f.t.Helper()
	query, args := stmt.Sql()
	if _, err := f.pool.Exec(context.Background(), query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sessionCount() int64 {
	var n int64
	query, args := pg.SELECT(pg.COUNT(pg.STAR)).FROM(sessions).Sql()
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

const goodPassword = "a long passphrase"

func TestLogin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := auth.CreateUser(ctx, f.pool, "ann", goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CreateUser(ctx, f.pool, "olly", goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := auth.Deactivate(ctx, f.pool, "olly"); err != nil {
		t.Fatal(err)
	}

	t.Run("a page asks to sign in first, coming back after", func(t *testing.T) {
		w := f.serve(http.MethodGet, "/admin/admin_users/?q=a", nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login?next=%2Fadmin%2Fadmin_users%2F%3Fq%3Da" {
			t.Fatalf("status %d to %q", w.Code, w.Header().Get("Location"))
		}
		page := f.serve(http.MethodGet, "/admin/login?next=/admin/admin_users/", nil)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="next" value="/admin/admin_users/"`) {
			t.Fatalf("login page: %d", page.Code)
		}
	})

	refused := []struct {
		name, username, password string
	}{
		{"wrong password", "ann", "not the password"},
		{"unknown user", "nobody", goodPassword},
		{"inactive user", "olly", goodPassword},
		{"empty", "", ""},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			w := f.login(tt.username, tt.password, "/admin/")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status %d", w.Code)
			}
			if !strings.Contains(w.Body.String(), "Please enter a correct username and password.") {
				t.Error("no generic error")
			}
			if sessionCookie(w) != nil {
				t.Error("a refused sign-in set a session cookie")
			}
		})
	}

	t.Run("signed in, sent on to next with a safe cookie", func(t *testing.T) {
		w := f.login("ann", goodPassword, "/admin/admin_users/")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/admin_users/" {
			t.Fatalf("status %d to %q", w.Code, w.Header().Get("Location"))
		}
		c := sessionCookie(w)
		if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/admin/" || c.MaxAge != 14*24*3600 {
			t.Fatalf("cookie %+v", c)
		}
		page := f.serve(http.MethodGet, "/admin/admin_users/", nil, c)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "ann") {
			t.Fatalf("status %d", page.Code)
		}
		var lastLogin *string
		query, args := pg.SELECT(pg.CAST(users.LastLoginAt).AS_TEXT()).FROM(users).WHERE(users.Username.EQ(pg.String("ann"))).Sql()
		if err := f.pool.QueryRow(ctx, query, args...).Scan(&lastLogin); err != nil || lastLogin == nil {
			t.Errorf("last_login_at not set: %v", err)
		}
	})

	t.Run("next never leaves the admin", func(t *testing.T) {
		for _, next := range []string{"https://evil.example/", "//evil.example/", "/elsewhere", `/admin\..\x`} {
			w := f.login("ann", goodPassword, next)
			if got := w.Header().Get("Location"); got != "/admin/" {
				t.Errorf("next %q went to %q", next, got)
			}
		}
	})

	t.Run("an expired session signs nobody in", func(t *testing.T) {
		c := sessionCookie(f.login("ann", goodPassword, "/admin/"))
		f.exec(sessions.UPDATE(sessions.ExpiresAt).SET(pg.NOW().SUB(pg.INTERVAL(1, pg.MINUTE))).WHERE(pg.Bool(true)))
		if w := f.serve(http.MethodGet, "/admin/", nil, c); w.Code != http.StatusSeeOther {
			t.Fatalf("status %d with an expired session", w.Code)
		}
		// The next sign-in prunes the expired ones.
		f.login("ann", goodPassword, "/admin/")
		if n := f.sessionCount(); n != 1 {
			t.Errorf("%d sessions, want only the new one", n)
		}
	})

	t.Run("logout ends the session", func(t *testing.T) {
		c := sessionCookie(f.login("ann", goodPassword, "/admin/"))
		ask := f.serve(http.MethodGet, "/admin/logout", nil, c)
		if ask.Code != http.StatusOK {
			t.Fatalf("logout page: %d", ask.Code)
		}
		if w := f.serve(http.MethodGet, "/admin/", nil, c); w.Code != http.StatusOK {
			t.Fatal("a GET of logout signed out")
		}
		w := f.serve(http.MethodPost, "/admin/logout", url.Values{}, c)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
			t.Fatalf("status %d to %q", w.Code, w.Header().Get("Location"))
		}
		if cleared := sessionCookie(w); cleared == nil || cleared.MaxAge >= 0 {
			t.Errorf("cookie not cleared: %+v", cleared)
		}
		if w := f.serve(http.MethodGet, "/admin/", nil, c); w.Code != http.StatusSeeOther {
			t.Fatalf("status %d after logout", w.Code)
		}
	})

	t.Run("deactivating ends a signed-in session", func(t *testing.T) {
		if _, err := auth.CreateUser(ctx, f.pool, "dee", goodPassword); err != nil {
			t.Fatal(err)
		}
		c := sessionCookie(f.login("dee", goodPassword, "/admin/"))
		if err := auth.Deactivate(ctx, f.pool, "dee"); err != nil {
			t.Fatal(err)
		}
		if w := f.serve(http.MethodGet, "/admin/", nil, c); w.Code != http.StatusSeeOther {
			t.Fatalf("status %d for a deactivated user", w.Code)
		}
	})

	t.Run("Datastar gets the error patched", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(url.Values{"username": {"ann"}, "password": {"nope nope"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Datastar-Request", "true")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		body := w.Body.String()
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(body, `id="login-error"`) || !strings.Contains(body, "Please enter a correct username") {
			t.Fatalf("%q", body)
		}
	})
}

func TestLoginBackoff(t *testing.T) {
	f := newFixture(t)
	if _, err := auth.CreateUser(context.Background(), f.pool, "ann", goodPassword); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if w := f.login("ann", "wrong guess", "/admin/"); w.Code != http.StatusUnauthorized {
			t.Fatalf("status %d", w.Code)
		}
	}
	w := f.login("ann", goodPassword, "/admin/")
	if w.Code != http.StatusTooManyRequests || sessionCookie(w) != nil {
		t.Fatalf("the right password after 5 failures: status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Too many failed sign-ins") {
		t.Error("no backoff message")
	}
}

func TestUsers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := auth.CreateUser(ctx, f.pool, "ann", "short"); err == nil {
		t.Error("a short password was accepted")
	}
	if _, err := auth.CreateUser(ctx, f.pool, "ann", strings.Repeat("x", 73)); err == nil {
		t.Error("a password past 72 bytes was accepted")
	}
	ann, err := auth.CreateUser(ctx, f.pool, " ann ", goodPassword)
	if err != nil || ann.Username != "ann" {
		t.Fatalf("CreateUser: %+v, %v", ann, err)
	}
	if _, err := auth.CreateUser(ctx, f.pool, "ann", goodPassword); err != auth.ErrUsernameTaken {
		t.Errorf("duplicate: %v", err)
	}
	if err := auth.SetPassword(ctx, f.pool, "nobody", goodPassword); err != auth.ErrUserNotFound {
		t.Errorf("SetPassword of nobody: %v", err)
	}
	if err := auth.SetPassword(ctx, f.pool, "ann", "another passphrase"); err != nil {
		t.Fatal(err)
	}
	if w := f.login("ann", goodPassword, "/admin/"); w.Code != http.StatusUnauthorized {
		t.Error("the old password still works")
	}
	c := sessionCookie(f.login("ann", "another passphrase", "/admin/"))
	if c == nil {
		t.Fatal("the new password does not work")
	}

	t.Run("managed in the admin without ever showing a hash", func(t *testing.T) {
		add := f.serve(http.MethodPost, "/admin/admin_users/add", url.Values{
			"username": {"bob"}, "is_active": {"true"}, "password": {"bob's passphrase"}, "password_confirm": {"bob's passphrase"},
		}, c)
		if add.Code != http.StatusSeeOther {
			t.Fatalf("add: %d %s", add.Code, add.Body.String())
		}
		if sessionCookie(f.login("bob", "bob's passphrase", "/admin/")) == nil {
			t.Fatal("bob cannot sign in")
		}
		bob, active, err := auth.LookupUser(ctx, f.pool, "bob")
		if err != nil || !active {
			t.Fatalf("LookupUser: %v %v", active, err)
		}
		path := "/admin/admin_users/" + bob.ID.String() + "/"
		page := f.serve(http.MethodGet, path, nil, c).Body.String()
		if strings.Contains(page, "$2a$") || strings.Contains(page, "password_hash") {
			t.Error("the change page shows the hash")
		}
		// A blank password keeps the old one; the hash posted is ignored.
		keep := f.serve(http.MethodPost, path, url.Values{"username": {"bob"}, "is_active": {"true"}, "password_hash": {"x"}}, c)
		if keep.Code != http.StatusSeeOther {
			t.Fatalf("change: %d %s", keep.Code, keep.Body.String())
		}
		if sessionCookie(f.login("bob", "bob's passphrase", "/admin/")) == nil {
			t.Fatal("a change without a password lost it")
		}
		// Unchecking active deactivates.
		f.serve(http.MethodPost, path, url.Values{"username": {"bob"}, "password": {"new bob phrase"}, "password_confirm": {"new bob phrase"}}, c)
		if _, active, _ := auth.LookupUser(ctx, f.pool, "bob"); active {
			t.Error("bob is still active")
		}
		if w := f.login("bob", "new bob phrase", "/admin/"); w.Code != http.StatusUnauthorized {
			t.Errorf("an inactive user signed in: %d", w.Code)
		}
	})
}
