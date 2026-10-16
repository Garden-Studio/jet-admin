// Package core holds what both the admin and the auth package need, so
// neither imports the other for it: auth registers its pages on a Site, and
// the Site takes an Authenticator. The public names are aliases of these
// (jetadmin.Authenticator, auth.User).
package core

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// User is the admin signed in on a request.
type User struct {
	ID       uuid.UUID
	Username string
}

// Authenticator tells who is signed in on a request.
type Authenticator interface {
	// Authenticate returns the admin user signed in on r.
	Authenticate(r *http.Request) (User, bool)
}

// LoginProvider is an Authenticator that also serves the sign-in and
// sign-out pages; the Site mounts them at BasePath/login and
// BasePath/logout.
type LoginProvider interface {
	Authenticator
	LoginHandler() http.Handler
	LogoutHandler() http.Handler
}

// Chrome is what a page outside the Site's own handlers (the login page of
// an Authenticator) needs to look like the rest of the admin. The Site puts
// it in the request's context.
type Chrome struct {
	Title    string
	BasePath string
}

type chromeKey struct{}

// WithChrome returns ctx carrying c.
func WithChrome(ctx context.Context, c Chrome) context.Context {
	return context.WithValue(ctx, chromeKey{}, c)
}

// ChromeFrom is the Chrome of ctx, or a plain one when the handler runs
// outside a Site (a test).
func ChromeFrom(ctx context.Context) Chrome {
	if c, ok := ctx.Value(chromeKey{}).(Chrome); ok {
		return c
	}
	return Chrome{Title: "Admin", BasePath: ""}
}

// SafeNext is where to go after signing in: next when it is a path under
// base, else base's index. Never another host.
func SafeNext(base, next string) string {
	if next == "" || !strings.HasPrefix(next, base+"/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return base + "/"
	}
	return next
}
