# Agent guidelines

jet-admin is a public Go library: a Django-admin-style admin over PostgreSQL
tables described by go-jet/jet generated code, rendered with templ and
enhanced with Datastar. README.md is the user documentation; keep it in step
with the public API.

## Layout

```
*.go                  package jetadmin: Site, Model, Register, the pages and their statements
schema.sql            the admin's own tables (jetadmin.Schema)
auth/                 the database-backed Authenticator, the CLI helpers, auth.Register
internal/core         what jetadmin and auth share (User, Authenticator, the page chrome)
internal/ui           templ components, view structs, admin.css and datastar.js (embedded)
internal/db/jet       Jet's generated code for schema.sql: never edit by hand
internal/testdb       a fresh database per test
example/              the runnable demo; example/db/jet is generated, with db tags
scripts/jetgen        regenerates internal/db/jet and example/db/jet
scripts/check-commit-msg  the commit-msg hook
```

## Rules

- Before reporting work done, all of these pass:
  - `go build ./...`
  - `go vet ./...`
  - `gofmt -l .` prints nothing
  - `go test ./...` with PostgreSQL reachable (`TEST_DATABASE_URL`, default
    `postgres://searchnzb:dev@localhost:55432/postgres`; tests skip without
    it, so check they ran)
- After editing a `.templ` file, regenerate and commit the `*_templ.go`:
  `go run github.com/a-h/templ/cmd/templ@v0.3.1070 generate`. CI fails when
  they differ.
- After changing `schema.sql` or `example/db/schema.sql`, regenerate the Jet
  code: `go run ./scripts/jetgen -schema schema.sql -dest internal/db/jet`
  and `go run ./scripts/jetgen -schema example/db/schema.sql -dest
  example/db/jet -db-tags`. Changing `schema.sql` changes what every host
  migrates: say so in the commit (a `!` if it breaks them).
- For a visible change, run the example and look at the pages (light, dark,
  narrow), with and without JavaScript.
- Every PostgreSQL statement is built with Jet's DSL (imported as `pg` in
  this package, which declares names Jet also exports) and run through pgx.
  No SQL strings in Go except DDL: the schema and the test databases'
  `CREATE DATABASE`. `Raw` only with constant SQL, values in its `RawArgs`.
- Tests run against a real PostgreSQL, one database per test; never mock
  the database. Prefer table tests.
- Comments explain why, in plain prose. Errors are wrapped with what was
  being done. Logging is slog.
- Every page works without JavaScript: links and form posts answered with
  303 redirects. Datastar requests (header `Datastar-Request: true`) get
  SSE patches or a redirect event instead.
- Keep pages accessible: a label for every input, visible focus, aria where
  the markup alone does not say it.

## Commits

Conventional Commits, checked by the `commit-msg` hook
(`prek install --hook-type pre-commit --hook-type commit-msg`) and, for pull
request titles, by CI:

```
type(scope)!: description
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`, `perf`,
`build` (also `style`, `revert`). The scope is optional (`auth`, `ui`,
`example`, …); `!` marks a breaking change, explained in the body. Never add a
Co-Authored-By trailer or any AI attribution.
