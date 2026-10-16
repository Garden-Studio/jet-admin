// Package testdb gives each test its own PostgreSQL database, built from the
// DDL the test passes and dropped when the test ends.
//
// TEST_DATABASE_URL points at the server (any database on it). Tests skip
// when it is unreachable and TEST_DATABASE_REQUIRED is unset, so a plain
// `go test ./...` without Postgres still runs the rest; CI sets it.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultURL is the development server the tests use without
// TEST_DATABASE_URL.
const DefaultURL = "postgres://searchnzb:dev@localhost:55432/postgres"

// New returns a pool on a fresh database where every script of ddl has run.
func New(t testing.TB, ddl ...string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	serverURL := os.Getenv("TEST_DATABASE_URL")
	if serverURL == "" {
		serverURL = DefaultURL
	}
	admin, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		if os.Getenv("TEST_DATABASE_REQUIRED") != "" {
			t.Fatalf("connect to test server: %v", err)
		}
		t.Skipf("no PostgreSQL for tests (%v); set TEST_DATABASE_URL", err)
	}
	defer admin.Close(context.Background())

	// The name is made here from hex digits, so it is safe to paste into
	// the DDL, which takes no parameters.
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "jetadmin_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		conn, err := pgx.Connect(cleanup, serverURL)
		if err != nil {
			return
		}
		defer conn.Close(cleanup)
		_, _ = conn.Exec(cleanup, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
	})

	pool, err := pgxpool.New(ctx, WithDatabase(serverURL, name))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, script := range ddl {
		if _, err := pool.Exec(ctx, script); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}
	return pool
}

// WithDatabase is url with its database replaced by name.
func WithDatabase(url, name string) string {
	before, after, _ := strings.Cut(url, "?")
	before = before[:strings.LastIndex(before, "/")+1] + name
	if after != "" {
		return before + "?" + after
	}
	return before
}
