package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/optionalsoftware/ask-about/config"
	"github.com/optionalsoftware/ask-about/corpus"
	"github.com/optionalsoftware/ask-about/server"
	"github.com/optionalsoftware/ask-about/store"
)

// minimal is a config that builds with nothing on disk: dummy key, storage
// in a temp dir, admin on so the admin seams can be exercised.
func minimal(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := `
[subject]
firstName = "Dana"
lastName  = "Reed"
[llm]
vendor  = "anthropic"
model   = "m"
api_key = "dummy"
[storage]
path = "` + filepath.Join(dir, "t.db") + `"
[admin]
username = "admin"
password = "pw"
`
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func base(t *testing.T) Options {
	t.Helper()
	return Options{
		ConfigPath:     minimal(t),
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Web:            fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>{{.Title}}</html>")}},
		SampleDocument: "# Dana Reed\nWorked places.",
		ProductSample:  "# Widget\nA thing.",
		CompanySample:  "# Acme\nA firm.",
		PersonPersona:  "You answer about {{fullName}}.",
		ProductPersona: "You answer about the product {{fullName}}.",
	}
}

func get(h http.Handler, path string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if auth {
		r.SetBasicAuth("admin", "pw")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The free binary is Build with no seams filled in. It has to come up and
// serve the page, the health check, and the admin pages behind Basic auth.
func TestBuildFreeEdition(t *testing.T) {
	a, err := Build(base(t))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Store.Close()

	if w := get(a.Handler, "/ask-about/health", false); w.Code != http.StatusOK {
		t.Errorf("health = %d", w.Code)
	}
	if w := get(a.Handler, "/ask-about/", false); w.Code != http.StatusOK {
		t.Errorf("page = %d", w.Code)
	}
	if w := get(a.Handler, "/ask-about/admin", false); w.Code != http.StatusUnauthorized {
		t.Errorf("admin without credentials = %d, want 401", w.Code)
	}
	body := get(a.Handler, "/ask-about/admin", true).Body.String()
	for _, want := range []string{"Links", "Your Document"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin nav lacks %q", want)
		}
	}
	if !strings.Contains(a.Corpus.System(), "You answer about Dana Reed") {
		t.Error("the persona was not rendered for the configured subject")
	}
}

// allow is an Authenticator that lets everyone in, marking the request so
// the test can see it ran.
type allow struct{}

func (allow) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Auth", "custom")
		next.ServeHTTP(w, r)
	})
}

// swapped is a Documents that serves whatever corpus it was last given.
type swapped struct{ c *corpus.Corpus }

func (s *swapped) Current() *corpus.Corpus { return s.c }

// Every seam, filled: a replacement authenticator, an extra admin page in
// the navigation with its own routes, and a document source the server asks
// on every question.
func TestBuildWithSeams(t *testing.T) {
	alt, err := corpus.Load("", "", "ALT PERSONA", "ALT DOCUMENT", corpus.Subject{First: "X", Full: "X"})
	if err != nil {
		t.Fatal(err)
	}
	docs := &swapped{c: alt}

	opts := base(t)
	opts.AdminAuth = allow{}
	opts.AdminPages = []server.AdminPage{{
		Title: "Hand-offs", Path: "/handoffs",
		Routes: map[string]http.Handler{
			"GET /handoffs": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "HANDOFF PAGE")
			}),
			"POST /handoffs/{id}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "resolved "+r.PathValue("id"))
			}),
		},
	}}
	opts.Documents = docs

	a, err := Build(opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Store.Close()

	// The custom authenticator replaced Basic auth entirely: no credentials,
	// still in, and its mark is on the response.
	w := get(a.Handler, "/ask-about/admin/handoffs", false)
	if w.Code != http.StatusOK || w.Body.String() != "HANDOFF PAGE" {
		t.Errorf("custom page = %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Auth") != "custom" {
		t.Error("the custom authenticator did not wrap the custom page")
	}
	if w := get(a.Handler, "/ask-about/admin", false); w.Code != http.StatusOK || w.Header().Get("X-Auth") != "custom" {
		t.Errorf("built-in page under the custom authenticator = %d, X-Auth=%q", w.Code, w.Header().Get("X-Auth"))
	}
	// The extra page is in the navigation of every admin page, after the
	// built-in ones.
	body := get(a.Handler, "/ask-about/admin", false).Body.String()
	if i, j := strings.Index(body, "Your Document"), strings.Index(body, "Hand-offs"); i < 0 || j < 0 || j < i {
		t.Errorf("navigation order wrong or entry missing: %d %d", i, j)
	}
	// A second route on the same page, with a path value.
	r := httptest.NewRequest("POST", "/ask-about/admin/handoffs/42", nil)
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, r)
	if rec.Body.String() != "resolved 42" {
		t.Errorf("POST route = %q", rec.Body.String())
	}
	// The document source is consulted, not the startup corpus.
	if got := a.Corpus.System(); strings.Contains(got, "ALT") {
		t.Error("the startup corpus should be the file/sample one")
	}
	// Swap what the source returns; the server must follow. (Exercised through
	// the seam directly — the chat endpoint needs a model to reach it.)
	docs.c, _ = corpus.Load("", "", "NEWER", "NEWER DOC", corpus.Subject{First: "Y", Full: "Y"})
	if !strings.Contains(opts.Documents.Current().System(), "NEWER DOC") {
		t.Error("Documents.Current did not follow the swap")
	}
}

