package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/optionalsoftware/ask-about/internal/config"
	"github.com/optionalsoftware/ask-about/internal/corpus"
	"github.com/optionalsoftware/ask-about/internal/llm"
	"github.com/optionalsoftware/ask-about/internal/pipeline"
	"github.com/optionalsoftware/ask-about/internal/store"
)

// stubProvider answers without a network call, so the gate can be tested on
// the real handler path rather than around it.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }

func (stubProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	out := make(chan llm.Chunk, 2)
	out <- llm.Chunk{Text: "An answer. "}
	out <- llm.Chunk{Usage: &llm.Usage{Model: "stub-1", OutputTokens: 3}}
	close(out)
	return out, nil
}

// captureObserver records what the pipeline was asked to run, which is how the
// invite reaches whatever stores the turn.
type captureObserver struct {
	mu   sync.Mutex
	reqs []llm.Request
}

func (c *captureObserver) RunFinished(_ context.Context, req llm.Request, _ pipeline.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
}

func (c *captureObserver) last() (llm.Request, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		return llm.Request{}, false
	}
	return c.reqs[len(c.reqs)-1], true
}

// gatedServer builds a server with invite_only on and a page to serve, since
// the gate has to let the page through while refusing the questions.
func gatedServer(t *testing.T) (http.Handler, store.Store, *captureObserver) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><title>{{.Title}}</title>` +
				`<meta property="og:image" content="{{.Image}}"></head><body></body></html>`)},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe := pipeline.New(stubProvider{}, nil, log)
	seen := &captureObserver{}
	pipe.Observe(seen)

	s := New(pipe, &corpus.Corpus{}, web, Options{
		Store:   db,
		Access:  config.Access{InviteOnly: true},
		Preview: Preview{Title: "Ask about Daniel Reyes"},
	}, log)

	h, err := s.Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h, db, seen
}

// issue creates a link and returns the cookie value a browser would carry.
func issue(t *testing.T, db store.Store, name string, expires *time.Time) string {
	t.Helper()
	token, err := newToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := db.CreateInvite(t.Context(), name, "", token, hashToken(name, token), expires); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	return slugify(name) + "/" + token
}

func chatRequestWith(cookie string) *http.Request {
	r := httptest.NewRequest("POST", "/ask-about/chat",
		strings.NewReader(`{"messages":[{"role":"user","text":"hi"}]}`))
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: inviteCookie, Value: cookie})
	}
	return r
}

// An unfurler has no token. If the gate refused the page, every preview in
// Slack and LinkedIn would be blank — so the page is always served.
func TestInvitePageServedWithoutValidLink(t *testing.T) {
	h, _, _ := gatedServer(t)

	for _, path := range []string{
		Base + "/i/acme-corp/notarealtoken",
		Base + "/i/nobody/aaaaaaaaaaaaaaaa",
		Base + "/",
	} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("User-Agent", "Slackbot-LinkExpanding 1.0")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 so the preview renders", path, w.Code)
		}
		if body := w.Body.String(); !strings.Contains(body, "Ask about Daniel Reyes") {
			t.Errorf("%s: page carries no preview title:\n%s", path, body)
		}
	}
}

// Following a good link is what admits someone; the cookie is how it survives
// navigating away from the /i/ URL.
func TestInviteAdmitsAfterFollowingLink(t *testing.T) {
	h, db, seen := gatedServer(t)
	cookie := issue(t, db, "Acme Corp", nil)

	// Arriving on the link sets the cookie.
	r := httptest.NewRequest("GET", Base+"/i/"+cookie, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	var got string
	for _, c := range w.Result().Cookies() {
		if c.Name == inviteCookie {
			got = c.Value
		}
	}
	if got != cookie {
		t.Fatalf("cookie = %q, want %q", got, cookie)
	}

	// And that cookie admits them.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, chatRequestWith(got))
	if w.Code == http.StatusForbidden {
		t.Fatalf("a valid link was refused: %s", w.Body.String())
	}

	// The turn has to be attributed, or the visits it produces belong to
	// nobody and the whole admin view stays empty.
	req, ok := seen.last()
	if !ok {
		t.Fatal("no run reached the observer")
	}
	if req.Invite == "" {
		t.Error("an admitted turn carried no invite id")
	}
	contacts, _ := db.ListContacts(t.Context())
	if want := contacts[0].Invites[0].ID; req.Invite != want {
		t.Errorf("invite id = %q, want %q", req.Invite, want)
	}
	if req.Session == "" {
		t.Error("an admitted turn carried no session id")
	}
}

func TestInviteRefusals(t *testing.T) {
	past := time.Now().Add(-time.Hour)

	tests := []struct {
		name   string
		cookie func(t *testing.T, db store.Store) string
	}{
		{"no cookie at all", func(*testing.T, store.Store) string { return "" }},
		{"unknown token", func(t *testing.T, db store.Store) string {
			issue(t, db, "Acme Corp", nil)
			return "acme-corp/aaaaaaaaaaaaaaaa"
		}},
		{"expired", func(t *testing.T, db store.Store) string {
			return issue(t, db, "Acme Corp", &past)
		}},
		{"malformed cookie", func(*testing.T, store.Store) string { return "no-slash-here" }},
		// The name is hashed into the digest, so editing the visible half of a
		// link resolves to nothing.
		{"tampered name", func(t *testing.T, db store.Store) string {
			c := issue(t, db, "Acme Corp", nil)
			_, token, _ := strings.Cut(c, "/")
			return "northwind/" + token
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _ := gatedServer(t)
			cookie := tc.cookie(t, db)

			w := httptest.NewRecorder()
			h.ServeHTTP(w, chatRequestWith(cookie))
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}

			// The visitor is never told which refusal applied.
			if body := w.Body.String(); strings.Contains(body, "expired") ||
				strings.Contains(body, "revoked") || strings.Contains(body, "unknown") {
				t.Errorf("refusal leaked its reason: %q", body)
			}
		})
	}
}

// Revocation has to stop the next question, not the next visit — otherwise a
// link stays live for as long as someone keeps the tab open.
func TestRevokeStopsTheNextQuestion(t *testing.T) {
	h, db, _ := gatedServer(t)
	cookie := issue(t, db, "Acme Corp", nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, chatRequestWith(cookie))
	if w.Code == http.StatusForbidden {
		t.Fatalf("refused before revocation: %s", w.Body.String())
	}

	contacts, _ := db.ListContacts(t.Context())
	if err := db.RevokeInvite(t.Context(), contacts[0].Invites[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, chatRequestWith(cookie))
	if w.Code != http.StatusForbidden {
		t.Errorf("status after revoking = %d, want 403", w.Code)
	}
}

// The page tells a refused visitor before they type, and offers no starter
// prompts that would only be refused.
func TestConfigReportsAdmission(t *testing.T) {
	h, db, _ := gatedServer(t)
	cookie := issue(t, db, "Acme Corp", nil)

	for _, tc := range []struct {
		name         string
		cookie       string
		wantAdmitted string
	}{
		{"valid link", cookie, `"admitted":true`},
		{"no link", "", `"admitted":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/ask-about/config", nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: inviteCookie, Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			body := w.Body.String()
			if !strings.Contains(body, tc.wantAdmitted) {
				t.Errorf("config = %s, want %s", body, tc.wantAdmitted)
			}
			// Never cached: admission is per-visitor, and a shared cache
			// holding one visitor's answer would hand it to the next.
			if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
		})
	}
}

