// Command example runs a jet-admin site over a small demo schema:
// customers, their orders and their API tokens.
//
//	DATABASE_URL=postgres://user:pass@localhost:5432/demo go run ./example
//
// It creates the tables it needs (jet-admin's and the demo's), seeds a few
// rows, makes an admin user with a fresh password, prints it and serves the
// admin at http://localhost:8080/admin/.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/auth"
	"github.com/garden-studio/jet-admin/example/db"
	"github.com/garden-studio/jet-admin/example/db/jet/model"
	"github.com/garden-studio/jet-admin/example/db/jet/table"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("example", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("set DATABASE_URL to a PostgreSQL database the demo may create tables in")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "localhost:8080"
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := seed(ctx, pool); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		password = randomPassword()
	}
	if _, err := auth.CreateUser(ctx, pool, "admin", password); errors.Is(err, auth.ErrUsernameTaken) {
		err = auth.SetPassword(ctx, pool, "admin", password)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	a := auth.New(pool, auth.Options{Logger: logger})
	site, err := jetadmin.New(jetadmin.Config{
		DB:       pool,
		Title:    "Demo admin",
		BasePath: "/admin",
		Auth:     a,
		Logger:   logger,
		AuditLog: true,
	})
	if err != nil {
		return err
	}
	if err := register(site); err != nil {
		return err
	}
	if err := auth.Register(site); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/admin/", site.Handler())
	mux.Handle("/{$}", http.RedirectHandler("/admin/", http.StatusFound))
	fmt.Printf("\n  Admin:    http://%s/admin/\n  Username: admin\n  Password: %s\n\n", addr, password)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return server.ListenAndServe()
}

