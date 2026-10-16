package jetadmin

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	pg "github.com/go-jet/jet/v2/postgres"

	"github.com/garden-studio/jet-admin/internal/ui"
)

// The list page and its address. Every state of a list is in its query
// string, so a filtered page reloads, shares and works without JavaScript:
//
//	q=ann                    the search
//	f_plan=pro               rows whose plan is pro (any column, in its text form)
//	f_shipped_at__since=7d   timestamps since today, 7d, 30d or year
//	f_notes__isnull=true     NULL (true) or not (false)
//	o=-created_at            the order; - for descending
//	p=2                      the page

const filterPrefix = "f_"

type listState struct {
	query   string
	filters url.Values // by parameter name, f_ prefix included
	order   string     // "name" or "-name", "" for the default
	page    int
}

func (p *modelPage) readListState(values url.Values) listState {
	st := listState{
		query:   strings.TrimSpace(values.Get("q")),
		filters: url.Values{},
		order:   values.Get("o"),
		page:    max(1, atoi(values.Get("p"))),
	}
	for name, vs := range values {
		if strings.HasPrefix(name, filterPrefix) && len(vs) > 0 && vs[0] != "" {
			if _, _, ok := p.filterColumn(name); ok {
				st.filters.Set(name, vs[0])
			}
		}
	}
	if len(p.search) == 0 {
		st.query = ""
	}
	return st
}

// filterColumn is the column and operation of a filter parameter.
func (p *modelPage) filterColumn(param string) (*column, string, bool) {
	name, op, _ := strings.Cut(strings.TrimPrefix(param, filterPrefix), "__")
	c, ok := p.byName[name]
	if !ok || c.hidden {
		return nil, "", false
	}
	switch op {
	case "", "since", "isnull":
		return c, op, true
	}
	return nil, "", false
}

// url is the list's address for st.
func (p *modelPage) stateURL(st listState) string {
	v := url.Values{}
	if st.query != "" {
		v.Set("q", st.query)
	}
	for name, vs := range st.filters {
		v[name] = vs
	}
	if st.order != "" {
		v.Set("o", st.order)
	}
	if st.page > 1 {
		v.Set("p", strconv.Itoa(st.page))
	}
	if len(v) == 0 {
		return p.listURL()
	}
	return p.listURL() + "?" + v.Encode()
}

// with is st with one filter set (value "" removes it), on the first page.
func (st listState) withFilter(param, value string) listState {
	next := st
	next.filters = url.Values{}
	for k, v := range st.filters {
		next.filters[k] = v
	}
	// One filter per column: a new choice replaces the others.
	name, _, _ := strings.Cut(strings.TrimPrefix(param, filterPrefix), "__")
	for k := range next.filters {
		if other, _, _ := strings.Cut(strings.TrimPrefix(k, filterPrefix), "__"); other == name {
			delete(next.filters, k)
		}
	}
	if value != "" {
		next.filters.Set(param, value)
	}
	next.page = 1
	return next
}

// sorted resolves the order parameter, falling back to the default.
func (p *modelPage) sorted(st listState) (*column, bool, bool) {
	name, desc := strings.CutPrefix(st.order, "-")
	if c, ok := p.byName[name]; ok && p.sortable(c) {
		return c, desc, true
	}
	return nil, false, false
}

func (p *modelPage) sortable(c *column) bool {
	for _, d := range p.listDisplay {
		if d == c {
			return c.kind != kindBytes
		}
	}
	return false
}

func (p *modelPage) buildListQuery(st listState) listQuery {
	var lq listQuery
	if st.query != "" {
		if cond := p.searchCondition(st.query); cond != nil {
			lq.where = append(lq.where, cond)
		}
	}
	for _, param := range slices.Sorted(maps.Keys(st.filters)) {
		c, op, _ := p.filterColumn(param)
		lq.where = append(lq.where, p.filterCondition(c, op, st.filters.Get(param)))
	}
	if c, desc, ok := p.sorted(st); ok {
		lq.order = append(lq.order, orderClause(c, desc))
		if !c.pk {
			lq.order = append(lq.order, p.pk.col.ASC())
		}
	} else {
		lq.order = p.defaultOrder()
	}
	return lq
}

// never matches no row: a filter value that cannot be one of the column's.
var never = pg.RawBool("false")

