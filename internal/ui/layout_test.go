package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// A host with a Content-Security-Policy puts its nonce in the context
// (templ.WithNonce): every script the admin loads carries it, from under
// BasePath.
func TestScriptsCarryTheNonce(t *testing.T) {
	ctx := templ.WithNonce(context.Background(), "n0nce")
	var b strings.Builder
	page := Message(MessagePage{Chrome: Chrome{SiteTitle: "Admin", Base: "/admin"}, Heading: "Not found"})
	if err := page.Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	scripts := regexp.MustCompile(`<script[^>]*>`).FindAllString(b.String(), -1)
	if len(scripts) != 2 {
		t.Fatalf("%d scripts: %v", len(scripts), scripts)
	}
	for _, s := range scripts {
		if !strings.Contains(s, `nonce="n0nce"`) || !strings.Contains(s, `src="/admin/static/`) {
			t.Errorf("script %s", s)
		}
	}
}

// The host's stylesheets come after the admin's, so their variables win.
func TestHostStylesheetsFollowTheAdmins(t *testing.T) {
	var b strings.Builder
	page := Message(MessagePage{Chrome: Chrome{SiteTitle: "Admin", Base: "/admin", Styles: []string{"/theme.css"}}})
	if err := page.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	own, host := strings.Index(b.String(), `href="/admin/static/admin.css"`), strings.Index(b.String(), `href="/theme.css"`)
	if own < 0 || host < own {
		t.Errorf("admin.css at %d, the host's at %d", own, host)
	}
}