// register describes the demo's tables to the admin.
func register(site *jetadmin.Site) error {
	c, o, k := table.Customers, table.Orders, table.APITokens
	err := jetadmin.Register(site, jetadmin.Model[model.Customers]{
		Table:        c,
		Group:        "Shop",
		ListDisplay:  []jetadmin.Column{c.Name, c.Email, c.Plan, c.IsActive, c.CreatedAt},
		ListFilter:   []jetadmin.Column{c.Plan, c.IsActive, c.CreatedAt},
		SearchFields: []jetadmin.Column{c.Name, c.Email},
		Ordering:     []jetadmin.Order{jetadmin.Desc(c.CreatedAt)},
		ReadOnly:     []jetadmin.Column{c.CreatedAt},
		Fields: []jetadmin.Field{
			{Column: c.Plan, Choices: []jetadmin.Choice{{Value: "free", Label: "Free"}, {Value: "pro", Label: "Pro"}, {Value: "team", Label: "Team"}}},
			{Column: c.IsActive, Label: "Active"},
			{Column: c.Preferences, Help: "Settings the app reads, as a JSON object."},
			{Column: c.Notes, Widget: jetadmin.Textarea},
		},
		Inlines: []jetadmin.Inline{{Model: "orders", Column: o.CustomerID}, {Model: "api_tokens", Column: k.CustomerID}},
		Display: func(m model.Customers) string { return m.Name },
		Validate: func(_ context.Context, m *model.Customers) error {
			if !strings.Contains(m.Email, "@") {
				return jetadmin.FieldErrors{"email": "Enter an email address."}
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	err = jetadmin.Register(site, jetadmin.Model[model.Orders]{
		Table:        o,
		Group:        "Shop",
		ListDisplay:  []jetadmin.Column{o.ID, o.CustomerID, o.Status, o.TotalCents, o.Paid, o.ShippedAt, o.CreatedAt},
		ListFilter:   []jetadmin.Column{o.Status, o.Paid, o.ShippedAt},
		SearchFields: []jetadmin.Column{o.ID, o.Status},
		ReadOnly:     []jetadmin.Column{o.CreatedAt},
		Fields: []jetadmin.Field{
			{Column: o.Status, Choices: jetadmin.Choices("pending", "paid", "shipped", "cancelled")},
			{Column: o.TotalCents, Label: "Total (cents)"},
		},
		ForeignKeys: []jetadmin.ForeignKey{{Column: o.CustomerID, Model: "customers"}},
		Display:     func(m model.Orders) string { return fmt.Sprintf("Order #%d", m.ID) },
		PageSize:    25,
	})
	if err != nil {
		return err
	}
	return jetadmin.Register(site, jetadmin.Model[model.APITokens]{
		Table:       k,
		Label:       "API token",
		Plural:      "API tokens",
		Group:       "Shop",
		ListDisplay: []jetadmin.Column{k.ID, k.Label, k.CustomerID, k.CreatedAt, k.RevokedAt},
		ListFilter:  []jetadmin.Column{k.RevokedAt},
		Hidden:      []jetadmin.Column{k.Secret},
		ReadOnly:    []jetadmin.Column{k.CreatedAt},
		ForeignKeys: []jetadmin.ForeignKey{{Column: k.CustomerID, Model: "customers"}},
		Display:     func(m model.APITokens) string { return m.Label },
	})
}

// migrate creates the tables when they are missing. A real host puts
// jetadmin.Schema into one of its own migrations instead.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	var missing bool
	query, args := pg.SELECT(pg.Func("to_regclass", pg.String("admin_users")).IS_NULL()).Sql()
	if err := pool.QueryRow(ctx, query, args...).Scan(&missing); err != nil {
		return err
	}
	if missing {
		if _, err := pool.Exec(ctx, jetadmin.Schema); err != nil {
			return err
		}
	}
	_, err := pool.Exec(ctx, db.Schema)
	return err
}

// seed adds a few customers and orders to an empty demo.
func seed(ctx context.Context, pool *pgxpool.Pool) error {
	c, o, k := table.Customers, table.Orders, table.APITokens
	var n int64
	query, args := pg.SELECT(pg.COUNT(pg.STAR)).FROM(c).Sql()
	if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil || n > 0 {
		return err
	}
	people := []model.Customers{
		{Name: "Ada Lovelace", Email: "ada@example.com", Plan: "team", IsActive: true, Preferences: []byte(`{"theme": "dark", "digest": "weekly"}`)},
		{Name: "Grace Hopper", Email: "grace@example.com", Plan: "pro", IsActive: true, Preferences: []byte(`{"theme": "light"}`)},
		{Name: "Alan Turing", Email: "alan@example.com", Plan: "free", IsActive: true, Preferences: []byte(`{}`)},
		{Name: "Katherine Johnson", Email: "katherine@example.com", Plan: "pro", IsActive: true, Preferences: []byte(`{"digest": "daily"}`)},
		{Name: "Edsger Dijkstra", Email: "edsger@example.com", Plan: "free", IsActive: false, Preferences: []byte(`{}`)},
		{Name: "Barbara Liskov", Email: "barbara@example.com", Plan: "team", IsActive: true, Preferences: []byte(`{"theme": "dark"}`)},
		{Name: "Donald Knuth", Email: "don@example.com", Plan: "free", IsActive: true, Preferences: []byte(`{}`)},
		{Name: "Margaret Hamilton", Email: "margaret@example.com", Plan: "team", IsActive: true, Preferences: []byte(`{"theme": "light"}`)},
	}
	statuses := []string{"pending", "paid", "shipped", "cancelled"}
	for i, p := range people {
		created := time.Now().UTC().Add(-time.Duration(i*9+2) * 24 * time.Hour)
		var id string
		query, args := c.INSERT(c.Name, c.Email, c.Plan, c.IsActive, c.Preferences, c.CreatedAt).
			VALUES(pg.String(p.Name), pg.String(p.Email), pg.String(p.Plan), pg.Bool(p.IsActive), pg.Json(string(p.Preferences)), pg.TimestampzT(created)).
			RETURNING(pg.CAST(c.ID).AS_TEXT()).Sql()
		if err := pool.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			return err
		}
		customer := pg.CAST(pg.String(id)).AS("uuid")
		for j := range i%4 + 1 {
			status := statuses[(i+j)%len(statuses)]
			at := created.Add(time.Duration(j+1) * 26 * time.Hour)
			shipped := pg.TimestampzExp(pg.NULL)
			if status == "shipped" {
				shipped = pg.TimestampzT(at.Add(30 * time.Hour))
			}
			query, args := o.INSERT(o.CustomerID, o.Status, o.TotalCents, o.Tags, o.Paid, o.ShippedAt, o.CreatedAt).
				VALUES(customer, pg.String(status), pg.Int32(int32(1200+i*731+j*389)), pg.Raw("#tags::text[]", pg.RawArgs{"#tags": []string{"web", statuses[j]}}), pg.Bool(status != "pending" && status != "cancelled"), shipped, pg.TimestampzT(at)).Sql()
			if _, err := pool.Exec(ctx, query, args...); err != nil {
				return err
			}
		}
		if i%3 == 0 {
			query, args := k.INSERT(k.CustomerID, k.Label).VALUES(customer, pg.String("CI deploys")).Sql()
			if _, err := pool.Exec(ctx, query, args...); err != nil {
				return err
			}
		}
	}
	return nil
}

func randomPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
