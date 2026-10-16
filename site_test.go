package jetadmin_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	pg "github.com/go-jet/jet/v2/postgres"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/example/db/jet/model"
	"github.com/garden-studio/jet-admin/example/db/jet/table"
	admintable "github.com/garden-studio/jet-admin/internal/db/jet/table"
)

func TestCrossOriginPostsAreRefused(t *testing.T) {
	f := newFixture(t)
	id := f.addCustomer("Ada", "ada@example.com", "free", true, time.Now())
	form := url.Values{"confirm": {"yes"}}
	path := "/admin/customers/" + id.String() + "/delete"
	tests := []struct {
		name   string
		header map[string]string
		status int
	}{
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"another origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"same origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusSeeOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := f.do(request{path: path, form: form, signedIn: true, header: tt.header})
			if r.status != tt.status {
				t.Fatalf("status %d, want %d", r.status, tt.status)
			}
		})
	}
	if n := f.count(table.Customers, nil); n != 0 {
		t.Errorf("%d customers: the same-origin delete did not run", n)
	}
}

func TestSignedOut(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/admin/", "/admin/customers/", "/admin/customers/add", "/admin/log"} {
		r := f.do(request{path: path})
		if r.status != http.StatusSeeOther || r.location() == "" {
			t.Errorf("%s: status %d to %q", path, r.status, r.location())
		}
	}
	r := f.do(request{path: "/admin/customers/", datastar: true})
	if !r.isSSE() {
		t.Errorf("a signed-out Datastar request got %q, want a redirect event", r.header.Get("Content-Type"))
	}
	post := f.do(request{path: "/admin/customers/add", form: url.Values{"name": {"x"}}})
	if post.status != http.StatusSeeOther || f.count(table.Customers, nil) != 0 {
		t.Errorf("a signed-out post: status %d", post.status)
	}
	for _, path := range []string{"/admin/static/admin.css", "/admin/static/datastar.js"} {
		if r := f.do(request{path: path}); r.status != http.StatusOK {
			t.Errorf("%s: status %d", path, r.status)
		}
	}
	if r := f.do(request{path: "/admin/static/missing.js"}); r.status != http.StatusNotFound {
		t.Errorf("missing static: %d", r.status)
	}
	if r := f.do(request{path: "/admin"}); r.status != http.StatusMovedPermanently || r.location() != "/admin/" {
		t.Errorf("/admin: %d to %q", r.status, r.location())
	}
}

// noLogin is a host's own Authenticator, which serves no login page.
type noLogin struct{ user *jetadmin.User }

func (a noLogin) Authenticate(*http.Request) (jetadmin.User, bool) {
	if a.user == nil {
		return jetadmin.User{}, false
	}
	return *a.user, true
}

func TestHostAuthenticatorAndPermissions(t *testing.T) {
	f := newFixture(t)
	id := f.addCustomer("Ada", "ada@example.com", "free", true, time.Now())
	mount := func(a jetadmin.Authenticator) http.Handler {
		site, err := jetadmin.New(jetadmin.Config{DB: f.pool, Auth: a, Logger: slog.New(slog.DiscardHandler), AuditLog: true})
		must(t, err)
		must(t, jetadmin.Register(site, jetadmin.Model[model.Customers]{Table: table.Customers, Permissions: jetadmin.ViewOnly}))
		return site.Handler()
	}
	serve := func(h http.Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, path, nil)
			r.PostForm = form
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := serve(mount(noLogin{}), http.MethodGet, "/", nil); w.Code != http.StatusForbidden {
		t.Errorf("no user and no login page: status %d, want 403", w.Code)
	}

	// The host's users are not admin_users rows; the log keeps their name.
	h := mount(noLogin{user: &jetadmin.User{Username: "host-user"}})
	page := serve(h, http.MethodGet, "/customers/"+id.String()+"/", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("view: %d", page.Code)
	}
	contains(t, page.Body.String(), "View customer: Customer "+id.String())
	lacks(t, page.Body.String(), `name="name"`, "Delete", "Save")
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/customers/add"},
		{http.MethodPost, "/customers/add"},
		{http.MethodPost, "/customers/" + id.String() + "/"},
		{http.MethodGet, "/customers/" + id.String() + "/delete"},
		{http.MethodPost, "/customers/"},
	} {
		if w := serve(h, tt.method, tt.path, url.Values{"name": {"x"}}); w.Code != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", tt.method, tt.path, w.Code)
		}
	}
}

func TestAuditLogOfAHostUser(t *testing.T) {
	f := newFixture(t)
	site, err := jetadmin.New(jetadmin.Config{DB: f.pool, Auth: noLogin{user: &jetadmin.User{Username: "host-user"}}, Logger: slog.New(slog.DiscardHandler), AuditLog: true})
	must(t, err)
	must(t, jetadmin.Register(site, jetadmin.Model[model.Customers]{Table: table.Customers}))
	r := httptest.NewRequest(http.MethodPost, "/customers/add", nil)
	r.PostForm = url.Values{"name": {"Ada"}, "email": {"ada@example.com"}, "preferences": {"{}"}}
	w := httptest.NewRecorder()
	site.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	log := admintable.AdminLog.FromSchema("")
	if n := f.count(log, pg.AND(log.Username.EQ(pg.String("host-user")), log.UserID.IS_NULL())); n != 1 {
		t.Errorf("%d entries by host-user without a user id, want 1", n)
	}
}
