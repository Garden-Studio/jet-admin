package jetadmin

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/go-jet/jet/v2/postgres"
)

// Model describes the admin pages of one table. M is the table's Jet model
// struct (model.Devices); Table is its generated builder (table.Devices).
// Every option but Table has a working default.
type Model[M any] struct {
	// Table is the generated builder. Its AllColumns, MutableColumns and
	// DefaultColumns are read by reflection.
	Table postgres.Table
	// Slug is the URL segment of the pages; the table's name by default.
	Slug string
	// Label and Plural name one row and the table: "Device", "Devices".
	// They default to the table's name, singularised and capitalised.
	Label, Plural string
	// Group is the section of the index page the model is listed under.
	Group string

	// ListDisplay are the columns of the list; by default the primary key
	// and the next few columns.
	ListDisplay []postgres.Column
	// ListFilter are the columns of the filter sidebar: a bool filters by
	// yes and no, a column with Choices by each choice, a timestamp or
	// date by today, the past 7 and 30 days and this year, other columns
	// by their distinct values when there are few enough of them.
	ListFilter []postgres.Column
	// SearchFields are matched, case-insensitively and as text, against
	// the search box: a row matches when one of them contains it.
	SearchFields []postgres.Column
	// Ordering is the list's default order; the primary key by default.
	// The column headers sort too.
	Ordering []Order
	// PageSize is the number of rows per list page; 50 by default.
	PageSize int

	// ReadOnly columns are shown on the form but never written by it.
	ReadOnly []postgres.Column
	// Hidden columns are never selected for display, shown nor written by
	// a form (a sealed credential).
	Hidden []postgres.Column
	// Fields are per-column options: label, help text, choices, widget.
	Fields []Field
	// ForeignKeys link a column to the row of another registered model.
	ForeignKeys []ForeignKey
	// Inlines list, on the change page, the rows of other models that
	// point at this one.
	Inlines []Inline
	// Extra are form inputs that are not columns (a password). Their
	// values reach BeforeSave.
	Extra []ExtraField
	// Computed are columns BeforeSave sets: written on every save even
	// when Hidden or ReadOnly (a password hash).
	Computed []postgres.Column

	// Display names a row in titles, links and the log, like Django's
	// __str__. By default "<Label> <primary key>".
	Display func(M) string
	// Permissions turns off adding, changing or deleting; everything is
	// allowed by default.
	Permissions Permissions
	// BeforeSave runs before Validate on every add and change, with the
	// values of Extra. Return FieldErrors to show the problem on the form.
	BeforeSave func(ctx context.Context, m *M, save Save) error
	// Validate runs before the row is inserted or updated. Return
	// FieldErrors to mark fields, any other error to show it above the
	// form.
	Validate func(ctx context.Context, m *M) error
}

// Save is what BeforeSave learns about the save in progress.
type Save struct {
	// Adding is true for a new row, false for a change.
	Adding bool
	// Extra holds the submitted values of Model.Extra, by name.
	Extra map[string]string
}

// Order is one column of a default ordering.
type Order struct {
	Column postgres.Column
	Desc   bool
}

// Asc orders by c, smallest first.
func Asc(c postgres.Column) Order { return Order{Column: c} }

// Desc orders by c, largest first.
func Desc(c postgres.Column) Order { return Order{Column: c, Desc: true} }

// Permissions turns off the actions a model does not allow. The zero value
// allows them all.
type Permissions struct {
	DisableAdd, DisableChange, DisableDelete bool
}

// ViewOnly is the Permissions of a table the admin only shows (a log).
var ViewOnly = Permissions{DisableAdd: true, DisableChange: true, DisableDelete: true}

// Field holds the options of one column's form field and list cell.
type Field struct {
	Column postgres.Column
	// Label is the field's name on the form and the list's header; the
	// column name in words by default.
	Label string
	// Help is shown under the input.
	Help string
	// Choices restrict the value to a list: a select on the form, a
	// filter of each choice, the choice's label in the list.
	Choices []Choice
	// Widget overrides the input chosen from the field's Go type.
	Widget Widget
}

// Choice is one allowed value of a column, in its text form.
type Choice struct {
	Value, Label string
}

// Choices makes Choices whose labels are their values.
func Choices(values ...string) []Choice {
	out := make([]Choice, len(values))
	for i, v := range values {
		out[i] = Choice{Value: v, Label: v}
	}
	return out
}

// Widget is the input of a form field.
type Widget int

const (
	// WidgetDefault picks the input from the field's Go type.
	WidgetDefault Widget = iota
	// Textarea is a multi-line text input.
	Textarea
	// JSON is a textarea whose value must be valid JSON; jsonb columns get
	// it without asking.
	JSON
	// Password is a masked input that never shows its value.
	Password
)

// ForeignKey links Column to the row of the model registered as Model (its
// slug) whose primary key it holds.
type ForeignKey struct {
	Column postgres.Column
	Model  string
}

// Inline lists, on a change page, the rows of the model registered as Model
// (its slug) whose Column holds this row's primary key, with an "add" link
// that fills Column in.
type Inline struct {
	Model  string
	Column postgres.Column
	// Limit is the number of rows shown; 20 by default.
	Limit int
}

// ExtraField is a form input that is not a column.
type ExtraField struct {
	Name, Label, Help string
	Widget            Widget
	// RequiredOnAdd refuses a blank value when adding a row.
	RequiredOnAdd bool
}

// FieldErrors is a validation failure: a message per field, keyed by column
// or Extra name. The key "" is a message for the whole form.
type FieldErrors map[string]string

func (e FieldErrors) Error() string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(e)) {
		if k == "" {
			parts = append(parts, e[k])
		} else {
			parts = append(parts, k+": "+e[k])
		}
	}
	return strings.Join(parts, "; ")
}

// Register adds the pages of a model to the site. It fails when the table
// or the model cannot be read, when the table has no single-column primary
// key, or when an option names a column the table does not have.
func Register[M any](s *Site, m Model[M]) error {
	page, err := newModelPage(s, m)
	if err != nil {
		name := "<nil>"
		if m.Table != nil {
			name = m.Table.TableName()
		}
		return fmt.Errorf("jetadmin: register %s: %w", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.models[page.slug]; taken {
		return fmt.Errorf("jetadmin: register %s: slug %q is taken", page.table.TableName(), page.slug)
	}
	s.models[page.slug] = page
	s.order = append(s.order, page.slug)
	return nil
}

// Column is a column of a generated table builder: table.Devices.Name.
type Column = postgres.Column
