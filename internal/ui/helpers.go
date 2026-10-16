package ui

import (
	"encoding/json"
	"strconv"

	"github.com/a-h/templ"

	"github.com/garden-studio/jet-admin/internal/ui/components/alert"
	"github.com/garden-studio/jet-admin/internal/ui/components/badge"
)

func pageTitle(c Chrome) string {
	if c.PageTitle == "" {
		return c.SiteTitle
	}
	return c.PageTitle + " | " + c.SiteTitle
}

func ariaCurrent(current bool) string {
	if current {
		return "page"
	}
	return "false"
}

func flashRole(f *Flash) string {
	if f.Error {
		return "alert"
	}
	return "status"
}

// js is s as a JavaScript string literal, for a Datastar expression.
func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// getHref follows a link with Datastar: the server patches the page.
func getHref() string { return "@get(el.href)" }

// getForm sends the closest form with Datastar as a GET.
func getForm(url string) string { return "@get(" + js(url) + ", {contentType: 'form'})" }

// postForm sends the closest form with Datastar as a POST.
func postForm(url string) string { return "@post(" + js(url) + ", {contentType: 'form'})" }


func i64toa(n int64) string { return strconv.FormatInt(n, 10) }

func fieldID(name string) string { return "field-" + name }

func describedBy(f FormField) string {
	ids := ""
	if f.Help != "" || f.Note != "" {
		ids = fieldID(f.Name) + "-help"
	}
	if f.Error != "" {
		if ids != "" {
			ids += " "
		}
		ids += fieldID(f.Name) + "-error"
	}
	return ids
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}

func ariaSort(sorted string) string {
	switch sorted {
	case "asc":
		return "ascending"
	case "desc":
		return "descending"
	}
	return "none"
}

func withoutHref(c Cell) Cell {
	c.Href = ""
	return c
}

func hasErrors(fields []FormField) bool {
	for _, f := range fields {
		if f.Error != "" {
			return true
		}
	}
	return false
}

func invalid(f FormField) string {
	if f.Error != "" {
		return "true"
	}
	return "false"
}

func spellcheck(f FormField) string {
	if f.Mono {
		return "false"
	}
	return "true"
}

func joinHelp(f FormField) string {
	switch {
	case f.Help != "" && f.Note != "":
		return f.Help + " " + f.Note
	case f.Help != "":
		return f.Help
	}
	return f.Note
}

func textareaRowsN(value string) int {
	rows := 3
	for _, r := range value {
		if r == '\n' {
			rows++
		}
	}
	return min(max(rows, 4), 20)
}

func slugID(s string) string {
	out := []rune{}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+'a'-'A')
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// crumbNarrow hides the middle crumbs, and the separators before them, on
// narrow screens: Home and the page itself stay.
func crumbNarrow(i, n int) string {
	if i > 0 && i < n-1 {
		return "max-sm:hidden"
	}
	return ""
}

func ariaCurrentAttr(current bool) templ.Attributes {
	if current {
		return templ.Attributes{"aria-current": "page"}
	}
	return nil
}

func flashVariant(f *Flash) alert.Variant {
	if f.Error {
		return alert.VariantDestructive
	}
	return alert.VariantDefault
}

func emptyFrame(framed bool) string {
	if framed {
		return "border py-10"
	}
	return ""
}

func emptyListText(p ListPage) string {
	if p.ClearURL != "" {
		return "Nothing matches the search and filters."
	}
	return ""
}

// actionVariant is the badge of an audit log action.
func actionVariant(action string) badge.Variant {
	switch action {
	case "add":
		return badge.VariantSecondary
	case "delete":
		return badge.VariantDestructive
	}
	return badge.VariantOutline
}

// pageLinkAttrs makes a page link follow itself with Datastar.
func pageLinkAttrs(url, rel string) templ.Attributes {
	if url == "" {
		return nil
	}
	a := templ.Attributes{"data-on:click__prevent": getHref()}
	if rel != "" {
		a["rel"] = rel
	}
	return a
}

func cellClass(c Cell) string {
	if c.Numeric {
		return "text-right tabular-nums"
	}
	return ""
}

func monoClass(mono bool) string {
	if mono {
		return "font-mono"
	}
	return ""
}

func invalidAttr(f FormField) templ.Attributes {
	if f.Error != "" {
		return templ.Attributes{"data-invalid": "true"}
	}
	return nil
}

// inputAttrs are the ARIA attributes every input of a field carries.
func inputAttrs(f FormField) templ.Attributes {
	a := templ.Attributes{"aria-invalid": invalid(f)}
	if d := describedBy(f); d != "" {
		a["aria-describedby"] = d
	}
	return a
}

// withAttrs is a with b's attributes added.
func withAttrs(a, b templ.Attributes) templ.Attributes {
	out := templ.Attributes{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
