package jetadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/garden-studio/jet-admin/internal/ui"
)

// How values look and read. Times are shown and entered in UTC, so what an
// admin types means the same whoever reads it; the forms say so.

const (
	cellTimeLayout  = "2006-01-02 15:04"
	inputTimeLayout = "2006-01-02T15:04:05"
	dateLayout      = "2006-01-02"
	listTextLimit   = 80
)

// isNil reports a NULL: a nil pointer, or a nil json.RawMessage.
func isNil(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer:
		return v.IsNil()
	case reflect.Slice:
		return v.IsNil() && v.Type() == typeJSON
	}
	return false
}

func deref(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Pointer {
		return v.Elem()
	}
	return v
}

// textOf is the text form of a field's value: the one in URLs, filters and
// the log. NULL is "".
func textOf(c *column, v reflect.Value) string {
	if isNil(v) {
		return ""
	}
	v = deref(v)
	switch c.kind {
	case kindString:
		return v.String()
	case kindBool:
		return strconv.FormatBool(v.Bool())
	case kindInt:
		return strconv.FormatInt(v.Int(), 10)
	case kindUint:
		return strconv.FormatUint(v.Uint(), 10)
	case kindFloat:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case kindTime:
		t := v.Interface().(time.Time).UTC()
		if c.sql == sqlDate {
			return t.Format(dateLayout)
		}
		return t.Format(time.RFC3339)
	case kindUUID:
		return v.Interface().(uuid.UUID).String()
	case kindJSON:
		return string(v.Bytes())
	case kindStrings:
		return strings.Join(v.Interface().([]string), "\n")
	case kindBytes:
		return byteCount(v.Len())
	}
	return fmt.Sprint(v.Interface())
}

func byteCount(n int) string {
	if n == 1 {
		return "1 byte"
	}
	return strconv.Itoa(n) + " bytes"
}

// inputText is a field's value as its input shows it.
func (p *modelPage) inputText(c *column, v reflect.Value) string {
	if isNil(v) {
		return ""
	}
	switch {
	case c.kind == kindTime:
		t := deref(v).Interface().(time.Time).UTC()
		if c.sql == sqlDate {
			return t.Format(dateLayout)
		}
		return t.Format(inputTimeLayout)
	case p.isJSON(c):
		raw := []byte(textOf(c, v))
		var out bytes.Buffer
		if json.Indent(&out, raw, "", "  ") == nil {
			return out.String()
		}
		return string(raw)
	}
	return textOf(c, v)
}

// cell is a field's value in a list or a read-only row. long keeps the
// whole text (a change page) instead of cutting it for a list.
func (p *modelPage) cell(c *column, v reflect.Value, long bool) ui.Cell {
	if isNil(v) {
		return ui.Cell{Kind: ui.CellNull}
	}
	if len(c.choices) > 0 {
		text := textOf(c, v)
		for _, choice := range c.choices {
			if choice.Value == text {
				return ui.Cell{Text: choice.Label, Title: text}
			}
		}
	}
	switch c.kind {
	case kindBool:
		return ui.Cell{Kind: ui.CellBool, Bool: deref(v).Bool()}
	case kindInt, kindUint, kindFloat:
		return ui.Cell{Text: textOf(c, v), Numeric: true}
	case kindTime:
		t := deref(v).Interface().(time.Time).UTC()
		if c.sql == sqlDate {
			return ui.Cell{Text: t.Format(dateLayout), Mono: true}
		}
		layout := cellTimeLayout
		if long {
			layout = "2006-01-02 15:04:05 UTC"
		}
		return ui.Cell{Text: t.Format(layout), Title: t.Format(time.RFC3339Nano), Mono: true}
	case kindUUID:
		return ui.Cell{Text: textOf(c, v), Mono: true}
	case kindBytes:
		return ui.Cell{Text: textOf(c, v), Kind: ui.CellMuted}
	case kindStrings:
		items := deref(v).Interface().([]string)
		if len(items) == 0 {
			return ui.Cell{Kind: ui.CellNull}
		}
		return truncated(strings.Join(items, ", "), long, false)
	}
	return truncated(textOf(c, v), long, p.isJSON(c))
}

// truncated cuts a list's long text, keeping the whole of it in the
// tooltip.
func truncated(text string, long, mono bool) ui.Cell {
	if long || utf8.RuneCountInString(text) <= listTextLimit {
		return ui.Cell{Text: text, Mono: mono}
	}
	title := text
	if utf8.RuneCountInString(title) > 1000 {
		title = string([]rune(title)[:1000]) + "…"
	}
	return ui.Cell{Text: strings.TrimSpace(string([]rune(text)[:listTextLimit])) + "…", Title: title, Mono: mono}
}

// errBlank is a required value left blank.
var errBlank = errors.New("This field is required.")

