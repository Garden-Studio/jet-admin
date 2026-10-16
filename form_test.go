package jetadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/google/uuid"

	"github.com/garden-studio/jet-admin/example/db/jet/model"
	"github.com/garden-studio/jet-admin/example/db/jet/table"
	admintable "github.com/garden-studio/jet-admin/internal/db/jet/table"
)

func (f *fixture) customer(id uuid.UUID) model.Customers {
	f.t.Helper()
	c := table.Customers
	var m model.Customers
	query, args := pg.SELECT(c.ID, c.Name, c.Email, c.Plan, c.IsActive, c.Preferences, c.Notes, c.CreatedAt).FROM(c).WHERE(c.ID.EQ(pg.UUID(id))).Sql()
	err := f.pool.QueryRow(context.Background(), query, args...).Scan(&m.ID, &m.Name, &m.Email, &m.Plan, &m.IsActive, &m.Preferences, &m.Notes, &m.CreatedAt)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) customerByEmail(email string) (uuid.UUID, bool) {
	c := table.Customers
	var id uuid.UUID
	query, args := pg.SELECT(c.ID).FROM(c).WHERE(c.Email.EQ(pg.String(email))).Sql()
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&id); err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

func TestAddLeavesBlankDefaultsToTheDatabase(t *testing.T) {
	f := newFixture(t)
	r := f.post("/admin/customers/add", url.Values{
		"id": {""}, "name": {"Ada"}, "email": {"ada@example.com"}, "plan": {""},
		"is_active": {"true"}, "preferences": {""}, "notes": {""},
	})
	if r.status != http.StatusSeeOther || r.location() != "/admin/customers/" {
		t.Fatalf("status %d to %q: %s", r.status, r.location(), r.body)
	}
	id, ok := f.customerByEmail("ada@example.com")
	if !ok {
		t.Fatal("no customer was added")
	}
	got := f.customer(id)
	if id == uuid.Nil || got.Plan != "free" || string(got.Preferences) != "{}" || got.Notes != nil || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("defaults not applied: %+v", got)
	}

	t.Run("an identity key, a text array and a nullable timestamp", func(t *testing.T) {
		r := f.post("/admin/orders/add", url.Values{
			"id": {""}, "customer_id": {id.String()}, "status": {"paid"}, "total_cents": {"1999"},
			"tags": {"gift\r\n\r\n  rush \n"}, "shipped_at": {"2026-03-04T05:06:07"},
		})
		if r.status != http.StatusSeeOther {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		o := table.Orders
		var tags []string
		var shipped time.Time
		var cents int32
		query, args := pg.SELECT(o.Tags, o.ShippedAt, o.TotalCents).FROM(o).WHERE(o.CustomerID.EQ(pg.UUID(id))).Sql()
		must(t, f.pool.QueryRow(context.Background(), query, args...).Scan(&tags, &shipped, &cents))
		if !reflect.DeepEqual(tags, []string{"gift", "rush"}) || cents != 1999 || !shipped.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) {
			t.Errorf("tags %q, cents %d, shipped %v", tags, cents, shipped)
		}
	})

	t.Run("a GENERATED ALWAYS key is not on the form", func(t *testing.T) {
		body := f.get("/admin/api_tokens/add").body
		lacks(t, body, `name="id"`)
		r := f.post("/admin/api_tokens/add", url.Values{"customer_id": {id.String()}, "label": {"CI"}})
		if r.status != http.StatusSeeOther {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		if n := f.count(table.APITokens, table.APITokens.Label.EQ(pg.String("CI"))); n != 1 {
			t.Errorf("%d tokens", n)
		}
	})

	t.Run("an add link fills the foreign key in", func(t *testing.T) {
		body := f.get("/admin/orders/add?customer_id=" + id.String()).body
		contains(t, body, `name="customer_id" value="`+id.String()+`"`)
	})

	t.Run("the add form shows constant defaults, and no zero values", func(t *testing.T) {
		body := f.get("/admin/customers/add").body
		contains(t, body, `value="free" selected>`, `name="is_active" value="true" checked`)
		blankInput(t, body, "id")
		lacks(t, body, uuid.Nil.String())
		blankInput(t, f.get("/admin/orders/add").body, "total_cents")
	})
}

