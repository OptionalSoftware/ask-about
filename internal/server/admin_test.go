package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/optionalsoftware/ask-about/internal/config"
	"github.com/optionalsoftware/ask-about/internal/store"
)

func testServer(t *testing.T, admin config.Admin) (http.Handler, store.Store) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := New(nil, nil, fstest.MapFS{}, Options{Store: db, Admin: admin},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	h, err := s.Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h, db
}

const (
	testUser = "daniel"
	testPass = "correct horse"
)

func enabledAdmin(ip string) config.Admin {
	return config.Admin{Username: testUser, Password: testPass, IP: ip}
}

func TestAdminRequiresCredentials(t *testing.T) {
	h, _ := testServer(t, enabledAdmin(""))

	tests := []struct {
		name       string
		user, pass string
		auth       bool
		want       int
	}{
		{name: "no credentials", want: http.StatusUnauthorized},
		{name: "wrong password", user: testUser, pass: "guess", auth: true, want: http.StatusUnauthorized},
		{name: "wrong user", user: "eve", pass: testPass, auth: true, want: http.StatusUnauthorized},
		{name: "correct", user: testUser, pass: testPass, auth: true, want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/ask-about/admin", nil)
			if tc.auth {
				r.SetBasicAuth(tc.user, tc.pass)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// The address is checked before the password, so a caller from the wrong
// network is refused whether or not their credentials are right.
func TestAdminAddressRestriction(t *testing.T) {
	tests := []struct {
		name, allow, from string
		want              int
	}{
		{"exact match", "203.0.113.5", "203.0.113.5:9000", http.StatusOK},
		{"different address", "203.0.113.5", "203.0.113.6:9000", http.StatusForbidden},
		{"inside CIDR", "203.0.113.0/24", "203.0.113.99:9000", http.StatusOK},
		{"outside CIDR", "203.0.113.0/24", "203.0.114.1:9000", http.StatusForbidden},
		{"unrestricted", "", "198.51.100.7:9000", http.StatusOK},
		{"ipv6 exact", "2001:db8::1", "[2001:db8::1]:9000", http.StatusOK},
		{"ipv6 mismatch", "2001:db8::1", "[2001:db8::2]:9000", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := testServer(t, enabledAdmin(tc.allow))
			r := httptest.NewRequest("GET", "/ask-about/admin", nil)
			r.RemoteAddr = tc.from
			r.SetBasicAuth(testUser, testPass)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("from %s allow %q: status = %d, want %d",
					tc.from, tc.allow, w.Code, tc.want)
			}
		})
	}
}

// Behind a proxy the connecting address is the proxy's, so the client comes
// from X-Forwarded-For. ask-about has to work both ways: straight on the internet,
// and behind nginx.
func TestAdminAddressBehindAProxy(t *testing.T) {
	const proxy = "127.0.0.1:9000"

	tests := []struct {
		name, allow, remote, forwarded string
		want                           int
	}{
		// No proxy in front: the connection address is the client.
		{"direct, allowed", "203.0.113.5", "203.0.113.5:9000", "", http.StatusOK},
		{"direct, refused", "203.0.113.5", "198.51.100.9:9000", "", http.StatusForbidden},

		// Behind one proxy. Without reading the header this would refuse
		// everyone, since every request arrives from 127.0.0.1.
		{"proxied, allowed", "203.0.113.5", proxy, "203.0.113.5", http.StatusOK},
		{"proxied, refused", "203.0.113.5", proxy, "198.51.100.9", http.StatusForbidden},

		// nginx APPENDS what it saw, so the rightmost entry is the one it
		// vouches for. Everything left of it is whatever the caller claimed —
		// reading the leftmost, which is the usual mistake, would take the
		// forgery instead.
		{"claimed address ignored", "203.0.113.5",
			proxy, "203.0.113.5, 198.51.100.9", http.StatusForbidden},
		{"real address found past a claim", "203.0.113.5",
			proxy, "198.51.100.9, 203.0.113.5", http.StatusOK},

		// A header that is not an address falls back to the connection, rather
		// than refusing everyone because something upstream sent nonsense.
		{"junk header, connection allowed", "203.0.113.5",
			"203.0.113.5:9000", "not-an-address", http.StatusOK},
		{"junk header, connection refused", "203.0.113.5",
			"198.51.100.9:9000", "not-an-address", http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := testServer(t, enabledAdmin(tc.allow))
			r := httptest.NewRequest("GET", "/ask-about/admin", nil)
			r.RemoteAddr = tc.remote
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			r.SetBasicAuth(testUser, testPass)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("remote %s, forwarded %q: status = %d, want %d",
					tc.remote, tc.forwarded, w.Code, tc.want)
			}
		})
	}
}

