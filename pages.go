package jetadmin

import (
	"net/http"
	"strconv"

	"github.com/garden-studio/jet-admin/internal/ui"
)

// chrome is what every page of a signed-in user shows around its content.
func (s *Site) chrome(w http.ResponseWriter, r *http.Request, u User, title, active string, crumbs ...ui.Crumb) ui.Chrome {
	c := ui.Chrome{
		SiteTitle: s.title,
		Base:      s.base,
		Styles:    s.styles,
		PageTitle: title,
		User:      u.Username,
		Nav:       s.nav(),
		Active:    active,
		Flash:     s.takeFlash(w, r),
	}
	if folded, err := r.Cookie("sidebar_state"); err == nil && folded.Value == "false" {
		c.NavCollapsed = true
	}
	if _, ok := s.auth.(LoginProvider); ok {
		c.LogoutURL = s.base + "/logout"
	}
	if len(crumbs) > 0 {
		c.Crumbs = append([]ui.Crumb{{Label: "Home", URL: s.base + "/"}}, crumbs...)
	}
	return c
}

// nav groups the models by Group, in the order they were registered.
func (s *Site) nav() []ui.NavGroup {
	var groups []ui.NavGroup
	at := map[string]int{}
	for _, p := range s.modelsInOrder() {
		name := p.group
		if name == "" {
			name = "Models"
		}
		i, ok := at[name]
		if !ok {
			i = len(groups)
			at[name] = i
			groups = append(groups, ui.NavGroup{Name: name})
		}
		m := ui.NavModel{Slug: p.slug, Plural: p.plural, URL: p.listURL(), Icon: p.icon}
		if !p.perms.DisableAdd {
			m.AddURL = p.addURL()
		}
		groups[i].Models = append(groups[i].Models, m)
	}
	return groups
}

func (s *Site) index(w http.ResponseWriter, r *http.Request, u User) {
	page := ui.IndexPage{
		Chrome:  s.chrome(w, r, u, "Site administration", ""),
		Groups:  s.nav(),
		Audited: s.auditLog,
		LogURL:  s.base + "/log",
	}
	if s.auditLog {
		recent, err := s.logEntries(r.Context(), nil, 10, 0)
		if err != nil {
			s.fail(w, r, u, "read the recent actions", err)
			return
		}
		page.Recent = recent
	}
	s.render(w, r, http.StatusOK, ui.Index(page))
}

const logPageSize = 50

func (s *Site) logPage(w http.ResponseWriter, r *http.Request, u User) {
	chrome := s.chrome(w, r, u, "Actions", "", ui.Crumb{Label: "Actions"})
	if !s.auditLog {
		s.render(w, r, http.StatusOK, ui.Log(ui.LogPage{Chrome: chrome, Empty: "The audit log is off (Config.AuditLog)."}))
		return
	}
	ctx := r.Context()
	total, err := s.countLog(ctx, nil)
	if err != nil {
		s.fail(w, r, u, "count the actions", err)
		return
	}
	pages := max(1, int((total+logPageSize-1)/logPageSize))
	current := min(max(1, atoi(r.URL.Query().Get("p"))), pages)
	entries, err := s.logEntries(ctx, nil, logPageSize, int64(current-1)*logPageSize)
	if err != nil {
		s.fail(w, r, u, "read the actions", err)
		return
	}
	pager := makePager(current, pages, func(n int) string { return s.base + "/log?p=" + strconv.Itoa(n) })
	s.render(w, r, http.StatusOK, ui.Log(ui.LogPage{Chrome: chrome, Entries: entries, Pager: pager, Empty: "Nothing has been changed yet."}))
}

func (s *Site) notFound(w http.ResponseWriter, r *http.Request, u User) {
	s.message(w, r, u, http.StatusNotFound, "Not found", "There is no such page, or the row was deleted.")
}

func (s *Site) forbidden(w http.ResponseWriter, r *http.Request, u User, text string) {
	s.message(w, r, u, http.StatusForbidden, "Not allowed", text)
}

func (s *Site) message(w http.ResponseWriter, r *http.Request, u User, status int, heading, text string) {
	if fromDatastar(r) {
		s.patch(w, r, ui.FlashFragment(&ui.Flash{Error: true, Text: text}))
		return
	}
	s.render(w, r, status, ui.Message(ui.MessagePage{Chrome: s.chrome(w, r, u, heading, ""), Heading: heading, Text: text}))
}

// fail answers a failure the page cannot explain, a database down: it is
// logged, and the visitor told something went wrong.
func (s *Site) fail(w http.ResponseWriter, r *http.Request, u User, what string, err error) {
	s.logFailure(r, what, err)
	s.message(w, r, u, http.StatusInternalServerError, "Something went wrong", "The admin could not "+what+". The error is in the server's log.")
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// makePager links the pages around the current one, the first and the
// last, as Django's paginator does.
func makePager(current, pages int, link func(int) string) ui.Pager {
	p := ui.Pager{Page: current, Pages: pages}
	if pages <= 1 {
		return p
	}
	if current > 1 {
		p.PrevURL = link(current - 1)
	}
	if current < pages {
		p.NextURL = link(current + 1)
	}
	last := 0
	for n := 1; n <= pages; n++ {
		if n != 1 && n != pages && (n < current-2 || n > current+2) {
			continue
		}
		if last != 0 && n > last+1 {
			p.Links = append(p.Links, ui.PageLink{Gap: true})
		}
		p.Links = append(p.Links, ui.PageLink{Label: strconv.Itoa(n), URL: link(n), Current: n == current})
		last = n
	}
	return p
}