// With the gate off, everyone is admitted and no turn is attributed to a link.
func TestOpenSiteAdmitsEveryone(t *testing.T) {
	s := New(nil, nil, fstest.MapFS{}, Options{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	adm := s.admit(t.Context(), httptest.NewRequest("POST", "/ask-about/chat", nil))
	if !adm.OK {
		t.Errorf("open site refused a visitor: %s", adm.Reason)
	}
	if adm.InviteID != "" {
		t.Errorf("invite id = %q, want empty on an open site", adm.InviteID)
	}
}

// sonnet5 is the September 2026 rate card, so the arithmetic below is the
// arithmetic that will actually run.
var sonnet5 = config.Pricing{"claude-sonnet-5": {
	Input: 3.00, CacheRead: 0.30, CacheWrite: 3.75, Output: 15.00,
}}

// cappedServer is an open site with spend caps, so the caps are what is under
// test rather than the invite gate.
func cappedServer(t *testing.T, perSession, perDay float64) (http.Handler, store.Store) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipe := pipeline.New(stubProvider{}, nil, log)
	s := New(pipe, &corpus.Corpus{}, fstest.MapFS{}, Options{
		Store:   db,
		Pricing: sonnet5,
		Access: config.Access{
			MaxCostPerSession: perSession,
			MaxCostPerDay:     perDay,
		},
	}, log)

	h, err := s.Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h, db
}

// spend records a turn costing a known amount. One cache write of 27,123
// tokens is $0.1017 at September rates, which is the real shape of a first
// question.
func recordSpend(t *testing.T, db store.Store, session string, writes int64, at time.Time) {
	t.Helper()
	err := db.RecordTurn(t.Context(), &store.Turn{
		SessionID: session,
		Question:  "q",
		Answer:    "a",
		Usage:     store.Usage{Model: "claude-sonnet-5", CacheWriteTokens: writes},
		AskedAt:   at,
	})
	if err != nil {
		t.Fatalf("record turn: %v", err)
	}
}

func chatWithSession(session string) *http.Request {
	r := httptest.NewRequest("POST", "/ask-about/chat",
		strings.NewReader(`{"messages":[{"role":"user","text":"hi"}]}`))
	r.Header.Set("Content-Type", "application/json")
	if session != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	}
	return r
}

