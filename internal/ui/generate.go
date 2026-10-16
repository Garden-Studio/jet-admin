package ui

// The stylesheet is compiled here, not by the host: Tailwind v4 (gowind, a
// Go build of it pinned in go.mod) reads css/admin.css, scans this package's
// templates and writes the minified static/admin.css that is embedded.
// CI fails when the committed file is stale.
//
//go:generate go tool gowind --input css/admin.css --output static/admin.css --minify --silent
