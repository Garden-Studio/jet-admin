package jetadmin_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestList(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	f.addCustomer("Ada Lovelace", "ada@example.com", "team", true, now.Add(-2*time.Hour))
	f.addCustomer("Grace Hopper", "grace@example.com", "pro", true, now.Add(-3*24*time.Hour))
	f.addCustomer("Alan Turing", "alan@example.com", "free", false, now.Add(-20*24*time.Hour))
	f.addCustomer("Katherine Johnson", "kj@example.com", "pro", true, now.Add(-400*24*time.Hour))
	f.addCustomer("Barbara 100% Liskov", "barbara@example.com", "team", true, now.Add(-40*24*time.Hour))

	tests := []struct {
		name    string
		query   string
		total   string
		present []string
		absent  []string
	}{
		{"everything, first page of three", "", "5 customers", []string{"Ada Lovelace", "Alan Turing", "Barbara 100% Liskov"}, []string{"Grace Hopper"}},
		{"second page", "?p=2", "5 customers", []string{"Grace Hopper", "Katherine Johnson"}, []string{"Ada Lovelace"}},
		{"a page past the last shows the last", "?p=9", "5 customers", []string{"Katherine Johnson"}, []string{"Ada Lovelace"}},
		{"search by name, any case", "?q=HOPPER", "1 customer", []string{"Grace Hopper"}, []string{"Ada Lovelace"}},
		{"search by email", "?q=kj@", "1 customer", []string{"Katherine Johnson"}, nil},
		{"every word must match", "?q=ada+example", "1 customer", []string{"Ada Lovelace"}, []string{"Alan Turing"}},
		{"a percent sign is not a wildcard", "?q=100%25", "1 customer", []string{"Barbara 100% Liskov"}, []string{"Ada"}},
		{"choice filter", "?f_plan=pro", "2 customers", []string{"Grace Hopper", "Katherine Johnson"}, []string{"Ada Lovelace"}},
		{"bool filter", "?f_is_active=false", "1 customer", []string{"Alan Turing"}, []string{"Grace Hopper"}},
		{"time filter, past 7 days", "?f_created_at__since=7d", "2 customers", []string{"Ada Lovelace", "Grace Hopper"}, []string{"Alan Turing"}},
		{"time filter, today", "?f_created_at__since=today", "", nil, []string{"Katherine Johnson", "Alan Turing"}},
		{"distinct value filter", "?f_email=alan%40example.com", "1 customer", []string{"Alan Turing"}, nil},
		{"search and filter together", "?q=a&f_plan=team", "2 customers", []string{"Ada Lovelace", "Barbara 100% Liskov"}, []string{"Grace"}},
		{"sorted by a header, descending", "?o=-name", "5 customers", []string{"Katherine Johnson", "Grace Hopper", "Barbara 100% Liskov"}, []string{"Ada Lovelace"}},
		{"an unknown filter value matches nothing", "?f_is_active=maybe", "0 customers", nil, []string{"Ada Lovelace"}},
		{"an unknown column is ignored", "?f_nope=1", "5 customers", []string{"Ada Lovelace"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := f.get("/admin/customers/" + tt.query)
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			if tt.total != "" {
				contains(t, r.body, `aria-live="polite">`+tt.total+`<`)
			}
			contains(t, r.body, tt.present...)
			lacks(t, r.body, tt.absent...)
		})
	}

	t.Run("sorted descending keeps the order of the rows", func(t *testing.T) {
		body := f.get("/admin/customers/?o=-name").body
		if strings.Index(body, "Katherine Johnson") > strings.Index(body, "Grace Hopper") {
			t.Error("Katherine is not before Grace")
		}
	})

	t.Run("the filter sidebar lists the choices and distinct values", func(t *testing.T) {
		body := f.get("/admin/customers/").body
		contains(t, body, "By plan", "By is active", "By created at", "Past 7 days", "This year", "ada@example.com")
	})

	t.Run("pagination links keep the search", func(t *testing.T) {
		body := f.get("/admin/customers/?q=a").body
		contains(t, body, `href="/admin/customers/?p=2&amp;q=a"`)
	})

	t.Run("Datastar gets the results patched and the address replaced", func(t *testing.T) {
		r := f.do(request{path: "/admin/customers/?q=grace", signedIn: true, datastar: true})
		if !r.isSSE() {
			t.Fatalf("Content-Type %q, want an event stream", r.header.Get("Content-Type"))
		}
		contains(t, r.body, "event: datastar-patch-elements", `id="changelist"`, `id="changelist-params"`, "Grace Hopper", "history.replaceState")
		lacks(t, r.body, "<html", "Ada Lovelace")
	})

	t.Run("foreign keys show the related row by name", func(t *testing.T) {
		ada := f.addCustomer("Ada Two", "ada2@example.com", "pro", true, now)
		f.addOrder(ada, "paid", 1200)
		body := f.get("/admin/orders/").body
		contains(t, body, ">Ada Two<", `href="/admin/customers/`+ada.String()+`/"`, "<th scope=\"col\" class=\"\" aria-sort=\"none\"><a href=\"/admin/orders/?o=customer_id\"")
	})
}
