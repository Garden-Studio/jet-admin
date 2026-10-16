package jetadmin

import (
	"context"
	"errors"
	"reflect"
	"strings"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
)

// The statements of the model pages. Every one is built with Jet; values
// are parameters. A value the admin compares with a column of a type it
// only knows at run time (a uuid or bigint key) goes as an untyped
// parameter, which PostgreSQL types from the column it meets, so the
// comparison type-checks and still uses the column's index.

// param is v as a parameter PostgreSQL types from its context.
func param(v any) pg.Expression {
	return pg.Raw("#value", pg.RawArgs{"#value": v})
}

// equals is c = v, v typed from c.
func equals(c pg.Column, v any) pg.BoolExpression {
	return pg.BoolExp(pg.BinaryOperator(c, param(v), "="))
}

// asText is c read as text, for a search or a filter that matches its text
// form whatever its type.
func asText(c pg.Column) pg.StringExpression {
	return pg.CAST(c).AS_TEXT()
}

// columnList is the columns, as Jet's list.
func columnList(cols []*column) pg.ColumnList {
	out := make(pg.ColumnList, len(cols))
	for i, c := range cols {
		out[i] = c.col
	}
	return out
}

// shown are the columns a page may read: every one but the hidden.
func (p *modelPage) shown() []*column {
	var out []*column
	for _, c := range p.columns {
		if !c.hidden {
			out = append(out, c)
		}
	}
	return out
}

func run(ctx context.Context, q querier, stmt pg.Statement) (pgx.Rows, error) {
	query, args := stmt.Sql()
	return q.Query(ctx, query, args...)
}

func (p *modelPage) rows(ctx context.Context, q querier, stmt pg.Statement) ([]reflect.Value, error) {
	rows, err := run(ctx, q, stmt)
	if err != nil {
		return nil, err
	}
	return scanRows(rows, p.typ, modelFields(p.typ))
}

// get is the row of key: its shown columns, or every one when all (a save,
// which must keep the hidden ones a hook may read).
func (p *modelPage) get(ctx context.Context, q querier, key any, all bool) (reflect.Value, bool, error) {
	cols := p.shown()
	if all {
		cols = p.columns
	}
	found, err := p.rows(ctx, q, pg.SELECT(columnList(cols)).FROM(p.table).WHERE(equals(p.pk.col, key)))
	if err != nil || len(found) == 0 {
		return reflect.Value{}, false, err
	}
	return found[0], true, nil
}

// listQuery is what a list page asks: its conditions and order.
type listQuery struct {
	where []pg.BoolExpression
	order []pg.OrderByClause
}

func (lq listQuery) apply(stmt pg.SelectStatement) pg.SelectStatement {
	if len(lq.where) > 0 {
		stmt = stmt.WHERE(pg.AND(lq.where...))
	}
	return stmt
}

func (p *modelPage) count(ctx context.Context, q querier, lq listQuery) (int64, error) {
	stmt := lq.apply(pg.SELECT(pg.COUNT(pg.STAR).AS("count")).FROM(p.table))
	query, args := stmt.Sql()
	var n int64
	err := q.QueryRow(ctx, query, args...).Scan(&n)
	return n, err
}

func (p *modelPage) page(ctx context.Context, q querier, lq listQuery, limit, offset int64) ([]reflect.Value, error) {
	stmt := lq.apply(pg.SELECT(columnList(p.shown())).FROM(p.table)).ORDER_BY(lq.order...).LIMIT(limit).OFFSET(offset)
	return p.rows(ctx, q, stmt)
}

// searchCondition matches every word of text against one of the search
// fields, as Django does: "ann smith" finds Ann Smith by name and email.
func (p *modelPage) searchCondition(text string) pg.BoolExpression {
	var words []pg.BoolExpression
	for word := range strings.FieldsSeq(text) {
		pattern := "%" + likeEscaper.Replace(word) + "%"
		var any []pg.BoolExpression
		for _, c := range p.search {
			any = append(any, pg.BoolExp(pg.BinaryOperator(asText(c.col), pg.String(pattern), "ILIKE")))
		}
		words = append(words, pg.OR(any...))
	}
	if len(words) == 0 {
		return nil
	}
	return pg.AND(words...)
}