// The address is one of three things a request has to get right. Getting it
// right is not on its own enough.
func TestAdminAddressIsNotEnough(t *testing.T) {
	h, _ := testServer(t, enabledAdmin("203.0.113.5"))
	r := httptest.NewRequest("GET", "/ask-about/admin", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("X-Forwarded-For", "203.0.113.5")
	// Correct address, no credentials.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — the address alone let it through", w.Code)
	}
}

// With no username and password nothing is registered under /admin, so the
// request falls through to the static handler and gets the chat page. What
// must never happen is the admin page itself rendering unguarded.
func TestAdminDisabledWhenUnconfigured(t *testing.T) {
	h, _ := testServer(t, config.Admin{})
	for _, path := range []string{"/ask-about/admin", "/ask-about/admin/links/abc"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "New link") {
			t.Errorf("%s rendered the admin page with no credentials configured", path)
		}
	}
}

// Creating and revoking must also be unreachable, not merely unlinked.
func TestAdminWritesRejectedWithoutCredentials(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))
	form := url.Values{"name": {"Eve"}}
	r := httptest.NewRequest("POST", "/ask-about/admin/links", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated create: status = %d, want 401", w.Code)
	}
	if contacts, _ := db.ListContacts(t.Context()); len(contacts) != 0 {
		t.Errorf("unauthenticated create wrote %d contacts", len(contacts))
	}
}

func TestAdminCreateInvite(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))

	form := url.Values{
		"name": {"  Acme   Corp  "}, // deliberately messy
		"note": {"staff eng, applied 12 Mar"},
		"days": {"14"},
	}
	r := httptest.NewRequest("POST", "/ask-about/admin/links", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Host = "ask-about.example.com"
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("create: status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/i/acme-corp/") {
		t.Errorf("issued link missing the slugified name:\n%s", body)
	}
	// The note is private: it must not appear in the link handed out.
	if strings.Contains(body, "/i/acme-corp/staff") {
		t.Error("the note leaked into the URL")
	}

	contacts, err := db.ListContacts(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(contacts))
	}
	got := contacts[0]
	if got.Name != "Acme Corp" {
		t.Errorf("name = %q, want whitespace collapsed", got.Name)
	}
	if len(got.Invites) != 1 {
		t.Fatalf("got %d links, want 1", len(got.Invites))
	}
	link := got.Invites[0]
	if link.Note != "staff eng, applied 12 Mar" {
		t.Errorf("note = %q", link.Note)
	}
	// Lookups go through the digest, so the page must show the token itself
	// and never the hash — a link built from the hash would not resolve.
	if strings.Contains(body, link.TokenHash) {
		t.Error("page rendered the stored hash instead of the token")
	}
	if len(link.TokenHash) != 64 {
		t.Errorf("token hash is %d chars, want a sha256 hex digest", len(link.TokenHash))
	}
}

