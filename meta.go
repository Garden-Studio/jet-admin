package jetadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/google/uuid"
)

// modelPage is a registered Model with its type parameter erased: the
// pages work on reflect.Values of the model struct, and the closures keep
// the typed hooks.
type modelPage struct {
	site                *Site
	slug, label, plural string
	group               string
	table               postgres.Table
	typ                 reflect.Type

	columns     []*column // in the table's order
	byName      map[string]*column
	pk          *column
	listDisplay []*column
	listFilter  []*column
	search      []*column
	ordering    []orderBy
	pageSize    int
	inlines     []inline
	extra       []ExtraField
	perms       Permissions

	display    func(reflect.Value) string
	beforeSave func(context.Context, reflect.Value, Save) error
	validate   func(context.Context, reflect.Value) error
}

type orderBy struct {
	col  *column
	desc bool
}

type inline struct {
	slug   string
	column string
	limit  int
}

// valueKind is how a field's Go type reads and writes.
type valueKind int

const (
	kindOther valueKind = iota // shown with fmt, never written
	kindString
	kindBool
	kindInt
	kindUint
	kindFloat
	kindTime
	kindUUID
	kindJSON // json.RawMessage
	kindStrings
	kindBytes
)

// sqlKind is the column's type as Jet's builder declares it.
type sqlKind int

const (
	sqlOther sqlKind = iota
	sqlString
	sqlBool
	sqlInteger
	sqlFloat
	sqlDate
	sqlTimestamp
	sqlTimestampz
	sqlBytea
)

type column struct {
	col      postgres.Column
	name     string
	label    string
	help     string
	index    []int
	goType   reflect.Type
	base     reflect.Type // goType without its pointer
	nullable bool         // a pointer: an empty input is NULL
	kind     valueKind
	sql      sqlKind
	choices  []Choice
	widget   Widget
	fk       string // slug of the model the column points at

	pk, mutable, hasDefault bool
	hidden, readOnly        bool
	computed                bool
}

var (
	typeTime = reflect.TypeFor[time.Time]()
	typeUUID = reflect.TypeFor[uuid.UUID]()
	typeJSON = reflect.TypeFor[json.RawMessage]()
)

func kindOf(t reflect.Type) valueKind {
	switch t {
	case typeTime:
		return kindTime
	case typeUUID:
		return kindUUID
	case typeJSON:
		return kindJSON
	}
	switch t.Kind() {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == reflect.TypeFor[time.Duration]() {
			return kindOther
		}
		return kindInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return kindUint
	case reflect.Float32, reflect.Float64:
		return kindFloat
	case reflect.Slice:
		switch t.Elem().Kind() {
		case reflect.String:
			return kindStrings
		case reflect.Uint8:
			return kindBytes
		}
	}
	return kindOther
}

func sqlKindOf(c postgres.Column) sqlKind {
	switch c.(type) {
	case postgres.ColumnString:
		return sqlString
	case postgres.ColumnBool:
		return sqlBool
	case postgres.ColumnInteger:
		return sqlInteger
	case postgres.ColumnFloat:
		return sqlFloat
	case postgres.ColumnDate:
		return sqlDate
	case postgres.ColumnTimestamp:
		return sqlTimestamp
	case postgres.ColumnTimestampz:
		return sqlTimestampz
	case postgres.ColumnBytea:
		return sqlBytea
	}
	return sqlOther
}

// tableColumns reads AllColumns, MutableColumns and DefaultColumns of a
// generated builder. They are fields of the struct the builder points at,
// promoted from the unexported struct Jet embeds.
func tableColumns(t postgres.Table) (all, mutable, defaults postgres.ColumnList, err error) {
	v := reflect.ValueOf(t)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil, nil, errors.New("Table is nil")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, nil, nil, fmt.Errorf("Table is a %s, not a generated table builder", v.Type())
	}
	get := func(name string) (postgres.ColumnList, error) {
		f := v.FieldByName(name)
		if !f.IsValid() {
			return nil, fmt.Errorf("Table %s has no %s field: pass the builder Jet generated (table.Name)", v.Type(), name)
		}
		list, ok := f.Interface().(postgres.ColumnList)
		if !ok {
			return nil, fmt.Errorf("%s of %s is a %s, not a postgres.ColumnList", name, v.Type(), f.Type())
		}
		return list, nil
	}
	if all, err = get("AllColumns"); err != nil {
		return
	}
	if mutable, err = get("MutableColumns"); err != nil {
		return
	}
	defaults, err = get("DefaultColumns")
	return
}

