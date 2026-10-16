package ui

import (
	"encoding/json"
	"strconv"
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

// getURL asks the server for url with Datastar.
func getURL(url string) string { return "@get(" + js(url) + ")" }

// closeDialog empties the dialog slot.
const closeDialog = "document.getElementById('dialog').replaceChildren()"

func itoa(n int) string { return strconv.Itoa(n) }

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

func sortMark(sorted string) string {
	switch sorted {
	case "asc":
		return "▲"
	case "desc":
		return "▼"
	}
	return ""
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

func textareaRows(value string) string {
	rows := 3
	for _, r := range value {
		if r == '\n' {
			rows++
		}
	}
	return strconv.Itoa(min(max(rows, 4), 20))
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
