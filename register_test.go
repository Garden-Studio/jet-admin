package jetadmin_test

import (
	"log/slog"
	"strings"
	"testing"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/auth"
	"github.com/garden-studio/jet-admin/example/db/jet/model"
	"github.com/garden-studio/jet-admin/example/db/jet/table"
)

// keyless is a model without a primary key tag, as Jet generates for a
// table without one.
type keyless struct {
	ID    int64  `db:"id"`
	Label string `db:"label"`
	Other string
}

// tokenRow has the api_tokens columns but no primary key.
type tokenRow struct {
	ID         int64 `db:"id"`
	CustomerID string
	Label      string
	Secret     []byte
	CreatedAt  string
	RevokedAt  *string
}

type twoKeys struct {
	ID         int64  `sql:"primary_key"`
	CustomerID string `sql:"primary_key"`
	Label      string
	Secret     []byte
	CreatedAt  string
	RevokedAt  *string
}

func TestRegisterErrors(t *testing.T) {
	// Registration reads only Go values, so the pool never connects.
	pool, err := pgxpool.New(t.Context(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	newSite := func() *jetadmin.Site {
		site, err := jetadmin.New(jetadmin.Config{DB: pool, Auth: auth.New(pool, auth.Options{}), Logger: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		return site
	}
	c, k := table.Customers, table.APITokens
	tests := []struct {
		name     string
		register func(*jetadmin.Site) error
		want     string
	}{
		{"no primary key", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[tokenRow]{Table: k})
		}, "no primary key"},
		{"composite primary key", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[twoKeys]{Table: k})
		}, "composite primary keys are not supported"},
		{"unknown icon", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c, Icon: "no-such-icon"})
		}, `Model.Icon "no-such-icon" is no lucide icon`},
		{"model lacks a column", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[keyless]{Table: k})
		}, `no field for column "customer_id"`},
		{"no table", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{})
		}, "Model.Table is required"},
		{"not a generated builder", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: pg.NewTable("public", "customers", "", c.ID)})
		}, "has no AllColumns field"},
		{"column of another table", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c, ListDisplay: []jetadmin.Column{k.Label}})
		}, "not a column of customers"},
		{"hidden primary key", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c, Hidden: []jetadmin.Column{c.ID}})
		}, "cannot be Hidden"},
		{"hidden and listed", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c, Hidden: []jetadmin.Column{c.Notes}, SearchFields: []jetadmin.Column{c.Notes}})
		}, "is Hidden but listed"},
		{"reserved slug", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c, Slug: "login"})
		}, "reserved"},
		{"slug taken", func(s *jetadmin.Site) error {
			if err := jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c}); err != nil {
				return err
			}
			return jetadmin.Register(s, jetadmin.Model[model.Customers]{Table: c})
		}, "is taken"},
		{"a model that is not a struct", func(s *jetadmin.Site) error {
			return jetadmin.Register(s, jetadmin.Model[string]{Table: c})
		}, "not a struct"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.register(newSite())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Register: %v, want an error with %q", err, tt.want)
			}
		})
	}

	t.Run("a valid model registers", func(t *testing.T) {
		if err := jetadmin.Register(newSite(), jetadmin.Model[model.Customers]{Table: c}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNewNeedsDBAndAuth(t *testing.T) {
	if _, err := jetadmin.New(jetadmin.Config{}); err == nil {
		t.Error("New without a DB succeeded")
	}
	pool, _ := pgxpool.New(t.Context(), "postgres://nobody@127.0.0.1:1/none")
	defer pool.Close()
	if _, err := jetadmin.New(jetadmin.Config{DB: pool}); err == nil {
		t.Error("New without an Authenticator succeeded")
	}
}

func TestBasePath(t *testing.T) {
	// New reads only Go values, so the pool never connects.
	pool, err := pgxpool.New(t.Context(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tests := []struct {
		name, basePath, want string
		fails                bool
	}{
		{name: "empty is the default", basePath: "", want: "/admin"},
		{name: "the root", basePath: "/", want: ""},
		{name: "a trailing slash", basePath: "/data/", want: "/data"},
		{name: "no leading slash", basePath: "data", fails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			site, err := jetadmin.New(jetadmin.Config{DB: pool, Auth: auth.New(pool, auth.Options{}), BasePath: tt.basePath})
			if tt.fails {
				if err == nil {
					t.Fatalf("BasePath %q was accepted", tt.basePath)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := site.BasePath(); got != tt.want {
				t.Errorf("BasePath() = %q, want %q", got, tt.want)
			}
		})
	}
}