func (p *modelPage) filterCondition(c *column, op, value string) pg.BoolExpression {
	switch op {
	case "isnull":
		if value == "true" {
			return c.col.IS_NULL()
		}
		return c.col.IS_NOT_NULL()
	case "since":
		var since pg.Expression
		switch value {
		case "today":
			since = pg.DATE_TRUNC(pg.DAY, pg.NOW(), "UTC")
		case "7d":
			since = pg.NOW().SUB(pg.INTERVAL(7, pg.DAY))
		case "30d":
			since = pg.NOW().SUB(pg.INTERVAL(30, pg.DAY))
		case "year":
			since = pg.DATE_TRUNC(pg.YEAR, pg.NOW(), "UTC")
		default:
			return never
		}
		return pg.BoolExp(pg.BinaryOperator(c.col, since, ">="))
	}
	switch c.kind {
	case kindString, kindBool, kindInt, kindUint, kindFloat, kindUUID:
		key, err := p.parseKey(c, value)
		if err != nil {
			return never
		}
		return equals(c.col, key)
	}
	return pg.BoolExp(pg.BinaryOperator(asText(c.col), pg.String(value), "="))
}

// distinctLimit bounds the values a text filter lists: past it, the
// column is better searched.
const distinctLimit = 50

func (p *modelPage) filters(ctx context.Context, st listState) ([]ui.Filter, error) {
	var out []ui.Filter
	for _, c := range p.listFilter {
		f := ui.Filter{Label: strings.ToLower(c.label)}
		active := func(param, value string) bool { return st.filters.Get(param) == value }
		anyActive := false
		for k := range st.filters {
			if fc, _, _ := p.filterColumn(k); fc == c {
				anyActive = true
			}
		}
		option := func(label, param, value string) {
			f.Options = append(f.Options, ui.FilterOption{
				Label:  label,
				URL:    p.stateURL(st.withFilter(param, value)),
				Active: value != "" && active(param, value),
			})
		}
		f.Options = append(f.Options, ui.FilterOption{Label: "All", URL: p.stateURL(st.withFilter(filterPrefix+c.name, "")), Active: !anyActive})
		exact := filterPrefix + c.name
		isNull := exact + "__isnull"
		switch {
		case len(c.choices) > 0:
			for _, choice := range c.choices {
				option(choice.Label, exact, choice.Value)
			}
		case c.kind == kindBool:
			option("Yes", exact, "true")
			option("No", exact, "false")
		case c.kind == kindTime:
			since := exact + "__since"
			option("Today", since, "today")
			option("Past 7 days", since, "7d")
			option("Past 30 days", since, "30d")
			option("This year", since, "year")
		default:
			values, err := p.distinctValues(ctx, p.site.db, c, distinctLimit+1)
			if err != nil {
				return nil, err
			}
			if len(values) > distinctLimit {
				f.Note = "Too many values to list; search instead."
				values = nil
				if v := st.filters.Get(exact); v != "" {
					values = []string{v}
				}
			}
			labels := p.relatedLabels(ctx, c, values)
			for _, v := range values {
				label := v
				if l, ok := labels[v]; ok {
					label = l
				}
				option(label, exact, v)
			}
		}
		if c.nullable || (c.kind == kindJSON) {
			if c.kind == kindTime {
				option("No date", isNull, "true")
				option("Has date", isNull, "false")
			} else if c.kind == kindBool {
				option("Unknown", isNull, "true")
			} else {
				option("Empty", isNull, "true")
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// relatedLabels names the rows a foreign key column's values point at,
// by their text form.
func (p *modelPage) relatedLabels(ctx context.Context, c *column, values []string) map[string]string {
	if c.fk == "" || len(values) == 0 {
		return nil
	}
	related, ok := p.site.model(c.fk)
	if !ok {
		return nil
	}
	names, err := related.names(ctx, values)
	if err != nil {
		p.site.logger.WarnContext(ctx, "jetadmin: name related rows", "model", related.slug, "error", err)
		return nil
	}
	return names
}

// names are the Display of the rows of keys, by key.
func (p *modelPage) names(ctx context.Context, keys []string) (map[string]string, error) {
	var parsed []any
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		if v, err := p.parseKey(p.pk, k); err == nil {
			parsed = append(parsed, v)
		}
	}
	rows, err := p.byKeys(ctx, p.site.db, parsed)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[p.pkText(row)] = p.display(row)
	}
	return out, nil
}

func (s *Site) list(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	ctx := r.Context()
	st := p.readListState(r.URL.Query())
	lq := p.buildListQuery(st)
	total, err := p.count(ctx, s.db, lq)
	if err != nil {
		s.fail(w, r, u, "count the "+strings.ToLower(p.plural), err)
		return
	}
	pageSize := int64(p.pageSize)
	pages := max(1, int((total+pageSize-1)/pageSize))
	st.page = min(st.page, pages)
	rows, err := p.page(ctx, s.db, lq, pageSize, int64(st.page-1)*pageSize)
	if err != nil {
		s.fail(w, r, u, "list the "+strings.ToLower(p.plural), err)
		return
	}
	filters, err := p.filters(ctx, st)
	if err != nil {
		s.fail(w, r, u, "read the filters", err)
		return
	}

	page := ui.ListPage{
		Chrome:      s.chrome(w, r, u, p.plural, p.slug, ui.Crumb{Label: p.plural}),
		Plural:      p.plural,
		Label:       strings.ToLower(p.label),
		ListURL:     p.listURL(),
		Filters:     filters,
		Total:       total,
		CanDelete:   !p.perms.DisableDelete,
		ResultLabel: countLabel(total, p),
	}
	if !p.perms.DisableAdd {
		page.AddURL = p.addURL()
	}
	if len(p.search) > 0 {
		var labels []string
		for _, c := range p.search {
			labels = append(labels, strings.ToLower(c.label))
		}
		page.Search = &ui.Search{Value: st.query, Placeholder: "Search by " + strings.Join(labels, ", ")}
	}
	for _, name := range slices.Sorted(maps.Keys(st.filters)) {
		page.Hidden = append(page.Hidden, ui.Param{Name: name, Value: st.filters.Get(name)})
	}
	if st.order != "" {
		page.Hidden = append(page.Hidden, ui.Param{Name: "o", Value: st.order})
	}
	if st.query != "" || len(st.filters) > 0 {
		page.ClearURL = p.listURL()
	}

	sortCol, sortDesc, explicit := p.sorted(st)
	if !explicit {
		sortCol, sortDesc = p.ordering[0].col, p.ordering[0].desc
	}
	for _, c := range p.listDisplay {
		col := ui.ListColumn{Label: c.label, Numeric: c.kind == kindInt || c.kind == kindUint || c.kind == kindFloat}
		if p.sortable(c) {
			next := st
			next.page = 1
			next.order = c.name
			if c == sortCol {
				col.Sorted = "asc"
				if sortDesc {
					col.Sorted = "desc"
				} else {
					next.order = "-" + c.name
				}
			}
			col.SortURL = p.stateURL(next)
		}
		page.Columns = append(page.Columns, col)
	}
	page.Rows = p.listRows(ctx, rows, p.listDisplay)
	page.Pager = makePager(st.page, pages, func(n int) string {
		next := st
		next.page = n
		return p.stateURL(next)
	})

	if fromDatastar(r) {
		s.patchURL(w, r, p.stateURL(st), ui.ListFragment(page))
		return
	}
	s.render(w, r, http.StatusOK, ui.List(page))
}

func countLabel(n int64, p *modelPage) string {
	if n == 1 {
		return "1 " + strings.ToLower(p.label)
	}
	return strconv.FormatInt(n, 10) + " " + strings.ToLower(p.plural)
}

// listRows are rows drawn as cells of cols, a foreign key linking to the
// row it points at.
func (p *modelPage) listRows(ctx context.Context, rows []reflect.Value, cols []*column) []ui.ListRow {
	related := map[*column]map[string]string{}
	for _, c := range cols {
		if c.fk == "" {
			continue
		}
		var keys []string
		for _, row := range rows {
			if v := c.field(row); !isNil(v) {
				keys = append(keys, textOf(c, v))
			}
		}
		related[c] = p.relatedLabels(ctx, c, keys)
	}
	out := make([]ui.ListRow, 0, len(rows))
	for _, row := range rows {
		key := p.pkText(row)
		lr := ui.ListRow{PK: key, URL: p.changeURL(key), Label: p.display(row)}
		for _, c := range cols {
			v := c.field(row)
			cell := p.cell(c, v, false)
			if c.fk != "" && !isNil(v) {
				if target, ok := p.site.model(c.fk); ok {
					// The related row by its name, as Django shows it; the
					// key stays in the tooltip.
					text := textOf(c, v)
					cell.Href = target.changeURL(text)
					if name, ok := related[c][text]; ok {
						cell.Text, cell.Title, cell.Mono = name, text, false
					}
				}
			}
			lr.Cells = append(lr.Cells, cell)
		}
		out = append(out, lr)
	}
	return out
}