// likeEscaper makes a searched word match itself in LIKE, where % and _
// are wildcards and \ escapes.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// distinctValues are the values of c in its text form, at most limit of
// them, for a filter.
func (p *modelPage) distinctValues(ctx context.Context, q querier, c *column, limit int64) ([]string, error) {
	value := asText(c.col).AS("value")
	stmt := pg.SELECT(value).DISTINCT().FROM(p.table).WHERE(c.col.IS_NOT_NULL()).ORDER_BY(pg.StringColumn("value").ASC()).LIMIT(limit)
	rows, err := run(ctx, q, stmt)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// insert adds a row of values for cols and returns it as the database
// wrote it, defaults and all.
func (p *modelPage) insert(ctx context.Context, q querier, cols []*column, values []any) (reflect.Value, error) {
	var stmt pg.InsertStatement
	if len(cols) == 0 {
		// Every column is left to its default.
		stmt = p.table.INSERT(p.columns[0].col).VALUES(pg.DEFAULT)
	} else {
		stmt = p.table.INSERT(columnList(cols)).VALUES(values[0], values[1:]...)
	}
	found, err := p.rows(ctx, q, stmt.RETURNING(columnList(p.columns)))
	if err != nil {
		return reflect.Value{}, err
	}
	if len(found) != 1 {
		return reflect.Value{}, errors.New("the insert returned no row")
	}
	return found[0], nil
}

// update writes values to cols of the row of key, and reports whether
// there was one.
func (p *modelPage) update(ctx context.Context, q querier, key any, cols []*column, values []any) (bool, error) {
	if len(cols) == 0 {
		return true, nil
	}
	stmt := p.table.UPDATE(columnList(cols)).SET(values[0], values[1:]...).WHERE(equals(p.pk.col, key))
	query, args := stmt.Sql()
	tag, err := q.Exec(ctx, query, args...)
	return tag.RowsAffected() > 0, err
}

// deleteKeys deletes the rows of keys.
func (p *modelPage) deleteKeys(ctx context.Context, q querier, keys []any) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	stmt := p.table.DELETE().WHERE(p.keyIn(keys))
	query, args := stmt.Sql()
	tag, err := q.Exec(ctx, query, args...)
	return tag.RowsAffected(), err
}

// byKeys are the rows of keys, in the list's default order.
func (p *modelPage) byKeys(ctx context.Context, q querier, keys []any) ([]reflect.Value, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return p.rows(ctx, q, pg.SELECT(columnList(p.shown())).FROM(p.table).WHERE(p.keyIn(keys)).ORDER_BY(p.defaultOrder()...))
}

func (p *modelPage) keyIn(keys []any) pg.BoolExpression {
	params := make([]pg.Expression, len(keys))
	for i, k := range keys {
		params[i] = param(k)
	}
	return p.pk.col.IN(params...)
}

func (p *modelPage) defaultOrder() []pg.OrderByClause {
	var out []pg.OrderByClause
	pkSeen := false
	for _, o := range p.ordering {
		out = append(out, orderClause(o.col, o.desc))
		pkSeen = pkSeen || o.col.pk
	}
	if !pkSeen {
		out = append(out, p.pk.col.ASC())
	}
	return out
}

func orderClause(c *column, desc bool) pg.OrderByClause {
	if desc {
		return c.col.DESC().NULLS_LAST()
	}
	return c.col.ASC().NULLS_LAST()
}

// valueOf is a field's value as a statement's parameter: nil for NULL.
func valueOf(v reflect.Value) any {
	if isNil(v) {
		return nil
	}
	return deref(v).Interface()
}