// A caller may hand Build a config it loaded itself and a store it opened
// itself. Build uses both as given, and Close leaves the caller's store open.
func TestBuildWithSuppliedConfigAndStore(t *testing.T) {
	dir := t.TempDir()
	db, err := store.OpenSQLite(filepath.Join(dir, "own.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cfg := config.Default()
	cfg.Subject.FirstName, cfg.Subject.LastName = "Dana", "Reed"
	cfg.LLM.Model, cfg.LLM.APIKeyRaw = "m", "dummy"
	cfg.Storage.Path = "" // nothing to open: the store is supplied
	cfg.Admin.Username, cfg.Admin.Password = "admin", "pw"

	opts := base(t)
	opts.ConfigPath = "/no/such/file.toml" // must be ignored
	opts.Config = &cfg
	opts.Store = db

	a, err := Build(opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Store != db {
		t.Error("Build did not use the supplied store")
	}
	if a.Config.Subject.FullName() != "Dana Reed" {
		t.Error("Build did not use the supplied config")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Still open: the caller owns it.
	if _, err := db.CreateInvite(t.Context(), "Acme", "", "tok", "hash", nil); err != nil {
		t.Errorf("Close closed a store it did not open: %v", err)
	}
	// And the admin pages are up, on the supplied store.
	if w := get(a.Handler, "/ask-about/admin", true); w.Code != http.StatusOK {
		t.Errorf("admin on supplied store = %d", w.Code)
	}
}

// With ReplaceAdminPages the built-in admin is not mounted at all — no Links,
// no Your Document, no prompts — and a supplied authenticator is enough to
// mount the caller's pages without any [admin] credentials configured.
func TestReplaceAdminPages(t *testing.T) {
	opts := base(t)
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Admin.Username, cfg.Admin.Password = "", "" // no built-in auth configured
	opts.Config = &cfg
	opts.AdminAuth = allow{}
	opts.ReplaceAdminPages = true
	opts.AdminPages = []server.AdminPage{{
		Title: "Dashboard", Path: "",
		Routes: map[string]http.Handler{"GET ": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "PRO DASHBOARD")
		})},
	}}

	a, err := Build(opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Close()

	if w := get(a.Handler, "/ask-about/admin", false); w.Code != http.StatusOK || w.Body.String() != "PRO DASHBOARD" {
		t.Errorf("admin root = %d %q", w.Code, w.Body.String())
	}
	for _, path := range []string{"/ask-about/admin/document", "/ask-about/admin/document/ic", "/ask-about/admin/links/x"} {
		if w := get(a.Handler, path, false); w.Code != http.StatusNotFound {
			t.Errorf("built-in %s still mounted: %d", path, w.Code)
		}
	}
}

// The built-in sample follows the kind: a product or company site with no
// document does not answer about the sample person.
func TestSampleFollowsKind(t *testing.T) {
	for kind, want := range map[string]string{"person": "Dana Reed", "product": "Widget", "company": "Acme"} {
		opts := base(t)
		cfg, err := config.Load(opts.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Subject.Kind, cfg.Subject.Name = kind, "X"
		cfg.Corpus.Path = ""
		opts.Config = &cfg
		a, err := Build(opts)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !strings.Contains(a.Corpus.System(), want) {
			t.Errorf("%s: sample document lacks %q", kind, want)
		}
		a.Close()
	}
}