func TestChange(t *testing.T) {
	f := newFixture(t)
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	id := f.addCustomer("Ada", "ada@example.com", "free", true, created)
	path := "/admin/customers/" + id.String() + "/"

	page := f.get(path)
	if page.status != http.StatusOK {
		t.Fatalf("status %d", page.status)
	}
	contains(t, page.body, `value="Ada"`, "2025-01-02 03:04:05 UTC", `href="`+path+`delete"`, `href="`+path+`history"`)

	r := f.post(path, url.Values{
		"name": {"Ada Lovelace"}, "email": {"ada@example.com"}, "plan": {"team"},
		"preferences": {`{"theme": "dark"}`}, "notes": {"Line one\r\nline two"},
		// Not writable, so ignored however they are posted.
		"created_at": {"2000-01-01T00:00:00"}, "id": {uuid.NewString()},
		"_continue": {"1"},
	})
	if r.status != http.StatusSeeOther || r.location() != path {
		t.Fatalf("status %d to %q: %s", r.status, r.location(), r.body)
	}
	got := f.customer(id)
	if got.Name != "Ada Lovelace" || got.Plan != "team" || got.IsActive || got.Notes == nil || *got.Notes != "Line one\nline two" {
		t.Errorf("not saved: %+v", got)
	}
	var prefs map[string]string
	if json.Unmarshal(got.Preferences, &prefs) != nil || prefs["theme"] != "dark" {
		t.Errorf("preferences %s", got.Preferences)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("read-only created_at changed to %v", got.CreatedAt)
	}

	t.Run("the flash message shows once", func(t *testing.T) {
		flash := &http.Cookie{Name: "jetadmin_flash"}
		for _, c := range (&http.Response{Header: r.header}).Cookies() {
			if c.Name == flash.Name {
				flash = c
			}
		}
		if flash.Value == "" {
			t.Fatal("no flash cookie")
		}
		next := f.do(request{path: path, signedIn: true, cookies: []*http.Cookie{flash}})
		contains(t, next.body, "was changed. You may edit it again below.")
	})

	t.Run("a missing row is 404", func(t *testing.T) {
		for _, p := range []string{"/admin/customers/" + uuid.NewString() + "/", "/admin/customers/not-a-uuid/", "/admin/nothing/", "/admin/orders/12x/"} {
			if s := f.get(p).status; s != http.StatusNotFound {
				t.Errorf("%s: status %d", p, s)
			}
		}
	})

	t.Run("an inline lists the related rows with an add link", func(t *testing.T) {
		f.addOrder(id, "paid", 4200)
		body := f.get(path).body
		contains(t, body, "4200", `href="/admin/orders/add?customer_id=`+id.String()+`"`)
	})
}