func newModelPage[M any](s *Site, m Model[M]) (*modelPage, error) {
	if m.Table == nil {
		return nil, errors.New("Model.Table is required")
	}
	typ := reflect.TypeFor[M]()
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("the model is a %s, not a struct", typ)
	}
	all, mutable, defaults, err := tableColumns(m.Table)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("the table has no columns")
	}
	names := func(list postgres.ColumnList) map[string]bool {
		out := map[string]bool{}
		for _, c := range list {
			out[c.Name()] = true
		}
		return out
	}
	isMutable, isDefault := names(mutable), names(defaults)
	fields := modelFields(typ)
	pkFields := primaryKeyFields(typ)

	p := &modelPage{
		site:     s,
		table:    m.Table,
		typ:      typ,
		byName:   map[string]*column{},
		pageSize: m.PageSize,
		extra:    m.Extra,
		perms:    m.Permissions,
		group:    m.Group,
	}
	for _, c := range all {
		index, ok := fields[c.Name()]
		if !ok {
			return nil, fmt.Errorf("model %s has no field for column %q (tag it db:%q)", typ, c.Name(), c.Name())
		}
		goType := typ.FieldByIndex(index).Type
		base := goType
		if base.Kind() == reflect.Pointer {
			base = base.Elem()
		}
		col := &column{
			col:        c,
			name:       c.Name(),
			label:      words(c.Name()),
			index:      index,
			goType:     goType,
			base:       base,
			nullable:   goType.Kind() == reflect.Pointer,
			kind:       kindOf(base),
			sql:        sqlKindOf(c),
			pk:         pkFields[fmt.Sprint(index)],
			mutable:    isMutable[c.Name()],
			hasDefault: isDefault[c.Name()],
		}
		if col.label == "Id" {
			col.label = "ID"
		}
		p.columns = append(p.columns, col)
		p.byName[col.name] = col
	}
	for _, c := range p.columns {
		if c.pk {
			if p.pk != nil {
				return nil, errors.New("composite primary keys are not supported: register a table with a single-column primary key")
			}
			p.pk = c
		}
	}
	if p.pk == nil {
		return nil, fmt.Errorf("model %s has no primary key (a field tagged sql:\"primary_key\"); jetadmin needs one to address rows", typ)
	}
	switch p.pk.kind {
	case kindString, kindInt, kindUint, kindUUID:
	default:
		return nil, fmt.Errorf("primary key %q has type %s; jetadmin addresses rows by a text, integer or uuid key", p.pk.name, p.pk.goType)
	}
	if p.pk.nullable {
		return nil, fmt.Errorf("primary key %q is a pointer", p.pk.name)
	}

	p.slug = m.Slug
	if p.slug == "" {
		p.slug = m.Table.TableName()
	}
	if reservedSlugs[p.slug] || strings.ContainsAny(p.slug, "/?#") {
		return nil, fmt.Errorf("slug %q is reserved or not a path segment", p.slug)
	}
	p.plural = m.Plural
	if p.plural == "" {
		p.plural = words(m.Table.TableName())
	}
	p.label = m.Label
	if p.label == "" {
		p.label = singular(words(m.Table.TableName()))
	}

	lookup := func(option string, cols []postgres.Column) ([]*column, error) {
		var out []*column
		for _, c := range cols {
			if c == nil {
				return nil, fmt.Errorf("%s has a nil column", option)
			}
			col, ok := p.byName[c.Name()]
			if !ok || (c.TableName() != "" && c.TableName() != m.Table.TableName() && c.TableName() != m.Table.Alias()) {
				return nil, fmt.Errorf("%s names column %s.%s, which is not a column of %s", option, c.TableName(), c.Name(), m.Table.TableName())
			}
			out = append(out, col)
		}
		return out, nil
	}
	hidden, err := lookup("Hidden", m.Hidden)
	if err != nil {
		return nil, err
	}
	for _, c := range hidden {
		if c.pk {
			return nil, errors.New("the primary key cannot be Hidden")
		}
		c.hidden = true
	}
	readOnly, err := lookup("ReadOnly", m.ReadOnly)
	if err != nil {
		return nil, err
	}
	for _, c := range readOnly {
		c.readOnly = true
	}
	computed, err := lookup("Computed", m.Computed)
	if err != nil {
		return nil, err
	}
	for _, c := range computed {
		c.computed = true
	}
	labeled := map[*column]bool{}
	for _, f := range m.Fields {
		cols, err := lookup("Fields", []postgres.Column{f.Column})
		if err != nil {
			return nil, err
		}
		c := cols[0]
		if f.Label != "" {
			c.label = f.Label
			labeled[c] = true
		}
		c.help, c.choices, c.widget = f.Help, f.Choices, f.Widget
	}
	for _, fk := range m.ForeignKeys {
		cols, err := lookup("ForeignKeys", []postgres.Column{fk.Column})
		if err != nil {
			return nil, err
		}
		if fk.Model == "" {
			return nil, fmt.Errorf("foreign key %s names no model", cols[0].name)
		}
		cols[0].fk = fk.Model
		// The column shows the related row, so customer_id reads Customer.
		if trimmed, ok := strings.CutSuffix(cols[0].label, " id"); ok && !labeled[cols[0]] && trimmed != "" {
			cols[0].label = trimmed
		}
	}
	for _, in := range m.Inlines {
		if in.Model == "" || in.Column == nil {
			return nil, errors.New("an Inline needs Model and Column")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		p.inlines = append(p.inlines, inline{slug: in.Model, column: in.Column.Name(), limit: limit})
	}

	if p.listDisplay, err = lookup("ListDisplay", m.ListDisplay); err != nil {
		return nil, err
	}
	if len(p.listDisplay) == 0 {
		p.listDisplay = append(p.listDisplay, p.pk)
		for _, c := range p.columns {
			if len(p.listDisplay) == 6 {
				break
			}
			if !c.pk && !c.hidden && c.kind != kindBytes && c.kind != kindJSON {
				p.listDisplay = append(p.listDisplay, c)
			}
		}
	}
	if p.listFilter, err = lookup("ListFilter", m.ListFilter); err != nil {
		return nil, err
	}
	if p.search, err = lookup("SearchFields", m.SearchFields); err != nil {
		return nil, err
	}
	for _, list := range [][]*column{p.listDisplay, p.listFilter, p.search} {
		for _, c := range list {
			if c.hidden {
				return nil, fmt.Errorf("column %q is Hidden but listed, filtered or searched", c.name)
			}
		}
	}
	for _, o := range m.Ordering {
		cols, err := lookup("Ordering", []postgres.Column{o.Column})
		if err != nil {
			return nil, err
		}
		p.ordering = append(p.ordering, orderBy{col: cols[0], desc: o.Desc})
	}
	if len(p.ordering) == 0 {
		p.ordering = []orderBy{{col: p.pk, desc: p.pk.kind == kindInt}}
	}
	if p.pageSize <= 0 {
		p.pageSize = 50
	}
	for _, e := range m.Extra {
		if e.Name == "" || p.byName[e.Name] != nil || strings.HasPrefix(e.Name, "_") {
			return nil, fmt.Errorf("Extra field %q needs a name that is not a column and does not start with _", e.Name)
		}
	}

	if m.Display != nil {
		display := m.Display
		p.display = func(v reflect.Value) string { return display(v.Interface().(M)) }
	} else {
		p.display = func(v reflect.Value) string { return p.label + " " + p.pkText(v) }
	}
	if m.BeforeSave != nil {
		hook := m.BeforeSave
		p.beforeSave = func(ctx context.Context, v reflect.Value, save Save) error {
			return hook(ctx, v.Interface().(*M), save)
		}
	}
	if m.Validate != nil {
		hook := m.Validate
		p.validate = func(ctx context.Context, v reflect.Value) error {
			return hook(ctx, v.Interface().(*M))
		}
	}
	return p, nil
}

