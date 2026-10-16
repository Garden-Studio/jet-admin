// Package db is the demo's schema: schema.sql, and the Jet builders and
// models generated from it (go run ./scripts/jetgen -schema
// example/db/schema.sql -dest example/db/jet -db-tags).
package db

import _ "embed"

// Schema creates the demo's tables when they are missing.
//
//go:embed schema.sql
var Schema string
