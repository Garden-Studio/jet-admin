package jetadmin

import (
	"context"
	"net/http"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/garden-studio/jet-admin/internal/ui"
)

// Deleting asks first: a page without JavaScript, a dialog over the page
// with it. The confirmation posts confirm=yes.

func (s *Site) deleteConfirm(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	if p.perms.DisableDelete {
		s.forbidden(w, r, u, p.plural+" cannot be deleted here.")
		return
	}
	key, row, ok := s.loadRow(w, r, u, p, false)
	if !ok {
		return
	}
	name := p.display(row)
	page := ui.DeletePage{
		Heading:   "Delete " + strings.ToLower(p.label) + "?",
		Objects:   []ui.DeleteObject{{Label: name, URL: p.changeURL(key)}},
		Action:    p.deleteURL(key),
		CancelURL: p.changeURL(key),
		Label:     strings.ToLower(p.label),
		Plural:    strings.ToLower(p.plural),
	}
	s.renderDelete(w, r, u, p, page, ui.Crumb{Label: name, URL: p.changeURL(key)})
}

func (s *Site) renderDelete(w http.ResponseWriter, r *http.Request, u User, p *modelPage, page ui.DeletePage, crumbs ...ui.Crumb) {
	if fromDatastar(r) {
		page.Dialog = true
		s.patch(w, r, ui.DeleteDialog(page))
		return
	}
	crumbs = append([]ui.Crumb{{Label: p.plural, URL: p.listURL()}}, crumbs...)
	crumbs = append(crumbs, ui.Crumb{Label: "Delete"})
	page.Chrome = s.chrome(w, r, u, page.Heading, p.slug, crumbs...)
	s.render(w, r, http.StatusOK, ui.Delete(page))
}

func (s *Site) deleteSubmit(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	if p.perms.DisableDelete {
		s.forbidden(w, r, u, p.plural+" cannot be deleted here.")
		return
	}
	key, row, ok := s.loadRow(w, r, u, p, false)
	if !ok {
		return
	}
	if r.FormValue("confirm") != "yes" {
		s.redirect(w, r, p.deleteURL(key))
		return
	}
	name := p.display(row)
	parsed, _ := p.parseKey(p.pk, key)
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := p.deleteKeys(r.Context(), tx, []any{parsed}); err != nil {
			return err
		}
		return s.logAction(r.Context(), tx, u, p, key, name, actionDelete, "Deleted.")
	})
	if err != nil {
		if refusal := deleteRefusal(err); refusal != "" {
			s.redirectWith(w, r, p.changeURL(key), ui.Flash{Error: true, Text: "The " + strings.ToLower(p.label) + " “" + name + "” cannot be deleted. " + refusal})
			return
		}
		s.fail(w, r, u, "delete the "+strings.ToLower(p.label), err)
		return
	}
	s.redirectWith(w, r, p.listURL(), ui.Flash{Text: "The " + strings.ToLower(p.label) + " “" + name + "” was deleted."})
}

// bulkDelete is the list's action: the checked rows, confirmed, deleted
// in one transaction.
func (s *Site) bulkDelete(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	if p.perms.DisableDelete {
		s.forbidden(w, r, u, p.plural+" cannot be deleted here.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("action") != "delete" {
		s.redirectWith(w, r, p.listURL(), ui.Flash{Error: true, Text: "Choose an action."})
		return
	}
	var keys []any
	for _, text := range r.PostForm["pk"] {
		if k, err := p.parseKey(p.pk, text); err == nil {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		s.redirectWith(w, r, p.listURL(), ui.Flash{Error: true, Text: "Select the " + strings.ToLower(p.plural) + " to delete first."})
		return
	}
	ctx := r.Context()
	rows, err := p.byKeys(ctx, s.db, keys)
	if err != nil {
		s.fail(w, r, u, "read the "+strings.ToLower(p.plural), err)
		return
	}
	if len(rows) == 0 {
		s.redirectWith(w, r, p.listURL(), ui.Flash{Error: true, Text: "The selected " + strings.ToLower(p.plural) + " no longer exist."})
		return
	}
	if r.PostForm.Get("confirm") != "yes" {
		page := ui.DeletePage{
			Heading:   "Delete " + countLabel(int64(len(rows)), p) + "?",
			Action:    p.listURL(),
			CancelURL: p.listURL(),
			Bulk:      true,
			Label:     strings.ToLower(p.label),
			Plural:    strings.ToLower(p.plural),
		}
		for _, row := range rows {
			key := p.pkText(row)
			page.Objects = append(page.Objects, ui.DeleteObject{Label: p.display(row), URL: p.changeURL(key)})
			page.Keys = append(page.Keys, key)
		}
		s.renderDelete(w, r, u, p, page)
		return
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		return s.deleteRows(ctx, tx, u, p, rows)
	})
	if err != nil {
		if refusal := deleteRefusal(err); refusal != "" {
			s.redirectWith(w, r, p.listURL(), ui.Flash{Error: true, Text: "Nothing was deleted. " + refusal})
			return
		}
		s.fail(w, r, u, "delete the "+strings.ToLower(p.plural), err)
		return
	}
	s.redirectWith(w, r, p.listURL(), ui.Flash{Text: "Deleted " + countLabel(int64(len(rows)), p) + "."})
}

func (s *Site) deleteRows(ctx context.Context, tx pgx.Tx, u User, p *modelPage, rows []reflect.Value) error {
	keys := make([]any, len(rows))
	for i, row := range rows {
		keys[i] = valueOf(p.pk.field(row))
	}
	if _, err := p.deleteKeys(ctx, tx, keys); err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.logAction(ctx, tx, u, p, p.pkText(row), p.display(row), actionDelete, "Deleted."); err != nil {
			return err
		}
	}
	return nil
}

func (s *Site) history(w http.ResponseWriter, r *http.Request, u User, p *modelPage) {
	key, row, ok := s.loadRow(w, r, u, p, false)
	if !ok {
		return
	}
	name := p.display(row)
	page := ui.HistoryPage{
		Chrome:  s.chrome(w, r, u, "History: "+name, p.slug, ui.Crumb{Label: p.plural, URL: p.listURL()}, ui.Crumb{Label: name, URL: p.changeURL(key)}, ui.Crumb{Label: "History"}),
		Heading: "History: " + name,
		Empty:   "This " + strings.ToLower(p.label) + " has no recorded changes.",
	}
	if !s.auditLog {
		page.Empty = "The audit log is off (Config.AuditLog)."
	} else {
		entries, err := s.logEntries(r.Context(), objectLog(p, key), 500, 0)
		if err != nil {
			s.fail(w, r, u, "read the history", err)
			return
		}
		page.Entries = entries
	}
	s.render(w, r, http.StatusOK, ui.History(page))
}
