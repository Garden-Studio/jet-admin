package jetadmin

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// How rows reach a model struct. Jet's MODEL and QRM match a column to a
// field by Jet's own naming (user_id → UserID), which a model with other
// field names fails; the admin matches the way a pgx scanner tagged for
// pgx does instead:
//
//   - A field's column is its `db` tag, or its Go name in snake_case;
//     `db:"-"` skips it.
//   - An embedded struct's fields count as the outer struct's.
//   - A result column matches the field of its exact name, else the part
//     after the last dot: Jet labels a table's column "users.email".

// modelFields maps each column name of t to the index path of its field.
func modelFields(t reflect.Type) map[string][]int {
	fields := map[string][]int{}
	collectFields(t, nil, fields)
	return fields
}

// collectFields registers the direct fields before descending into
// embedded structs, so an outer field shadows an embedded one, as in Go.
func collectFields(t reflect.Type, path []int, fields map[string][]int) {
	type nested struct {
		t    reflect.Type
		path []int
	}
	var later []nested
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, tagged := field.Tag.Lookup("db")
		name, _, _ = strings.Cut(name, ",")
		if name == "-" {
			continue
		}
		index := append(append([]int{}, path...), i)
		if field.Anonymous && !tagged && field.Type.Kind() == reflect.Struct && !isValue(field.Type) {
			later = append(later, nested{field.Type, index})
			continue
		}
		if name == "" {
			name = snakeCase(field.Name)
		}
		if _, exists := fields[name]; !exists {
			fields[name] = index
		}
	}
	for _, n := range later {
		collectFields(n.t, n.path, fields)
	}
}

var sqlScanner = reflect.TypeFor[sql.Scanner]()

// isValue reports a struct pgx scans one column into.
func isValue(t reflect.Type) bool {
	return t == reflect.TypeFor[time.Time]() ||
		reflect.PointerTo(t).Implements(sqlScanner) ||
		strings.HasPrefix(t.PkgPath(), "github.com/jackc/pgx/v5/pgtype")
}

// snakeCase is the column a Go field name stands for: UserID → user_id,
// APITokens → api_tokens.
func snakeCase(name string) string {
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			// A boundary before an upper case letter that follows a lower
			// case one, or that starts a word after an acronym (IDList).
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1])) {
				out.WriteByte('_')
			}
			out.WriteRune(unicode.ToLower(r))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// scanRows reads every row into a new value of t (a struct type), through
// the fields of fields.
func scanRows(rows pgx.Rows, t reflect.Type, fields map[string][]int) ([]reflect.Value, error) {
	defer rows.Close()
	descriptions := rows.FieldDescriptions()
	var out []reflect.Value
	for rows.Next() {
		row := reflect.New(t).Elem()
		targets := make([]any, len(descriptions))
		for i, d := range descriptions {
			path, ok := fields[d.Name]
			if !ok {
				if dot := strings.LastIndexByte(d.Name, '.'); dot >= 0 {
					path, ok = fields[d.Name[dot+1:]]
				}
			}
			if !ok {
				return nil, fmt.Errorf("no field of %s for column %q", t, d.Name)
			}
			targets[i] = row.FieldByIndex(path).Addr().Interface()
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
