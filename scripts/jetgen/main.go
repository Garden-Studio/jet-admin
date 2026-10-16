// Command jetgen writes Jet table builders and models for a schema file. It
// creates a scratch database on the server, runs the file there, generates
// from it and drops it again, so no migrated database has to exist.
//
//	go run ./scripts/jetgen -schema schema.sql -dest internal/db/jet
//	go run ./scripts/jetgen -schema example/db/schema.sql -dest example/db/jet -db-tags
//
// Without -db-tags the models are Jet's defaults (no db tags, string for
// jsonb), which is what the admin's own tables use and so what the tests of
// a default model run against. With it every field carries a `db:"column"`
// tag and jsonb is json.RawMessage, as in a generator tuned for pgx.
//
// It drives Jet's generator as a library rather than through the jet
// command, which links the cgo SQLite driver.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/go-jet/jet/v2/generator/metadata"
	postgresgen "github.com/go-jet/jet/v2/generator/postgres"
	"github.com/go-jet/jet/v2/generator/template"
	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/garden-studio/jet-admin/internal/testdb"
)

func main() {
	server := flag.String("server", envOr("TEST_DATABASE_URL", testdb.DefaultURL), "PostgreSQL server to make the scratch database on")
	schema := flag.String("schema", "", "SQL file to generate from")
	dest := flag.String("dest", "", "directory to write table/ and model/ into; emptied first")
	dbTags := flag.Bool("db-tags", false, "tag model fields with db:\"column\" and use json.RawMessage for jsonb")
	flag.Parse()
	if *schema == "" || *dest == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*server, *schema, *dest, *dbTags); err != nil {
		fmt.Fprintln(os.Stderr, "jetgen:", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run(server, schemaFile, dest string, dbTags bool) error {
	ctx := context.Background()
	ddl, err := os.ReadFile(schemaFile)
	if err != nil {
		return err
	}
	admin, err := pgx.Connect(ctx, server)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "jetgen_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		return err
	}
	defer admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))

	db, err := sql.Open("pgx", testdb.WithDatabase(server, name))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		return fmt.Errorf("apply %s: %w", schemaFile, err)
	}
	// Jet's generator empties dest before writing, so dest holds only
	// what it generates.
	model := template.DefaultModel()
	if dbTags {
		model = model.UseTable(taggedModel)
	}
	return postgresgen.GenerateDB(db, "public", dest, template.Default(postgres.Dialect).
		UseSchema(func(schema metadata.Schema) template.Schema {
			return template.DefaultSchema(schema).UsePath("").UseModel(model)
		}))
}

// taggedModel tags each field with its column and reads jsonb as
// json.RawMessage, a type pgx decodes without a copy into string.
func taggedModel(table metadata.Table) template.TableModel {
	return template.DefaultTableModel(table).UseField(func(column metadata.Column) template.TableModelField {
		field := template.DefaultTableModelField(column)
		name := strings.ToLower(strings.TrimPrefix(column.DataType.Name, "_"))
		switch {
		case column.DataType.IsArray() && (name == "text" || name == "varchar"):
			field = field.UseType(template.NewType([]string(nil)))
		case !column.DataType.IsArray() && (name == "jsonb" || name == "json"):
			field = field.UseType(template.NewType(json.RawMessage(nil)))
		}
		return field.UseTags(fmt.Sprintf("db:%q", column.Name))
	})
}
