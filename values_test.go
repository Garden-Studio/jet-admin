package jetadmin

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"ID": "id", "UserID": "user_id", "APITokens": "api_tokens", "CreatedAt": "created_at",
		"IsActive": "is_active", "HTTPServer": "http_server", "Field2Name": "field2_name",
	} {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[string][2]string{
		"customers": {"Customers", "Customer"}, "push_logs": {"Push logs", "Push log"},
		"categories": {"Categories", "Category"}, "addresses": {"Addresses", "Address"},
		"watcher_states": {"Watcher states", "Watcher state"},
	} {
		if got := words(in); got != want[0] {
			t.Errorf("words(%q) = %q", in, got)
		}
		if got := singular(words(in)); got != want[1] {
			t.Errorf("singular(%q) = %q", in, got)
		}
	}
}

func TestLiteralDefault(t *testing.T) {
	tests := []struct {
		expr, want string
		ok         bool
	}{
		{"'free'::text", "free", true},
		{"'it''s'::text", "it's", true},
		{"true", "true", true},
		{"0", "0", true},
		{"'{}'::jsonb", "", false},
		{"'{}'::text[]", "", false},
		{"now()", "", false},
		{"gen_random_uuid()", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := literalDefault(tt.expr)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("literalDefault(%q) = %q, %v", tt.expr, got, ok)
		}
	}
}

// fields is a struct of every type a form reads.
type fields struct {
	S   string
	SP  *string
	B   bool
	BP  *bool
	I16 int16
	I64 *int64
	U   uint8
	F   float64
	T   time.Time
	TP  *time.Time
	ID  uuid.UUID
	J   json.RawMessage
	L   []string
}

func TestParse(t *testing.T) {
	site := &Site{}
	p := &modelPage{site: site, typ: reflect.TypeFor[fields]()}
	col := func(name string) *column {
		f, _ := p.typ.FieldByName(name)
		base := f.Type
		if base.Kind() == reflect.Pointer {
			base = base.Elem()
		}
		return &column{name: name, index: f.Index, goType: f.Type, base: base, nullable: f.Type.Kind() == reflect.Pointer, kind: kindOf(base)}
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		field, input string
		want         any
		blank        bool
		err          string
	}{
		{"S", "hi\r\nthere", "hi\nthere", false, ""},
		{"S", "", "", true, ""},
		{"SP", "", (*string)(nil), true, ""},
		{"SP", "x", ptr("x"), false, ""},
		{"B", "true", true, false, ""},
		{"B", "", false, false, ""},
		{"BP", "", (*bool)(nil), true, ""},
		{"BP", "false", ptr(false), false, ""},
		{"BP", "maybe", nil, false, "Select yes or no."},
		{"I16", " 42 ", int16(42), false, ""},
		{"I16", "40000", nil, false, "Enter a whole number between -32768 and 32767."},
		{"I16", "", nil, true, "This field is required."},
		{"I64", "", (*int64)(nil), true, ""},
		{"I64", "-7", ptr(int64(-7)), false, ""},
		{"U", "-1", nil, false, "Enter a whole number, 0 or more."},
		{"F", "1.5", 1.5, false, ""},
		{"F", "x", nil, false, "Enter a number."},
		{"T", "2026-01-02T03:04:05", at, false, ""},
		{"T", "2026-01-02T03:04", at.Add(-5 * time.Second), false, ""},
		{"T", "2026-01-02T05:04:05+02:00", at, false, ""},
		{"T", "soon", nil, false, "Enter a date and time, in UTC."},
		{"TP", "", (*time.Time)(nil), true, ""},
		{"ID", "6f1c0f9e-1f2b-4a63-9c43-2c6f0f6b8f10", uuid.MustParse("6f1c0f9e-1f2b-4a63-9c43-2c6f0f6b8f10"), false, ""},
		{"ID", "nope", nil, false, "Enter a valid UUID."},
		{"J", `{"a": 1}`, json.RawMessage(`{"a": 1}`), false, ""},
		{"J", `{"a": `, nil, false, "Enter valid JSON."},
		{"L", "a\n\n b \n", []string{"a", "b"}, false, ""},
		{"L", "", []string{}, true, ""},
	}
	for _, tt := range tests {
		got, blank, err := p.parse(col(tt.field), tt.input)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("%s %q: error %v, want %q", tt.field, tt.input, err, tt.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %q: %v", tt.field, tt.input, err)
			continue
		}
		if !reflect.DeepEqual(got.Interface(), tt.want) {
			t.Errorf("%s %q = %#v, want %#v", tt.field, tt.input, got.Interface(), tt.want)
		}
		if blank != tt.blank {
			t.Errorf("%s %q: blank %v", tt.field, tt.input, blank)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestDatabaseErrors(t *testing.T) {
	p := &modelPage{label: "Device", byName: map[string]*column{
		"token":  {name: "token", label: "Token"},
		"secret": {name: "secret", label: "Secret", hidden: true},
	}}
	tests := []struct {
		err   *pgconn.PgError
		field string
		want  string
	}{
		{&pgconn.PgError{Code: "23502", ColumnName: "token"}, "token", "This field is required."},
		{&pgconn.PgError{Code: "23502", ColumnName: "secret"}, "", "This field is required."},
		{&pgconn.PgError{Code: "23505", ConstraintName: "devices_token_key", Detail: "Key (token)=(x) already exists."}, "token", "Another device already has this token. (constraint devices_token_key)"},
		{&pgconn.PgError{Code: "23505", ConstraintName: "two", Detail: "Key (a, b)=(x, y) already exists."}, "", "Another device already has this value. (constraint two)"},
	}
	for _, tt := range tests {
		got, ok := p.databaseErrorsFor(tt.err, "devices")
		if !ok || got[tt.field] != tt.want {
			t.Errorf("%s: %v", tt.err.Code, got)
		}
	}
	if _, ok := p.databaseErrorsFor(&pgconn.PgError{Code: "57014"}, "devices"); ok {
		t.Error("a statement timeout became a form error")
	}
}