// parse reads an input's text into a value of the field's type. A blank
// input is NULL for a pointer field; blank tells the caller the input was
// empty, so it can leave a defaulted column to the database.
func (p *modelPage) parse(c *column, raw string) (value reflect.Value, blank bool, err error) {
	trimmed := strings.TrimSpace(raw)
	blank = trimmed == ""
	if c.kind == kindString && !p.isJSON(c) {
		blank = raw == ""
	}
	if c.kind == kindBool && !c.nullable {
		// A checkbox: present is true, absent false. Never blank.
		return reflect.ValueOf(raw != "" && raw != "false").Convert(c.goType), false, nil
	}
	if blank && c.nullable {
		return reflect.Zero(c.goType), true, nil
	}
	if len(c.choices) > 0 && !blank {
		known := false
		for _, choice := range c.choices {
			known = known || choice.Value == raw
		}
		if !known {
			return reflect.Value{}, false, errors.New("Select a valid choice.")
		}
	}
	base, err := p.parseBase(c, raw, trimmed, blank)
	if err != nil {
		return reflect.Value{}, blank, err
	}
	if c.nullable {
		ptr := reflect.New(c.base)
		ptr.Elem().Set(base)
		return ptr, blank, nil
	}
	return base, blank, nil
}

func (p *modelPage) parseBase(c *column, raw, trimmed string, blank bool) (reflect.Value, error) {
	t := c.base
	switch c.kind {
	case kindString:
		if p.isJSON(c) {
			if blank {
				return reflect.Value{}, errBlank
			}
			if !json.Valid([]byte(trimmed)) {
				return reflect.Value{}, errors.New("Enter valid JSON.")
			}
			return reflect.ValueOf(trimmed).Convert(t), nil
		}
		return reflect.ValueOf(strings.ReplaceAll(raw, "\r\n", "\n")).Convert(t), nil
	case kindBool:
		switch trimmed {
		case "true":
			return reflect.ValueOf(true).Convert(t), nil
		case "false":
			return reflect.ValueOf(false).Convert(t), nil
		}
		return reflect.Value{}, errors.New("Select yes or no.")
	case kindInt:
		if blank {
			return reflect.Value{}, errBlank
		}
		n, err := strconv.ParseInt(trimmed, 10, t.Bits())
		if err != nil {
			return reflect.Value{}, fmt.Errorf("Enter a whole number between %d and %d.", minInt(t.Bits()), maxInt(t.Bits()))
		}
		return reflect.ValueOf(n).Convert(t), nil
	case kindUint:
		if blank {
			return reflect.Value{}, errBlank
		}
		n, err := strconv.ParseUint(trimmed, 10, t.Bits())
		if err != nil {
			return reflect.Value{}, errors.New("Enter a whole number, 0 or more.")
		}
		return reflect.ValueOf(n).Convert(t), nil
	case kindFloat:
		if blank {
			return reflect.Value{}, errBlank
		}
		f, err := strconv.ParseFloat(trimmed, t.Bits())
		if err != nil {
			return reflect.Value{}, errors.New("Enter a number.")
		}
		return reflect.ValueOf(f).Convert(t), nil
	case kindTime:
		if blank {
			return reflect.Value{}, errBlank
		}
		layouts := []string{inputTimeLayout, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", time.RFC3339Nano, dateLayout}
		if c.sql == sqlDate {
			layouts = []string{dateLayout}
		}
		for _, layout := range layouts {
			if at, err := time.ParseInLocation(layout, trimmed, time.UTC); err == nil {
				return reflect.ValueOf(at.UTC()), nil
			}
		}
		if c.sql == sqlDate {
			return reflect.Value{}, errors.New("Enter a date as YYYY-MM-DD.")
		}
		return reflect.Value{}, errors.New("Enter a date and time, in UTC.")
	case kindUUID:
		if blank {
			return reflect.Value{}, errBlank
		}
		id, err := uuid.Parse(trimmed)
		if err != nil {
			return reflect.Value{}, errors.New("Enter a valid UUID.")
		}
		return reflect.ValueOf(id), nil
	case kindJSON:
		if blank {
			return reflect.Value{}, errBlank
		}
		if !json.Valid([]byte(trimmed)) {
			return reflect.Value{}, errors.New("Enter valid JSON.")
		}
		return reflect.ValueOf(json.RawMessage(trimmed)).Convert(t), nil
	case kindStrings:
		items := []string{}
		for line := range strings.SplitSeq(raw, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				items = append(items, line)
			}
		}
		return reflect.ValueOf(items).Convert(t), nil
	}
	return reflect.Value{}, fmt.Errorf("a %s cannot be entered here", t)
}

func minInt(bits int) int64 { return -1 << (bits - 1) }
func maxInt(bits int) int64 { return 1<<(bits-1) - 1 }

// parseKey reads the text form of c's value (a primary key in a URL, a
// foreign key) into the Go value a statement passes as its parameter.
func (p *modelPage) parseKey(c *column, text string) (any, error) {
	if text == "" {
		return nil, errBlank
	}
	v, err := p.parseBase(c, text, strings.TrimSpace(text), false)
	if err != nil {
		return nil, err
	}
	return v.Interface(), nil
}
