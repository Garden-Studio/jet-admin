package jetadmin

import (
	_ "embed"
)

// Schema is the DDL of the tables jet-admin uses: admin_users and
// admin_sessions for the auth package, admin_log for Config.AuditLog. Paste
// it (or schema.sql, the same text) into one of your migrations.
//
//go:embed schema.sql
var Schema string