func TestAdminEditsNoteAndExpiry(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))
	ctx := t.Context()

	soon := time.Now().AddDate(0, 0, 7)
	inv, err := db.CreateInvite(ctx, "Acme Corp", "first note", "tok", "hash", &soon)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	post := func(note, expires string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"note": {note}, "expires": {expires}}
		r := httptest.NewRequest("POST", "/ask-about/admin/links/"+inv.ID,
			strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth(testUser, testPass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := post("second note", "2027-03-01")
	// A redirect rather than a rendered page, so refreshing does not save again.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save: status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "?saved=1") {
		t.Errorf("Location = %q, want the detail page with ?saved=1", loc)
	}

	link := onlyInvite(t, db)
	if link.Note != "second note" {
		t.Errorf("note = %q", link.Note)
	}
	if link.ExpiresAt == nil {
		t.Fatal("expiry cleared, want 1 Mar 2027")
	}
	// End of the chosen day in local time, so the link works for all of it.
	if got := link.ExpiresAt.Local().Format("2006-01-02 15:04:05"); got != "2027-03-01 23:59:59" {
		t.Errorf("expiry = %s, want the end of 1 Mar 2027", got)
	}

	// An empty date box is how an expiry is removed.
	if w := post("second note", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("clear: status = %d", w.Code)
	}
	if link := onlyInvite(t, db); link.ExpiresAt != nil {
		t.Errorf("expiry = %v, want none", link.ExpiresAt)
	}

	// A date that cannot be read changes nothing.
	w = post("third note", "the ides of march")
	if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "?err=date") {
		t.Errorf("Location = %q, want ?err=date", loc)
	}
	if link := onlyInvite(t, db); link.Note != "second note" {
		t.Errorf("note = %q, want the save to have been refused whole", link.Note)
	}
}

func onlyInvite(t *testing.T, db store.Store) store.InviteDetail {
	t.Helper()
	contacts, err := db.ListContacts(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(contacts) != 1 || len(contacts[0].Invites) != 1 {
		t.Fatalf("want exactly one contact with one link, got %#v", contacts)
	}
	return contacts[0].Invites[0]
}

func TestAdminRejectsUnusableName(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))
	// Nothing here survives slugification, so there would be no URL to build.
	form := url.Values{"name": {"!!!"}}
	r := httptest.NewRequest("POST", "/ask-about/admin/links", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	contacts, _ := db.ListContacts(t.Context())
	if len(contacts) != 0 {
		t.Errorf("unusable name created %d contacts", len(contacts))
	}
}

// Nothing used is $0.00. A link nobody has opened, and a fresh install with no
// questions yet, have no model to look up a rate for; reporting that as "no
// rates" reads as a missing [pricing] block, which is a different problem.
func TestZeroUsagePricesAsZeroNotUnpriced(t *testing.T) {
	s := &Server{pricing: config.Pricing{}}
	cost, priced := s.cost(store.Usage{})
	if !priced || cost != 0 {
		t.Errorf("zero usage = (%v, priced=%v), want (0, true)", cost, priced)
	}
	// Real usage of a model with no rate is still unpriced.
	if _, priced := s.cost(store.Usage{Model: "m", OutputTokens: 1}); priced {
		t.Error("usage of an unpriced model was reported as priced")
	}
}

// The spend strip reads $0.00 on a fresh install, and "no rates" once a model
// without a [pricing] block has actually been used — the two must not be
// confused, since only the second is a misconfiguration.
func TestSpendStripDistinguishesUnusedFromUnpriced(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{store: db, pricing: config.Pricing{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if v := s.buildSpend(t.Context()); v == nil || !v.Priced {
		t.Fatalf("fresh install: Priced = %v, want true (reads $0.00)", v != nil && v.Priced)
	}

	// A turn on a model nobody configured a rate for.
	err = db.RecordTurn(t.Context(), &store.Turn{
		SessionID: "v1", Question: "q", Answer: "a",
		Usage: store.Usage{Model: "unpriced-model", OutputTokens: 10}, AskedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if v := s.buildSpend(t.Context()); v == nil || v.Priced {
		t.Fatalf("unpriced usage: Priced = %v, want false (reads \"no rates\")", v != nil && v.Priced)
	}
}
