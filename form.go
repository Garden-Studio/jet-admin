package jetadmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/garden-studio/jet-admin/internal/ui"
)

// The add and change pages. A form posts back to its own address; a row
// that does not validate comes back with its errors (patched in place for
// Datastar, a 422 page without it), a saved one redirects.

const maxFormBytes = 1 << 20

// formFields lays out the form of row (a zero row when adding). submitted
// are the values to show instead of row's, after a failed save or as the
// initial values of an add link.
func (p *modelPage) formFields(ctx context.Context, row reflect.Value, adding, canSave bool, submitted url.Values, posted bool, errs FieldErrors) []ui.FormField {
	var fields []ui.FormField
	for _, c := range p.columns {
		if c.hidden {
			continue
		}
		info := p.site.catalogInfo(p, c)
		writable := canSave && p.writable(c, adding)
		if adding && !writable {
			// The database fills it in; nothing to show yet.
			continue
		}
		f := ui.FormField{Name: c.name, Label: c.label, Help: c.help, Error: errs[c.name]}
		if !writable {
			f.Input = ui.InputReadOnly
			f.Cell = p.cell(c, c.field(row), true)
			if c.fk != "" && !isNil(c.field(row)) {
				p.relate(ctx, c, textOf(c, c.field(row)), &f.Cell)
			}
			fields = append(fields, f)
			continue
		}
		value := p.inputText(c, c.field(row))
		checked := c.kind == kindBool && !c.nullable && deref(c.field(row)).Bool()
		if adding {
			// A new row's zero values (a nil uuid, 0) are no one's choice:
			// the inputs start blank, or at the column's constant default.
			value, checked = "", false
		}
		if adding && !posted {
			if lit, ok := literalDefault(info.defaultExpr); ok {
				value, checked = lit, lit == "true"
			}
		}
		if vs, ok := submitted[c.name]; ok {
			value = vs[0]
			checked = value != "" && value != "false"
		} else if posted {
			// Only an unchecked checkbox is missing from a posted form.
			value, checked = "", false
		}
		f.Value, f.Checked = value, checked
		defaulted := adding && (c.hasDefault || info.defaulted || c.pk)
		required := !c.nullable && !defaulted
		switch {
		case len(c.choices) > 0:
			f.Input = ui.InputSelect
			if c.nullable || defaulted || value == "" {
				f.Options = append(f.Options, ui.Option{Value: "", Label: "---------", Selected: value == ""})
			}
			for _, choice := range c.choices {
				f.Options = append(f.Options, ui.Option{Value: choice.Value, Label: choice.Label, Selected: choice.Value == value})
			}
			f.Required = required
		case p.isJSON(c):
			f.Input, f.Mono, f.Required = ui.InputTextarea, true, required
			f.Note = "Must be valid JSON."
		case c.kind == kindString:
			switch c.widget {
			case Textarea:
				f.Input = ui.InputTextarea
			case Password:
				f.Input, f.Value = ui.InputPassword, ""
			default:
				f.Input = ui.InputText
			}
		case c.kind == kindBool && !c.nullable:
			f.Input = ui.InputCheckbox
		case c.kind == kindBool:
			f.Input = ui.InputNullBool
			f.Options = []ui.Option{
				{Value: "", Label: "Unknown", Selected: value == ""},
				{Value: "true", Label: "Yes", Selected: value == "true"},
				{Value: "false", Label: "No", Selected: value == "false"},
			}
		case c.kind == kindInt || c.kind == kindUint:
			f.Input, f.Required = ui.InputNumber, required
		case c.kind == kindFloat:
			f.Input, f.Required = ui.InputDecimal, required
		case c.kind == kindTime && c.sql == sqlDate:
			f.Input, f.Required = ui.InputDate, required
		case c.kind == kindTime:
			f.Input, f.Required = ui.InputDateTime, required
			f.Note = "UTC."
		case c.kind == kindUUID:
			f.Input, f.Mono, f.Required = ui.InputText, true, required
		case c.kind == kindStrings:
			f.Input = ui.InputTextarea
			f.Note = "One per line."
		}
		switch {
		case defaulted && c.pk:
			f.Note = strings.TrimSpace(f.Note + " Leave blank to let the database choose.")
		case defaulted && f.Input != ui.InputCheckbox && f.Input != ui.InputSelect:
			f.Note = strings.TrimSpace(f.Note + " Leave blank for the default.")
		case c.nullable && f.Input != ui.InputNullBool && f.Input != ui.InputSelect:
			f.Note = strings.TrimSpace(f.Note + " Leave blank for none.")
		}
		if c.fk != "" && value != "" && f.Error == "" {
			var related ui.Cell
			if p.relate(ctx, c, value, &related) {
				f.Related = &related
			}
		}
		fields = append(fields, f)
	}
	if canSave {
		for _, e := range p.extra {
			f := ui.FormField{Name: e.Name, Label: e.Label, Help: e.Help, Error: errs[e.Name], Input: ui.InputText}
			if f.Label == "" {
				f.Label = words(e.Name)
			}
			switch e.Widget {
			case Password:
				f.Input = ui.InputPassword
			case Textarea, JSON:
				f.Input = ui.InputTextarea
			}
			if f.Input != ui.InputPassword {
				f.Value = submitted.Get(e.Name)
			}
			f.Required = adding && e.RequiredOnAdd
			fields = append(fields, f)
		}
	}
	return fields
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// relate fills cell with a link to the row key of the model c points at,
// and its name. It reports whether there is such a row.
func (p *modelPage) relate(ctx context.Context, c *column, key string, cell *ui.Cell) bool {
	target, ok := p.site.model(c.fk)
	if !ok {
		return false
	}
	names := p.relatedLabels(ctx, c, []string{key})
	name, found := names[key]
	if !found {
		return false
	}
	// The row by its name, the key in the tooltip, as in the lists.
	cell.Kind, cell.Text, cell.Title, cell.Mono = 0, name, key, false
	cell.Href = target.changeURL(key)
	return true
}

// literalDefault is the value of a column default that is a constant
// ('free'::text, true, 0), to show as the add form's initial value.
func literalDefault(expr string) (string, bool) {
	switch expr {
	case "":
		return "", false
	case "true", "false":
		return expr, true
	}
	if m := quotedDefault.FindStringSubmatch(expr); m != nil {
		return strings.ReplaceAll(m[1], "''", "'"), !strings.HasPrefix(m[1], "{") && !strings.HasPrefix(m[1], "[")
	}
	if numericDefault.MatchString(expr) {
		return strings.Trim(expr, "()"), true
	}
	return "", false
}

var (
	quotedDefault  = regexp.MustCompile(`^'((?:[^']|'')*)'::[a-z ]+$`)
	numericDefault = regexp.MustCompile(`^\(?-?[0-9]+(\.[0-9]+)?\)?$`)
)

func (s *Site) addForm(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	if p.perms.DisableAdd {
		s.forbidden(w, r, u, p.plural+" cannot be added here.")
		return
	}
	// An add link may fill fields in: ?customer_id=… from an inline.
	var initial url.Values
	for k, vs := range r.URL.Query() {
		if c, ok := p.byName[k]; ok && !c.hidden {
			if initial == nil {
				initial = url.Values{}
			}
			initial[k] = vs
		}
	}
	row := reflect.New(p.typ).Elem()
	s.renderForm(w, r, u, p, row, "", true, http.StatusOK, initial, false, nil)
}

func (s *Site) changeForm(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	key, row, ok := s.loadRow(w, r, u, p, false)
	if !ok {
		return
	}
	s.renderForm(w, r, u, p, row, key, false, http.StatusOK, nil, false, nil)
}

// loadRow reads the row of the URL's key, answering 404 when there is
// none.
func (s *Site) loadRow(w http.ResponseWriter, r *http.Request, u User, p *modelPage, all bool) (string, reflect.Value, bool) {
	key := r.PathValue("pk")
	parsed, err := p.parseKey(p.pk, key)
	if err != nil {
		s.notFound(w, r, u)
		return "", reflect.Value{}, false
	}
	row, found, err := p.get(r.Context(), s.db, parsed, all)
	if err != nil {
		s.fail(w, r, u, "read the "+strings.ToLower(p.label), err)
		return "", reflect.Value{}, false
	}
	if !found {
		s.notFound(w, r, u)
		return "", reflect.Value{}, false
	}
	return key, row, true
}

func (s *Site) renderForm(w http.ResponseWriter, r *http.Request, u User, p *modelPage, row reflect.Value, key string, adding bool, status int, submitted url.Values, posted bool, errs FieldErrors) {
	ctx := r.Context()
	canSave := !p.perms.DisableChange || adding
	page := ui.FormPage{
		Adding:    adding,
		CanSave:   canSave,
		FormError: errs[""],
		SaveLabel: "Save",
	}
	var crumb ui.Crumb
	if adding {
		page.Heading = "Add " + strings.ToLower(p.label)
		page.Action = p.addURL()
		crumb = ui.Crumb{Label: page.Heading}
	} else {
		name := p.display(row)
		page.Heading = name
		if !canSave {
			page.Heading = "View " + strings.ToLower(p.label) + ": " + name
		}
		page.Action = p.changeURL(key)
		crumb = ui.Crumb{Label: name}
		if !p.perms.DisableDelete {
			page.DeleteURL = p.deleteURL(key)
		}
		if s.auditLog {
			page.HistoryURL = p.historyURL(key)
		}
		page.Inlines = s.inlines(ctx, p, row)
	}
	page.Fields = p.formFields(ctx, row, adding, canSave, submitted, posted, errs)
	if fromDatastar(r) {
		s.patch(w, r, ui.FormFragment(page))
		return
	}
	page.Chrome = s.chrome(w, r, u, crumb.Label, p.slug, ui.Crumb{Label: p.plural, URL: p.listURL()}, crumb)
	s.render(w, r, status, ui.Form(page))
}

// inlines are the rows of other models pointing at row.
func (s *Site) inlines(ctx context.Context, p *modelPage, row reflect.Value) []ui.InlineList {
	var out []ui.InlineList
	key := valueOf(p.pk.field(row))
	keyText := p.pkText(row)
	for _, in := range p.inlines {
		child, ok := s.model(in.slug)
		if !ok {
			s.logger.WarnContext(ctx, "jetadmin: inline of an unregistered model", "model", p.slug, "inline", in.slug)
			continue
		}
		col, ok := child.byName[in.column]
		if !ok || col.hidden {
			s.logger.WarnContext(ctx, "jetadmin: inline column is not a shown column", "inline", in.slug, "column", in.column)
			continue
		}
		lq := listQuery{where: []pg.BoolExpression{equals(col.col, key)}, order: child.defaultOrder()}
		total, err := child.count(ctx, s.db, lq)
		if err != nil {
			s.logger.ErrorContext(ctx, "jetadmin: count inline rows", "inline", in.slug, "error", err)
			continue
		}
		rows, err := child.page(ctx, s.db, lq, int64(in.limit), 0)
		if err != nil {
			s.logger.ErrorContext(ctx, "jetadmin: list inline rows", "inline", in.slug, "error", err)
			continue
		}
		var cols []*column
		for _, c := range child.listDisplay {
			if c != col {
				cols = append(cols, c)
			}
		}
		list := ui.InlineList{
			Plural: child.plural,
			Rows:   child.listRows(ctx, rows, cols),
			Total:  total,
			AllURL: child.listURL() + "?" + url.Values{filterPrefix + col.name: {keyText}}.Encode(),
		}
		for _, c := range cols {
			list.Columns = append(list.Columns, c.label)
		}
		if !child.perms.DisableAdd {
			list.AddURL = child.addURL() + "?" + url.Values{col.name: {keyText}}.Encode()
		}
		out = append(out, list)
	}
	return out
}

func (s *Site) addSubmit(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	if p.perms.DisableAdd {
		s.forbidden(w, r, u, p.plural+" cannot be added here.")
		return
	}
	s.save(w, r, u, p, "", reflect.New(p.typ).Elem(), true)
}

func (s *Site) changeSubmit(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	if p.perms.DisableChange {
		s.forbidden(w, r, u, p.plural+" cannot be changed here.")
		return
	}
	key, row, ok := s.loadRow(w, r, u, p, true)
	if !ok {
		return
	}
	s.save(w, r, u, p, key, row, false)
}

// save applies the posted form to row and writes it.
func (s *Site) save(w http.ResponseWriter, r *http.Request, u User, p *modelPage, key string, row reflect.Value, adding bool) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	old := reflect.New(p.typ).Elem()
	old.Set(row)
	edited := reflect.New(p.typ)
	edited.Elem().Set(row)

	errs := FieldErrors{}
	var written []*column
	for _, c := range p.columns {
		if !p.writable(c, adding) {
			continue
		}
		info := s.catalogInfo(p, c)
		value, blank, err := p.parse(c, form.Get(c.name))
		if adding && blank && (c.hasDefault || info.defaulted || c.pk) {
			// Left to the database's default: a generated key, now().
			continue
		}
		if err != nil {
			errs[c.name] = err.Error()
			continue
		}
		c.field(edited).Set(value)
		written = append(written, c)
	}
	extra := map[string]string{}
	for _, e := range p.extra {
		v := form.Get(e.Name)
		extra[e.Name] = v
		if adding && e.RequiredOnAdd && strings.TrimSpace(v) == "" {
			errs[e.Name] = errBlank.Error()
		}
	}
	if len(errs) == 0 && p.beforeSave != nil {
		addErrors(errs, p.beforeSave(ctx, edited, Save{Adding: adding, Extra: extra}))
	}
	if len(errs) == 0 && p.validate != nil {
		addErrors(errs, p.validate(ctx, edited))
	}
	for _, c := range p.columns {
		if c.computed && !c.pk && !contains(names(written), c.name) {
			written = append(written, c)
		}
	}
	if len(errs) > 0 {
		s.renderForm(w, r, u, p, old, key, adding, http.StatusUnprocessableEntity, form, true, errs)
		return
	}

	values := make([]any, len(written))
	for i, c := range written {
		values[i] = valueOf(c.field(edited))
	}
	var saved reflect.Value
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if adding {
			inserted, err := p.insert(ctx, tx, written, values)
			if err != nil {
				return err
			}
			saved = inserted
			key = p.pkText(saved)
			return s.logAction(ctx, tx, u, p, key, p.display(saved), actionAdd, "Added.")
		}
		parsed, err := p.parseKey(p.pk, key)
		if err != nil {
			return err
		}
		found, err := p.update(ctx, tx, parsed, written, values)
		if err != nil {
			return err
		}
		if !found {
			return pgx.ErrNoRows
		}
		saved = edited.Elem()
		var changed []string
		for _, c := range written {
			if !reflect.DeepEqual(valueOf(c.field(old)), valueOf(c.field(saved))) {
				changed = append(changed, strings.ToLower(c.label))
			}
		}
		return s.logAction(ctx, tx, u, p, key, p.display(saved), actionChange, changeMessage(changed))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		s.notFound(w, r, u)
		return
	}
	if err != nil {
		if dbErrs, ok := p.databaseErrors(err); ok {
			s.renderForm(w, r, u, p, old, key, adding, http.StatusUnprocessableEntity, form, true, dbErrs)
			return
		}
		s.fail(w, r, u, "save the "+strings.ToLower(p.label), err)
		return
	}

	verb := "changed"
	if adding {
		verb = "added"
	}
	text := "The " + strings.ToLower(p.label) + " “" + p.display(saved) + "” was " + verb + "."
	location := p.listURL()
	switch {
	case form.Get("_continue") != "":
		location = p.changeURL(key)
		text += " You may edit it again below."
	case form.Get("_addanother") != "":
		location = p.addURL()
		text += " You may add another " + strings.ToLower(p.label) + " below."
	}
	s.redirectWith(w, r, location, ui.Flash{Text: text})
}

func names(cols []*column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.name
	}
	return out
}

// addErrors adds a hook's error to errs: its fields when it is
// FieldErrors, the whole form's message otherwise.
func addErrors(errs FieldErrors, err error) {
	if err == nil {
		return
	}
	var fe FieldErrors
	if errors.As(err, &fe) {
		for k, v := range fe {
			errs[k] = v
		}
		if len(fe) > 0 {
			return
		}
	}
	errs[""] = err.Error()
}

// inTx runs fn in a transaction, committed when fn succeeds.
func (s *Site) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