func TestSessionCap(t *testing.T) {
	// $1.50 per visit. 27,123 cache-write tokens is $0.1017, so ~15 of them
	// crosses it.
	h, db := cappedServer(t, 1.50, 0)
	const session = "abcdefghijklmnop"

	recordSpend(t, db, session, 27123*10, time.Now()) // $1.017 — under
	w := httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession(session))
	if w.Code == http.StatusForbidden {
		t.Fatalf("refused under the cap: %s", w.Body.String())
	}

	recordSpend(t, db, session, 27123*6, time.Now()) // now $1.627 — over
	w = httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession(session))
	if w.Code != http.StatusForbidden {
		t.Errorf("status over the cap = %d, want 403", w.Code)
	}
	// A cap is not a security boundary, so the message says what happened.
	if body := w.Body.String(); !strings.Contains(body, "limit for this visit") {
		t.Errorf("session cap message = %q", body)
	}

	// Another visitor is unaffected — the cap is per visit, not global.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession("qrstuvwxyz234567"))
	if w.Code == http.StatusForbidden {
		t.Errorf("one visitor's cap refused a different visitor: %s", w.Body.String())
	}
}

func TestDayCap(t *testing.T) {
	h, db := cappedServer(t, 0, 10.00)

	// Spread across different visitors: the day cap totals everyone.
	for i := range 50 {
		recordSpend(t, db, "visitor"+string(rune('a'+i%5))+"0000000000", 27123*2, time.Now())
	}
	// 50 turns x 54,246 tokens x $3.75/M = $10.17 — over.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession("brandnewvisitor1"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status over the day cap = %d, want 403", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "limit for today") {
		t.Errorf("day cap message = %q", body)
	}
}

// Yesterday's spend must not count against today, or the site would never
// reopen.
func TestDayCapResets(t *testing.T) {
	h, db := cappedServer(t, 0, 10.00)

	yesterday := startOfDay(time.Now()).Add(-2 * time.Hour)
	for range 50 {
		recordSpend(t, db, "yesterdayvisitor", 27123*2, yesterday)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession("todayvisitor0001"))
	if w.Code == http.StatusForbidden {
		t.Errorf("yesterday's spend closed today: %s", w.Body.String())
	}
}

// With no caps configured nothing is totalled and nobody is refused.
func TestNoCapsAdmitsEveryone(t *testing.T) {
	h, db := cappedServer(t, 0, 0)
	for range 100 {
		recordSpend(t, db, "bigspender00000a", 27123*10, time.Now())
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession("bigspender00000a"))
	if w.Code == http.StatusForbidden {
		t.Errorf("refused with no caps set: %s", w.Body.String())
	}
}

// A visitor with cookies off has no visit to total. They must not be refused
// by a per-visit cap that cannot apply to them.
func TestSessionCapSkippedWithoutCookie(t *testing.T) {
	h, db := cappedServer(t, 0.01, 0)
	recordSpend(t, db, "", 27123*100, time.Now())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, chatWithSession(""))
	if w.Code == http.StatusForbidden {
		t.Errorf("a cookieless visitor was refused by the per-visit cap: %s", w.Body.String())
	}
}