func TestFormErrors(t *testing.T) {
	f := newFixture(t)
	taken := f.addCustomer("Taken", "taken@example.com", "free", true, time.Now())
	valid := url.Values{"name": {"New"}, "email": {"new@example.com"}, "plan": {"pro"}, "preferences": {"{}"}}
	with := func(changes ...string) url.Values {
		form := url.Values{}
		for k, v := range valid {
			form[k] = v
		}
		for i := 0; i < len(changes); i += 2 {
			form.Set(changes[i], changes[i+1])
		}
		return form
	}
	tests := []struct {
		name  string
		path  string
		form  url.Values
		field string
		want  string
	}{
		{"unique violation", "/admin/customers/add", with("email", "taken@example.com"), "email", "Another customer already has this email. (constraint customers_email_key)"},
		{"check violation on a column", "/admin/customers/add", with("name", ""), "name", "constraint customers_name_check"},
		{"invalid JSON", "/admin/customers/add", with("preferences", "{nope"), "preferences", "Enter valid JSON."},
		{"not a choice", "/admin/customers/add", with("plan", "gold"), "plan", "Select a valid choice."},
		{"foreign key violation", "/admin/orders/add", url.Values{"customer_id": {uuid.NewString()}, "status": {"paid"}, "total_cents": {"1"}}, "customer_id", "constraint orders_customer_id_fkey"},
		{"check violation", "/admin/orders/add", url.Values{"customer_id": {taken.String()}, "total_cents": {"-1"}}, "total_cents", "constraint orders_total_cents_check"},
		{"not a number", "/admin/orders/add", url.Values{"customer_id": {taken.String()}, "total_cents": {"lots"}}, "total_cents", "Enter a whole number"},
		{"out of range", "/admin/orders/add", url.Values{"customer_id": {taken.String()}, "total_cents": {"99999999999"}}, "total_cents", "Enter a whole number"},
		{"not a uuid", "/admin/orders/add", url.Values{"customer_id": {"nope"}, "total_cents": {"1"}}, "customer_id", "Enter a valid UUID."},
		{"required", "/admin/orders/add", url.Values{"customer_id": {""}, "total_cents": {"1"}}, "customer_id", "This field is required."},
		{"bad timestamp", "/admin/orders/add", url.Values{"customer_id": {taken.String()}, "total_cents": {"1"}, "shipped_at": {"yesterday"}}, "shipped_at", "Enter a date and time, in UTC."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := f.post(tt.path, tt.form)
			if r.status != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			fieldError(t, r.body, tt.field, htmlEscape(tt.want))
			contains(t, r.body, "Please correct the errors below.")
		})
	}
	if n := f.count(table.Customers, nil); n != 1 {
		t.Errorf("%d customers after refused adds", n)
	}

	t.Run("Datastar gets the form patched with its errors", func(t *testing.T) {
		r := f.do(request{path: "/admin/customers/add", form: with("email", "taken@example.com"), signedIn: true, datastar: true})
		if !r.isSSE() {
			t.Fatalf("Content-Type %q", r.header.Get("Content-Type"))
		}
		contains(t, r.body, "datastar-patch-elements", `id="change-form"`, "customers_email_key", `value="New"`)
		lacks(t, r.body, "<html")
	})

	t.Run("Datastar is redirected after a save", func(t *testing.T) {
		r := f.do(request{path: "/admin/customers/add", form: valid, signedIn: true, datastar: true})
		if !r.isSSE() {
			t.Fatalf("Content-Type %q: %s", r.header.Get("Content-Type"), r.body)
		}
		contains(t, r.body, "window.location", "/admin/customers/")
	})

	t.Run("a hook's FieldErrors land on their field", func(t *testing.T) {
		r := f.post("/admin/admin_users/add", url.Values{"username": {"bob"}, "is_active": {"true"}, "password": {"long enough pw"}, "password_confirm": {"different"}})
		if r.status != http.StatusUnprocessableEntity {
			t.Fatalf("status %d", r.status)
		}
		fieldError(t, r.body, "password_confirm", "The two passwords differ.")
	})
}

// fieldError checks the error shown under field holds want.
func fieldError(t *testing.T, body, field, want string) {
	t.Helper()
	marker := `id="field-` + field + `-error"`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no error on %s", field)
	}
	_, rest, _ := strings.Cut(body[i:], ">")
	text, _, _ := strings.Cut(rest, "<")
	if !strings.Contains(text, want) {
		t.Errorf("error on %s is %q, want %q", field, text, want)
	}
}

// blankInput checks that the input named name is on the page with no value.
func blankInput(t *testing.T, body, name string) {
	t.Helper()
	i := strings.Index(body, `name="`+name+`"`)
	if i < 0 {
		t.Fatalf("no input %s", name)
	}
	tag, _, _ := strings.Cut(body[i:], ">")
	if strings.Contains(tag, `value="`) && !strings.Contains(tag, `value=""`) {
		t.Errorf("input %s has a value: %s", name, tag)
	}
}

