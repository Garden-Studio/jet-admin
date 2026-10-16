package jetadmin

import (
	"encoding/base64"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/garden-studio/jet-admin/internal/ui"
)

// How every page answers. A plain request gets the whole document and a
// form post a 303; a request Datastar sent gets the fragments to patch, or
// the navigation to make, over Server-Sent Events.

// fromDatastar reports a request Datastar sent; it marks each with this
// header.
func fromDatastar(r *http.Request) bool {
	return r.Header.Get("Datastar-Request") == "true"
}

// render writes a whole page.
func (s *Site) render(w http.ResponseWriter, r *http.Request, status int, page templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := page.Render(r.Context(), w); err != nil {
		s.logFailure(r, "render page", err)
	}
}

// patch answers a Datastar request with fragments, each patched over the
// element of its id.
func (s *Site) patch(w http.ResponseWriter, r *http.Request, fragments ...templ.Component) {
	sse := datastar.NewSSE(w, r)
	for _, f := range fragments {
		if err := sse.PatchElementTempl(f); err != nil {
			s.logFailure(r, "patch fragment", err)
			return
		}
	}
}

// patchURL is patch that also puts location in the address bar, so a
// filtered list can be reloaded or shared.
func (s *Site) patchURL(w http.ResponseWriter, r *http.Request, location string, fragments ...templ.Component) {
	sse := datastar.NewSSE(w, r)
	for _, f := range fragments {
		if err := sse.PatchElementTempl(f); err != nil {
			s.logFailure(r, "patch fragment", err)
			return
		}
	}
	if u, err := url.Parse(location); err == nil {
		if err := sse.ReplaceURL(*u); err != nil {
			s.logFailure(r, "replace url", err)
		}
	}
}

// redirect is a 303, or for Datastar a navigation it makes itself.
func (s *Site) redirect(w http.ResponseWriter, r *http.Request, location string) {
	if fromDatastar(r) {
		if err := datastar.NewSSE(w, r).Redirect(location); err != nil {
			s.logFailure(r, "datastar redirect", err)
		}
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// redirectWith redirects with a message for the next page.
func (s *Site) redirectWith(w http.ResponseWriter, r *http.Request, location string, f ui.Flash) {
	s.setFlash(w, f)
	s.redirect(w, r, location)
}

func (s *Site) logFailure(r *http.Request, what string, err error) {
	level := slog.LevelError
	if r.Context().Err() != nil {
		// The visitor went away (a search superseded by the next
		// keystroke): no failure of ours.
		level = slog.LevelDebug
	}
	s.logger.Log(r.Context(), level, "jetadmin: "+what, "path", r.URL.Path, "error", err)
}

// The flash message rides a cookie for one redirect: it is read, and
// cleared, by the next page.
const flashCookie = "jetadmin_flash"

func (s *Site) cookiePath() string { return s.base + "/" }

func (s *Site) setFlash(w http.ResponseWriter, f ui.Flash) {
	kind := "ok"
	if f.Error {
		kind = "error"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + f.Text)),
		Path:     s.cookiePath(),
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Site) takeFlash(w http.ResponseWriter, r *http.Request) *ui.Flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: s.cookiePath(), MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	kind, text, ok := strings.Cut(string(raw), ":")
	if !ok || text == "" {
		return nil
	}
	return &ui.Flash{Error: kind == "error", Text: text}
}

// staticFiles serves the stylesheet, Datastar and the components' script.
// The script's name carries a hash of its sources, so browsers may keep it
// for good; the others' names carry no version, so an hour only.
func (s *Site) staticFiles() http.Handler {
	files := http.StripPrefix(s.base+"/static/", http.FileServerFS(ui.Static()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, s.base+"/static/")
		if _, err := fs.Stat(ui.Static(), name); err != nil || name == "" {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "shadcn-templ-") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
