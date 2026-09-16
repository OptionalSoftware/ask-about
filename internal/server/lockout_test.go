package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/optionalsoftware/ask-about/internal/config"
)

func lockedAdmin(max int) config.Admin {
	return config.Admin{Username: testUser, Password: testPass, MaxAttempts: max}
}

// Three wrong passwords and that address is done for the window.
func TestAdminLocksOutAfterFailures(t *testing.T) {
	h, _ := testServer(t, lockedAdmin(3))

	try := func(pass string) int {
		r := httptest.NewRequest("GET", Base+"/admin", nil)
		r.RemoteAddr = "203.0.113.5:9000"
		r.SetBasicAuth(testUser, pass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	for i := 1; i <= 3; i++ {
		if got := try("guess"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, got)
		}
	}
	if got := try("guess"); got != http.StatusTooManyRequests {
		t.Errorf("fourth attempt = %d, want 429", got)
	}
	// Even the right password is refused while locked out — otherwise the
	// limit only slows down someone who never guesses correctly.
	if got := try(testPass); got != http.StatusTooManyRequests {
		t.Errorf("correct password while locked out = %d, want 429", got)
	}
}

// Counted per address, so someone guessing cannot lock the owner out.
func TestLockoutIsPerAddress(t *testing.T) {
	h, _ := testServer(t, lockedAdmin(3))

	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", Base+"/admin", nil)
		r.RemoteAddr = "198.51.100.9:9000"
		r.SetBasicAuth(testUser, "guess")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	r := httptest.NewRequest("GET", Base+"/admin", nil)
	r.RemoteAddr = "203.0.113.5:9000"
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("owner locked out by someone else's guessing: %d", w.Code)
	}
}

// A typo before the right password must not count against you.
func TestSuccessClearsTheCount(t *testing.T) {
	h, _ := testServer(t, lockedAdmin(3))
	const from = "203.0.113.5:9000"

	send := func(pass string) int {
		r := httptest.NewRequest("GET", Base+"/admin", nil)
		r.RemoteAddr = from
		r.SetBasicAuth(testUser, pass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	send("typo")
	send("typo")
	if got := send(testPass); got != http.StatusOK {
		t.Fatalf("correct password after two typos = %d, want 200", got)
	}
	// The count is cleared, so two more typos are not the fourth and fifth.
	send("typo")
	send("typo")
	if got := send(testPass); got != http.StatusOK {
		t.Errorf("success did not clear the count: %d", got)
	}
}

// The window expires, or a mistyped password locks you out until you restart.
func TestLockoutExpires(t *testing.T) {
	l := newLockout(3, 15*time.Minute)
	clock := time.Now()
	l.now = func() time.Time { return clock }
	addr := netip.MustParseAddr("203.0.113.5")

	for i := 0; i < 3; i++ {
		l.failed(addr)
	}
	if !l.lockedOut(addr) {
		t.Fatal("not locked out after three failures")
	}

	clock = clock.Add(16 * time.Minute)
	if l.lockedOut(addr) {
		t.Error("still locked out after the window passed")
	}
	if len(l.tracked) != 0 {
		t.Errorf("expired entry was not dropped: %d left", len(l.tracked))
	}
}

// Zero disables it, which the config documents.
func TestLockoutDisabled(t *testing.T) {
	h, _ := testServer(t, lockedAdmin(0))
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("GET", Base+"/admin", nil)
		r.RemoteAddr = "203.0.113.5:9000"
		r.SetBasicAuth(testUser, "guess")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401 with the limit off", i, w.Code)
		}
	}
}

// Entries from addresses that stopped trying must not accumulate.
func TestLockoutPrunesOldEntries(t *testing.T) {
	l := newLockout(3, 15*time.Minute)
	clock := time.Now()
	l.now = func() time.Time { return clock }

	for i := 0; i < 100; i++ {
		l.failed(netip.MustParseAddr("198.51.100." + string(rune('0'+i%10))))
	}
	before := len(l.tracked)
	clock = clock.Add(30 * time.Minute)
	l.failed(netip.MustParseAddr("203.0.113.5"))
	if len(l.tracked) >= before {
		t.Errorf("stale entries kept: %d before, %d after", before, len(l.tracked))
	}
}

// A page you visit while logged in must not be able to act as you. The browser
// attaches saved Basic auth credentials to a cross-site form post by itself.
func TestAdminRejectsCrossOriginPost(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))
	form := url.Values{"name": {"Acme Corp"}}

	r := httptest.NewRequest("POST", Base+"/admin/links", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	r.Host = "ask-about.example.com"
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin post = %d, want 403", w.Code)
	}
	if contacts, _ := db.ListContacts(t.Context()); len(contacts) != 0 {
		t.Errorf("cross-origin post created %d contacts", len(contacts))
	}
}

func TestAdminAcceptsSameOriginPost(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))
	form := url.Values{"name": {"Acme Corp"}}

	r := httptest.NewRequest("POST", Base+"/admin/links", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://ask-about.example.com")
	r.Host = "ask-about.example.com"
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("same-origin post = %d, want 200", w.Code)
	}
	if contacts, _ := db.ListContacts(t.Context()); len(contacts) != 1 {
		t.Errorf("same-origin post created %d contacts, want 1", len(contacts))
	}
}

// curl sends no Origin. Rejecting that would break scripting for no gain,
// since CSRF requires a browser.
func TestAdminAllowsPostWithNoOrigin(t *testing.T) {
	h, db := testServer(t, enabledAdmin(""))
	form := url.Values{"name": {"Acme Corp"}}

	r := httptest.NewRequest("POST", Base+"/admin/links", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("post with no Origin = %d, want 200", w.Code)
	}
	if contacts, _ := db.ListContacts(t.Context()); len(contacts) != 1 {
		t.Errorf("got %d contacts, want 1", len(contacts))
	}
}

// A caller who reaches the binary directly cannot dodge the lockout by
// sending a different X-Forwarded-For each time: with no trusted proxy the
// header is ignored and every attempt counts against the real address.
func TestLockoutIgnoresForgedForwardedFor(t *testing.T) {
	h, _ := testServer(t, config.Admin{Username: testUser, Password: testPass, MaxAttempts: 3})
	attempt := func(forged string) int {
		r := httptest.NewRequest("GET", "/ask-about/admin", nil)
		r.RemoteAddr = "198.51.100.9:9000"
		r.Header.Set("X-Forwarded-For", forged)
		r.SetBasicAuth(testUser, "wrong")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 1; i <= 3; i++ {
		if got := attempt("203.0.113." + string(rune('0'+i))); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, got)
		}
	}
	if got := attempt("203.0.113.9"); got != http.StatusTooManyRequests {
		t.Errorf("fourth attempt with yet another forged address = %d, want 429", got)
	}
}
