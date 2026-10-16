// Package ui renders the admin's pages. The handlers fill these view
// structs; the templ components only lay them out.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Static is the stylesheet and datastar.js, served under BasePath/static/.
func Static() fs.FS {
	sub, _ := fs.Sub(static, "static")
	return sub
}

// Chrome is what every page around its content shows.
type Chrome struct {
	SiteTitle string
	Base      string // BasePath, without the trailing slash
	PageTitle string
	User      string // username, "" on the login page
	LogoutURL string // "" when the Authenticator serves no logout
	Nav       []NavGroup
	Active    string // slug of the model the page is about
	Crumbs    []Crumb
	Flash     *Flash
}

// StaticURL is the address of a static file.
func (c Chrome) StaticURL(name string) string { return c.Base + "/static/" + name }

type NavGroup struct {
	Name   string
	Models []NavModel
}

type NavModel struct {
	Slug, Plural, URL, AddURL string
}

type Crumb struct {
	Label, URL string // the last crumb has no URL
}

// Flash is a message after a redirect.
type Flash struct {
	Error bool
	Text  string
}

// CellKind is how a value is drawn.
type CellKind int

const (
	CellText CellKind = iota
	CellNull
	CellBool
	CellMuted
)

// Cell is one value in a list or a read-only row.
type Cell struct {
	Kind    CellKind
	Text    string
	Title   string // the whole value, when Text is cut or formatted
	Bool    bool
	Mono    bool
	Numeric bool
	Href    string // a link to a related row
	Note    string // the related row's name, beside its key
}

type IndexPage struct {
	Chrome  Chrome
	Groups  []NavGroup
	Recent  []LogEntry
	LogURL  string
	Audited bool
}

type LogEntry struct {
	Time, User, Action, Model, Object, ObjectURL, Message string
	ActionClass                                           string // add, change, delete
}

type LogPage struct {
	Chrome  Chrome
	Entries []LogEntry
	Pager   Pager
	Empty   string
}

type ListPage struct {
	Chrome      Chrome
	Plural      string
	Label       string
	ListURL     string
	AddURL      string
	Search      *Search
	Hidden      []Param // the filters and order the search form keeps
	Columns     []ListColumn
	Rows        []ListRow
	Filters     []Filter
	Total       int64
	Pager       Pager
	CanDelete   bool
	ClearURL    string // "" when nothing is filtered or searched
	ResultLabel string
}

type Search struct {
	Value, Placeholder string
}

type Param struct{ Name, Value string }

type ListColumn struct {
	Label   string
	SortURL string // "" when the column does not sort
	Sorted  string // "asc", "desc" or ""
	Numeric bool
}

type ListRow struct {
	PK    string
	URL   string
	Label string // the row's name, for the checkbox's label
	Cells []Cell
}

type Filter struct {
	Label   string
	Note    string
	Options []FilterOption
}

type FilterOption struct {
	Label, URL string
	Active     bool
}

type Pager struct {
	Page, Pages int
	PrevURL     string
	NextURL     string
	Links       []PageLink
}

type PageLink struct {
	Label, URL string
	Current    bool
	Gap        bool
}

// InputKind is the input a form field renders.
type InputKind int

const (
	InputText InputKind = iota
	InputTextarea
	InputNumber
	InputDecimal
	InputCheckbox
	InputNullBool
	InputSelect
	InputDateTime
	InputDate
	InputPassword
	InputReadOnly
)

type FormField struct {
	Name     string
	Label    string
	Help     string
	Note     string // a hint the type adds: UTC, one per line, NULL
	Error    string
	Input    InputKind
	Value    string
	Checked  bool
	Required bool
	Mono     bool
	Options  []Option
	Cell     Cell // what a read-only field shows
	Related  *Cell
}

type Option struct {
	Value, Label string
	Selected     bool
}

type FormPage struct {
	Chrome     Chrome
	Heading    string
	Action     string
	FormError  string
	Fields     []FormField
	Adding     bool
	CanSave    bool
	DeleteURL  string
	HistoryURL string
	Inlines    []InlineList
	SaveLabel  string
}

type InlineList struct {
	Plural  string
	Columns []string
	Rows    []ListRow
	Total   int64
	AllURL  string
	AddURL  string
}

type DeletePage struct {
	Chrome    Chrome
	Heading   string
	Objects   []DeleteObject
	Action    string
	CancelURL string
	Keys      []string // the primary keys a bulk delete posts back
	Bulk      bool
	Dialog    bool // shown over the page by Datastar, not as a page
	Label     string
	Plural    string
}

type DeleteObject struct {
	Label, URL string
}

type HistoryPage struct {
	Chrome  Chrome
	Heading string
	Entries []LogEntry
	Empty   string
}

type LoginPage struct {
	Chrome   Chrome
	Action   string
	Next     string
	Username string
	Error    string
}

type MessagePage struct {
	Chrome  Chrome
	Heading string
	Text    string
}
