package jetadmin

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"time"

	pg "github.com/go-jet/jet/v2/postgres"

	"github.com/garden-studio/jet-admin/internal/db/jet/model"
	"github.com/garden-studio/jet-admin/internal/db/jet/table"
	"github.com/garden-studio/jet-admin/internal/ui"
)

// The audit log: a row in admin_log per add, change and delete, written in
// the transaction that makes it.

// The admin's own tables are named without a schema, so they are found on
// the host's search_path wherever its migration put them.
var (
	adminLog   = table.AdminLog.FromSchema("")
	adminUsers = table.AdminUsers.FromSchema("")
)

const (
	actionAdd    int16 = 1
	actionChange int16 = 2
	actionDelete int16 = 3
)

// logAction records what user did to the object key of p. The user's id
// is kept only when it is an admin_users row: a host's own Authenticator
// has users of its own, which the foreign key would refuse.
func (s *Site) logAction(ctx context.Context, q querier, u User, p *modelPage, key, repr string, action int16, message string) error {
	if !s.auditLog {
		return nil
	}
	knownUser := pg.SELECT(adminUsers.ID).FROM(adminUsers).WHERE(adminUsers.ID.EQ(pg.UUID(u.ID)))
	stmt := adminLog.INSERT(adminLog.UserID, adminLog.Username, adminLog.Model, adminLog.ObjectID, adminLog.ObjectRepr, adminLog.Action, adminLog.ChangeMessage).
		VALUES(knownUser, pg.String(u.Username), pg.String(p.slug), pg.String(key), pg.String(truncate(repr, 200)), pg.Int16(action), pg.String(message))
	query, args := stmt.Sql()
	_, err := q.Exec(ctx, query, args...)
	return err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// changeMessage names the fields a change touched, never their values,
// which may be secrets.
func changeMessage(labels []string) string {
	switch len(labels) {
	case 0:
		return "No fields changed."
	case 1:
		return "Changed " + labels[0] + "."
	}
	return "Changed " + strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1] + "."
}

// logEntries reads admin_log rows matching where, newest first.
func (s *Site) logEntries(ctx context.Context, where pg.BoolExpression, limit, offset int64) ([]ui.LogEntry, error) {
	stmt := pg.SELECT(adminLog.AllColumns).FROM(adminLog)
	if where != nil {
		stmt = stmt.WHERE(where)
	}
	stmt = stmt.ORDER_BY(adminLog.ActionTime.DESC(), adminLog.ID.DESC()).LIMIT(limit).OFFSET(offset)
	rows, err := run(ctx, s.db, stmt)
	if err != nil {
		return nil, err
	}
	typ := reflect.TypeFor[model.AdminLog]()
	found, err := scanRows(rows, typ, modelFields(typ))
	if err != nil {
		return nil, err
	}
	out := make([]ui.LogEntry, 0, len(found))
	for _, v := range found {
		out = append(out, s.logEntry(v.Interface().(model.AdminLog)))
	}
	return out, nil
}

func (s *Site) countLog(ctx context.Context, where pg.BoolExpression) (int64, error) {
	stmt := pg.SELECT(pg.COUNT(pg.STAR).AS("count")).FROM(adminLog)
	if where != nil {
		stmt = stmt.WHERE(where)
	}
	query, args := stmt.Sql()
	var n int64
	err := s.db.QueryRow(ctx, query, args...).Scan(&n)
	return n, err
}

func (s *Site) logEntry(row model.AdminLog) ui.LogEntry {
	e := ui.LogEntry{
		Time:    row.ActionTime.UTC().Format(time.DateTime),
		User:    row.Username,
		Model:   row.Model,
		Object:  row.ObjectRepr,
		Message: row.ChangeMessage,
	}
	if e.User == "" {
		e.User = "—"
	}
	switch row.Action {
	case actionAdd:
		e.Action, e.ActionClass = "Added", "add"
	case actionChange:
		e.Action, e.ActionClass = "Changed", "change"
	default:
		e.Action, e.ActionClass = "Deleted", "delete"
	}
	if p, ok := s.model(row.Model); ok {
		e.Model = p.plural
		if row.Action != actionDelete {
			e.ObjectURL = p.changeURL(row.ObjectID)
		}
	}
	return e
}

// objectLog is the condition of the entries of one object.
func objectLog(p *modelPage, key string) pg.BoolExpression {
	return pg.AND(adminLog.Model.EQ(pg.String(p.slug)), adminLog.ObjectID.EQ(pg.String(key)))
}

func (p *modelPage) listURL() string { return p.site.base + "/" + url.PathEscape(p.slug) + "/" }
func (p *modelPage) addURL() string  { return p.site.base + "/" + url.PathEscape(p.slug) + "/add" }
func (p *modelPage) changeURL(key string) string {
	return p.site.base + "/" + url.PathEscape(p.slug) + "/" + url.PathEscape(key) + "/"
}
func (p *modelPage) deleteURL(key string) string  { return p.changeURL(key) + "delete" }
func (p *modelPage) historyURL(key string) string { return p.changeURL(key) + "history" }
