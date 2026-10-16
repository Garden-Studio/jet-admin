package auth

import (
	"context"
	"strings"

	jetadmin "github.com/garden-studio/jet-admin"
	"github.com/garden-studio/jet-admin/internal/db/jet/model"
	"github.com/garden-studio/jet-admin/internal/db/jet/table"
)

// Register adds the admin users to site: list them, add one with a
// password, change a password, deactivate one. The password hash is
// never selected for a page, shown nor sent.
func Register(site *jetadmin.Site) error {
	t := table.AdminUsers.FromSchema("")
	return jetadmin.Register(site, jetadmin.Model[model.AdminUsers]{
		Table:        t,
		Slug:         "admin_users",
		Label:        "Admin user",
		Plural:       "Admin users",
		Group:        "Authentication",
		ListDisplay:  []jetadmin.Column{t.Username, t.IsActive, t.LastLoginAt, t.CreatedAt},
		ListFilter:   []jetadmin.Column{t.IsActive},
		SearchFields: []jetadmin.Column{t.Username},
		Ordering:     []jetadmin.Order{jetadmin.Asc(t.Username)},
		Hidden:       []jetadmin.Column{t.PasswordHash},
		Computed:     []jetadmin.Column{t.PasswordHash},
		ReadOnly:     []jetadmin.Column{t.CreatedAt, t.LastLoginAt},
		Fields: []jetadmin.Field{
			{Column: t.IsActive, Label: "Active", Help: "An inactive user cannot sign in; their open sessions stop working."},
			{Column: t.LastLoginAt, Label: "Last login"},
		},
		Extra: []jetadmin.ExtraField{
			{Name: "password", Label: "Password", Widget: jetadmin.Password, RequiredOnAdd: true,
				Help: "At least 8 characters. When changing a user, leave blank to keep their password."},
			{Name: "password_confirm", Label: "Password again", Widget: jetadmin.Password},
		},
		Display: func(u model.AdminUsers) string { return u.Username },
		BeforeSave: func(_ context.Context, u *model.AdminUsers, save jetadmin.Save) error {
			u.Username = strings.TrimSpace(u.Username)
			password := save.Extra["password"]
			if password == "" && !save.Adding {
				return nil
			}
			if password != save.Extra["password_confirm"] {
				return jetadmin.FieldErrors{"password_confirm": "The two passwords differ."}
			}
			hash, err := HashPassword(password)
			if err != nil {
				return jetadmin.FieldErrors{"password": capitalize(err.Error()) + "."}
			}
			u.PasswordHash = hash
			return nil
		},
		Validate: func(_ context.Context, u *model.AdminUsers) error {
			if u.Username == "" {
				return jetadmin.FieldErrors{"username": "This field is required."}
			}
			return nil
		},
	})
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
