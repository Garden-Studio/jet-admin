package jetadmin

import (
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Constraints live in the database, so its refusals are what the form
// shows: each on the field it is about when the error names one, else above
// the form, with the constraint's name for whoever reads the schema.

// keyDetail is the column list of a unique or foreign key violation's
// detail: Key (email)=(ann@example.com) already exists.
var keyDetail = regexp.MustCompile(`^Key \(([^)]+)\)=`)

// databaseErrors turns a refusal of the database into form errors, and
// reports whether err was one.
func (p *modelPage) databaseErrors(err error) (FieldErrors, bool) {
	return p.databaseErrorsFor(err, p.table.TableName())
}

func (p *modelPage) databaseErrorsFor(err error, tableName string) (FieldErrors, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, false
	}
	field := p.errorField(pgErr, tableName)
	label := "this value"
	if c, ok := p.byName[field]; ok {
		label = "this " + strings.ToLower(c.label)
	}
	constraint := ""
	if pgErr.ConstraintName != "" {
		constraint = " (constraint " + pgErr.ConstraintName + ")"
	}
	var message string
	switch pgErr.Code {
	case "23505": // unique_violation
		message = "Another " + strings.ToLower(p.label) + " already has " + label + "." + constraint
	case "23503": // foreign_key_violation
		message = "There is no such related row." + constraint
	case "23514": // check_violation
		message = "The value is not allowed" + constraint + "."
		if field == "" {
			message = "The values are not allowed together" + constraint + "."
		}
	case "23502": // not_null_violation
		message = "This field is required."
	case "23P01": // exclusion_violation
		message = "This conflicts with another " + strings.ToLower(p.label) + "." + constraint
	case "22001": // string_data_right_truncation
		message = "The value is too long."
	case "22003": // numeric_value_out_of_range
		message = "The number is out of range."
	case "22P02", "22007", "22008", "22023": // invalid input syntax, datetime format, field overflow, invalid parameter
		message = "The value is not valid: " + pgErr.Message + "."
	case "428C9": // generated_always
		message = "The database generates this value."
	default:
		return nil, false
	}
	return FieldErrors{field: message}, true
}

// errorField is the shown column a database error is about, or "".
func (p *modelPage) errorField(pgErr *pgconn.PgError, tableName string) string {
	shown := func(name string) string {
		if c, ok := p.byName[name]; ok && !c.hidden {
			return name
		}
		return ""
	}
	if pgErr.ColumnName != "" {
		return shown(pgErr.ColumnName)
	}
	if m := keyDetail.FindStringSubmatch(pgErr.Detail); m != nil && !strings.Contains(m[1], ",") {
		return shown(strings.TrimSpace(m[1]))
	}
	// A check constraint named the way PostgreSQL names an inline one:
	// customers_plan_check is about plan.
	if name, ok := strings.CutPrefix(pgErr.ConstraintName, tableName+"_"); ok {
		if col, ok := strings.CutSuffix(name, "_check"); ok {
			return shown(col)
		}
	}
	return ""
}

// deleteRefusal is why the database refused to delete, or "".
func deleteRefusal(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		text := "Other rows still refer to it"
		if pgErr.TableName != "" {
			text = "Rows of " + pgErr.TableName + " still refer to it"
		}
		if pgErr.ConstraintName != "" {
			text += " (constraint " + pgErr.ConstraintName + ")"
		}
		return text + "."
	}
	return ""
}