// primaryKeyFields are the index paths, printed, of the fields tagged
// sql:"primary_key", as Jet's generator tags them.
func primaryKeyFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	var walk func(t reflect.Type, path []int)
	walk = func(t reflect.Type, path []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			index := append(append([]int{}, path...), i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct && !isValue(f.Type) {
				walk(f.Type, index)
				continue
			}
			for _, part := range strings.Split(f.Tag.Get("sql"), ",") {
				if strings.TrimSpace(part) == "primary_key" {
					out[fmt.Sprint(index)] = true
				}
			}
		}
	}
	walk(t, nil)
	return out
}

// field is the struct field of c in the model value v (a struct, or a
// pointer to one).
func (c *column) field(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	return v.FieldByIndex(c.index)
}

// isJSON reports a column edited as JSON: a json.RawMessage, a jsonb
// column read as string, or a field given the JSON widget.
func (p *modelPage) isJSON(c *column) bool {
	return c.kind == kindJSON || c.widget == JSON || (c.kind == kindString && p.site.catalogInfo(p, c).json)
}

// writable reports a column a form may write: not the primary key on a
// change, not hidden, read-only, bytea or of a type the forms cannot
// parse, and not one the database always generates.
func (p *modelPage) writable(c *column, adding bool) bool {
	if c.hidden || c.readOnly || c.kind == kindBytes || c.kind == kindOther {
		return false
	}
	if p.site.catalogInfo(p, c).generated {
		return false
	}
	if c.pk {
		return adding
	}
	return c.mutable || c.hasDefault
}

// pkText is the text form of v's primary key, the one in URLs.
func (p *modelPage) pkText(v reflect.Value) string {
	return textOf(p.pk, p.pk.field(v))
}

// words makes a column or table name readable: created_at → Created at.
func words(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// singular is a plural English noun's singular, good enough for table
// names; Model.Label overrides it.
func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "ss"):
		return s
	case strings.HasSuffix(s, "s"):
		return s[:len(s)-1]
	}
	return s
}