// The page reports a cap the same way it reports a bad link, so a visitor who
// has run out is told before typing.
func TestConfigReportsCap(t *testing.T) {
	h, db := cappedServer(t, 1.00, 0)
	const session = "cappedvisitor001"
	recordSpend(t, db, session, 27123*20, time.Now()) // $2.03 — over

	r := httptest.NewRequest("GET", "/ask-about/config", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := w.Body.String()
	if !strings.Contains(body, `"admitted":false`) {
		t.Errorf("config = %s, want admitted false", body)
	}
	if !strings.Contains(body, "limit for this visit") {
		t.Errorf("config carried no cap message: %s", body)
	}
}

// Everything ask-about serves lives under /ask-about. Nothing at all sits at the root, so
// it can share a domain without arguing over a common name.
//
// This is the regression test for adding a route and forgetting the prefix.
func TestNothingServedAtTheRoot(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(pipeline.New(stubProvider{}, nil, log), &corpus.Corpus{},
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>{{.Title}}</html>")}},
		Options{Store: db, Admin: config.Admin{Username: "daniel", Password: "pw"}}, log)
	h, err := s.Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Paths a co-hosted application might want for itself, including the two
	// ask-about used to keep. None may be answered.
	for _, path := range []string{
		"/", "/i/acme-corp/token",
		"/api/chat", "/api/config", "/api/health", "/api/avatar-photo",
		"/admin", "/admin/links", "/preview.jpg",
		"/app.js", "/style.css", "/avatar.js", "/backgrounds.js",
	} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 — ask-about is still claiming it", path, w.Code)
		}
	}

	// Under the prefix, everything works.
	for _, tc := range []struct {
		path string
		want int
	}{
		{Base, http.StatusTemporaryRedirect}, // the mux sends it to Base + "/"
		{Base + "/", http.StatusOK},
		{Base + "/health", http.StatusOK},
		{Base + "/config", http.StatusOK},
		{Base + "/i/acme-corp/token", http.StatusOK},
		{Base + "/admin", http.StatusUnauthorized}, // present, and guarded
		{Base + "/nope.js", http.StatusNotFound},   // a real typo looks like one
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.path, w.Code, tc.want)
		}
	}
}

// Someone who finds the URL with no link is not the same as someone whose link
// stopped working. The first has done nothing wrong and holds no token, so
// there is nothing to give away by being useful to them — and it is the only
// thing the site ever says to whoever wanders in.
func TestNoLinkGetsItsOwnMessage(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(pipeline.New(stubProvider{}, nil, log), &corpus.Corpus{},
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>{{.Title}}</html>")}},
		Options{
			Store:   db,
			Subject: config.Subject{FirstName: "Dana", LastName: "Reed"},
			Access: config.Access{
				InviteOnly: true,
				Message:    "That link stopped working.",
				NoLink:     "Ask {{firstName}} for a link.",
			},
		}, log)
	h, err := s.Handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Arriving with nothing.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", Base+"/config", nil))
	body := w.Body.String()
	if !strings.Contains(body, "Ask Dana for a link.") {
		t.Errorf("no-link visitor got: %s", body)
	}
	if !strings.Contains(body, `"noLink":true`) {
		t.Errorf("the client cannot tell the two cases apart: %s", body)
	}
	if strings.Contains(body, "stopped working") {
		t.Error("a visitor with no link was told their link stopped working")
	}

	// Arriving with a link that resolves to nothing.
	r := httptest.NewRequest("GET", Base+"/config", nil)
	r.AddCookie(&http.Cookie{Name: inviteCookie, Value: "acme-corp/aaaaaaaaaaaaaaaa"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body = w.Body.String()
	if !strings.Contains(body, "That link stopped working.") {
		t.Errorf("bad-link visitor got: %s", body)
	}
	if strings.Contains(body, `"noLink":true`) {
		t.Error("a bad link was reported as no link")
	}
	// The refusal still must not say which of expired, revoked or unknown.
	for _, leak := range []string{"expired", "revoked", "unknown"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("the refusal leaked %q", leak)
		}
	}
}