func htmlEscape(s string) string {
	return strings.NewReplacer(`"`, "&#34;", "'", "&#39;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	ada := f.addCustomer("Ada", "ada@example.com", "free", true, now)
	grace := f.addCustomer("Grace", "grace@example.com", "free", true, now)
	alan := f.addCustomer("Alan", "alan@example.com", "free", true, now)
	kept := f.addCustomer("Kept", "kept@example.com", "free", true, now)
	path := "/admin/customers/" + ada.String() + "/delete"

	confirm := f.get(path)
	if confirm.status != http.StatusOK {
		t.Fatalf("status %d", confirm.status)
	}
	contains(t, confirm.body, "Delete customer?", "Ada", `name="confirm" value="yes"`)

	t.Run("Datastar gets the confirmation as a dialog", func(t *testing.T) {
		r := f.do(request{path: path, signedIn: true, datastar: true})
		contains(t, r.body, "datastar-patch-elements", `id="dialog"`, `role="alertdialog"`)
	})

	t.Run("a post without confirm deletes nothing", func(t *testing.T) {
		r := f.post(path, url.Values{})
		if r.status != http.StatusSeeOther || r.location() != path {
			t.Fatalf("status %d to %q", r.status, r.location())
		}
		if _, ok := f.customerByEmail("ada@example.com"); !ok {
			t.Fatal("deleted without confirmation")
		}
	})

	r := f.post(path, url.Values{"confirm": {"yes"}})
	if r.status != http.StatusSeeOther || r.location() != "/admin/customers/" {
		t.Fatalf("status %d to %q", r.status, r.location())
	}
	if _, ok := f.customerByEmail("ada@example.com"); ok {
		t.Fatal("not deleted")
	}

	t.Run("a referenced row is refused with the constraint", func(t *testing.T) {
		f.addOrder(kept, "paid", 100)
		r := f.post("/admin/customers/"+kept.String()+"/delete", url.Values{"confirm": {"yes"}})
		if r.status != http.StatusSeeOther {
			t.Fatalf("status %d", r.status)
		}
		if _, ok := f.customerByEmail("kept@example.com"); !ok {
			t.Fatal("deleted a referenced row")
		}
		contains(t, decodeFlash(t, r), "cannot be deleted", "orders_customer_id_fkey")
	})

	t.Run("bulk delete asks, then deletes the checked rows", func(t *testing.T) {
		form := url.Values{"action": {"delete"}, "pk": {grace.String(), alan.String(), "garbage"}}
		ask := f.post("/admin/customers/", form)
		if ask.status != http.StatusOK {
			t.Fatalf("status %d", ask.status)
		}
		contains(t, ask.body, "Delete 2 customers?", "Grace", "Alan", `name="pk" value="`+grace.String()+`"`)
		if f.count(table.Customers, nil) != 3 {
			t.Fatal("deleted before the confirmation")
		}
		form.Set("confirm", "yes")
		done := f.post("/admin/customers/", form)
		if done.status != http.StatusSeeOther {
			t.Fatalf("status %d: %s", done.status, done.body)
		}
		if n := f.count(table.Customers, nil); n != 1 {
			t.Errorf("%d customers left, want 1", n)
		}
		contains(t, decodeFlash(t, done), "Deleted 2 customers.")
	})

	t.Run("bulk delete with nothing checked", func(t *testing.T) {
		r := f.post("/admin/customers/", url.Values{"action": {"delete"}})
		if r.status != http.StatusSeeOther {
			t.Fatalf("status %d", r.status)
		}
		contains(t, decodeFlash(t, r), "Select the customers to delete first.")
	})
}

func decodeFlash(t *testing.T, r response) string {
	t.Helper()
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == "jetadmin_flash" && c.Value != "" {
			return flashText(t, c)
		}
	}
	t.Fatal("no flash message")
	return ""
}

