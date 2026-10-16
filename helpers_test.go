package jetadmin_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/auth"
	exampledb "github.com/garden-studio/jet-admin/example/db"
	"github.com/garden-studio/jet-admin/example/db/jet/model"
	"github.com/garden-studio/jet-admin/example/db/jet/table"
	"github.com/garden-studio/jet-admin/internal/testdb"
)

// The tests run the admin's Handler over a real database holding the demo
// schema: customers (uuid key, jsonb, choices), orders (identity key,
// foreign key, text[]) and api_tokens (a hidden bytea secret).

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	auth   *auth.Auth
	site   *jetadmin.Site
	h      http.Handler
	user   auth.User
	cookie *http.Cookie
}

const password = "correct horse battery"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t, jetadmin.Schema, exampledb.Schema)
	ctx := context.Background()
	a := auth.New(pool, auth.Options{})
	user, err := auth.CreateUser(ctx, pool, "ann", password)
	if err != nil {
		t.Fatal(err)
	}
	site, err := jetadmin.New(jetadmin.Config{
		DB:       pool,
		Title:    "Test admin",
		BasePath: "/admin",
		Auth:     a,
		Logger:   slog.New(slog.DiscardHandler),
		AuditLog: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	registerDemo(t, site)
	if err := auth.Register(site); err != nil {
		t.Fatal(err)
	}
	cookie, err := a.NewSession(ctx, user.ID, "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, pool: pool, auth: a, site: site, h: site.Handler(), user: user, cookie: cookie}
}

func registerDemo(t *testing.T, site *jetadmin.Site) {
	t.Helper()
	c, o, k := table.Customers, table.Orders, table.APITokens
	must(t, jetadmin.Register(site, jetadmin.Model[model.Customers]{
		Table:        c,
		ListDisplay:  []jetadmin.Column{c.Name, c.Email, c.Plan, c.IsActive, c.CreatedAt},
		ListFilter:   []jetadmin.Column{c.Plan, c.IsActive, c.CreatedAt, c.Email},
		SearchFields: []jetadmin.Column{c.Name, c.Email},
		Ordering:     []jetadmin.Order{jetadmin.Asc(c.Name)},
		ReadOnly:     []jetadmin.Column{c.CreatedAt},
		Fields:       []jetadmin.Field{{Column: c.Plan, Choices: jetadmin.Choices("free", "pro", "team")}},
		Inlines:      []jetadmin.Inline{{Model: "orders", Column: o.CustomerID}},
		Display:      func(m model.Customers) string { return m.Name },
		PageSize:     3,
	}))
	must(t, jetadmin.Register(site, jetadmin.Model[model.Orders]{
		Table:       o,
		ListDisplay: []jetadmin.Column{o.ID, o.CustomerID, o.Status, o.TotalCents},
		ListFilter:  []jetadmin.Column{o.Status, o.ShippedAt},
		ForeignKeys: []jetadmin.ForeignKey{{Column: o.CustomerID, Model: "customers"}},
		Display:     func(m model.Orders) string { return fmt.Sprintf("Order %d", m.ID) },
	}))
	must(t, jetadmin.Register(site, jetadmin.Model[model.APITokens]{
		Table:       k,
		ListDisplay: []jetadmin.Column{k.ID, k.Label, k.CustomerID},
		Hidden:      []jetadmin.Column{k.Secret},
		ReadOnly:    []jetadmin.Column{k.CreatedAt},
		ForeignKeys: []jetadmin.ForeignKey{{Column: k.CustomerID, Model: "customers"}},
		Display:     func(m model.APITokens) string { return m.Label },
	}))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// response is what the Handler answered.
type response struct {
	status int
	header http.Header
	body   string
}

func (r response) location() string { return r.header.Get("Location") }

func (r response) isSSE() bool {
	return strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream")
}

type request struct {
	method   string
	path     string
	form     url.Values
	datastar bool
	signedIn bool
	header   map[string]string
	cookies  []*http.Cookie
}

func (f *fixture) do(req request) response {
	f.t.Helper()
	var body io.Reader
	if req.form != nil {
		body = strings.NewReader(req.form.Encode())
	}
	method := req.method
	if method == "" {
		method = http.MethodGet
		if req.form != nil {
			method = http.MethodPost
		}
	}
	r := httptest.NewRequest(method, req.path, body)
	if req.form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if req.datastar {
		r.Header.Set("Datastar-Request", "true")
	}
	for k, v := range req.header {
		r.Header.Set(k, v)
	}
	if req.signedIn {
		r.AddCookie(f.cookie)
	}
	for _, c := range req.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return response{status: w.Code, header: w.Header(), body: w.Body.String()}
}

// get is a signed-in GET.
func (f *fixture) get(path string) response {
	f.t.Helper()
	return f.do(request{path: path, signedIn: true})
}

// post is a signed-in form post.
func (f *fixture) post(path string, form url.Values) response {
	f.t.Helper()
	return f.do(request{path: path, form: form, signedIn: true})
}

func (f *fixture) exec(stmt pg.Statement) {
	f.t.Helper()
	query, args := stmt.Sql()
	if _, err := f.pool.Exec(context.Background(), query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func (f *fixture) scalar(stmt pg.Statement, dest any) {
	f.t.Helper()
	query, args := stmt.Sql()
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(dest); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

// addCustomer inserts a customer and returns its id.
func (f *fixture) addCustomer(name, email, plan string, active bool, created time.Time) uuid.UUID {
	f.t.Helper()
	c := table.Customers
	var id uuid.UUID
	f.scalar(c.INSERT(c.Name, c.Email, c.Plan, c.IsActive, c.CreatedAt).
		VALUES(pg.String(name), pg.String(email), pg.String(plan), pg.Bool(active), pg.TimestampzT(created)).
		RETURNING(c.ID), &id)
	return id
}

func (f *fixture) addOrder(customer uuid.UUID, status string, cents int32) int64 {
	f.t.Helper()
	o := table.Orders
	var id int64
	f.scalar(o.INSERT(o.CustomerID, o.Status, o.TotalCents).
		VALUES(pg.UUID(customer), pg.String(status), pg.Int32(cents)).
		RETURNING(o.ID), &id)
	return id
}

func (f *fixture) count(t pg.Table, where pg.BoolExpression) int64 {
	f.t.Helper()
	stmt := pg.SELECT(pg.COUNT(pg.STAR)).FROM(t)
	if where != nil {
		stmt = stmt.WHERE(where)
	}
	var n int64
	f.scalar(stmt, &n)
	return n
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q", w)
		}
	}
}

func lacks(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(body, w) {
			t.Errorf("body has %q", w)
		}
	}
}

func base64URLDecode(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return string(b), err
}