func flashText(t *testing.T, c *http.Cookie) string {
	t.Helper()
	raw, err := base64URLDecode(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHiddenColumnsNeverReachThePage(t *testing.T) {
	f := newFixture(t)
	id := f.addCustomer("Ada", "ada@example.com", "free", true, time.Now())
	k := table.APITokens
	secret := []byte("SUPER-SECRET-TOKEN-BYTES")
	var tokenID int64
	f.scalar(k.INSERT(k.CustomerID, k.Label, k.Secret).VALUES(pg.UUID(id), pg.String("deploys"), pg.Bytea(secret)).RETURNING(k.ID), &tokenID)
	tokenPath := "/admin/api_tokens/" + strconv.FormatInt(tokenID, 10) + "/"

	for _, p := range []string{"/admin/api_tokens/", tokenPath, "/admin/api_tokens/add", "/admin/api_tokens/?q=SUPER", "/admin/admin_users/", "/admin/admin_users/" + f.user.ID.String() + "/"} {
		body := f.get(p).body
		lacks(t, body, "SUPER-SECRET", "Secret", `name="secret"`, "password_hash", "Password hash", "$2a$")
	}

	t.Run("posted anyway, a hidden column is not written", func(t *testing.T) {
		r := f.post(tokenPath, url.Values{"customer_id": {id.String()}, "label": {"renamed"}, "secret": {"overwritten"}})
		if r.status != http.StatusSeeOther {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		var got []byte
		var label string
		query, args := pg.SELECT(k.Secret, k.Label).FROM(k).WHERE(k.ID.EQ(pg.Int64(tokenID))).Sql()
		must(t, f.pool.QueryRow(context.Background(), query, args...).Scan(&got, &label))
		if string(got) != string(secret) || label != "renamed" {
			t.Errorf("secret %q, label %q", got, label)
		}
	})
}

func TestAuditLog(t *testing.T) {
	f := newFixture(t)
	log := admintable.AdminLog.FromSchema("")
	f.post("/admin/customers/add", url.Values{"name": {"Ada"}, "email": {"ada@example.com"}, "plan": {"free"}, "preferences": {"{}"}})
	id, _ := f.customerByEmail("ada@example.com")
	path := "/admin/customers/" + id.String() + "/"
	f.post(path, url.Values{"name": {"Ada L"}, "email": {"ada@example.com"}, "plan": {"pro"}, "preferences": {"{}"}, "notes": {"x"}})
	f.post(path+"delete", url.Values{"confirm": {"yes"}})

	type entry struct {
		Action             int16
		Username, ObjectID string
		Model, ObjectRepr  string
		ChangeMessage      string
		UserID             *uuid.UUID
	}
	query, args := pg.SELECT(log.Action, log.Username, log.ObjectID, log.Model, log.ObjectRepr, log.ChangeMessage, log.UserID).FROM(log).ORDER_BY(log.ID).Sql()
	rows, err := f.pool.Query(context.Background(), query, args...)
	must(t, err)
	var got []entry
	for rows.Next() {
		var e entry
		must(t, rows.Scan(&e.Action, &e.Username, &e.ObjectID, &e.Model, &e.ObjectRepr, &e.ChangeMessage, &e.UserID))
		got = append(got, e)
	}
	must(t, rows.Err())
	want := []entry{
		{1, "ann", id.String(), "customers", "Ada", "Added.", &f.user.ID},
		{2, "ann", id.String(), "customers", "Ada L", "Changed name, plan and notes.", &f.user.ID},
		{3, "ann", id.String(), "customers", "Ada L", "Deleted.", &f.user.ID},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("log\n got %+v\nwant %+v", got, want)
	}
	contains(t, f.get("/admin/").body, "Recent actions", "Ada L")
	contains(t, f.get("/admin/log").body, "Added.", "Deleted.")
	f.addCustomer("Grace", "grace@example.com", "free", true, time.Now())
	gid, _ := f.customerByEmail("grace@example.com")
	gpath := "/admin/customers/" + gid.String() + "/"
	f.post(gpath, url.Values{"name": {"Grace"}, "email": {"grace@example.com"}, "plan": {"team"}, "preferences": {"{}"}})
	history := f.get(gpath + "history").body
	contains(t, history, "Changed plan and is active.")
	lacks(t, history, "Changed name, plan and notes.")
}
