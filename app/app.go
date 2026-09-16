// Package app assembles a running ask-about from its parts: config, document,
// model, store, pipeline and server. cmd/ask-about is a thin caller of it,
// and so is any other binary built on this module.
//
// Everything a caller might want to replace is a field on Options. Left nil,
// each falls back to what the free edition does.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/optionalsoftware/ask-about/config"
	"github.com/optionalsoftware/ask-about/corpus"
	"github.com/optionalsoftware/ask-about/llm"
	"github.com/optionalsoftware/ask-about/pipeline"
	"github.com/optionalsoftware/ask-about/server"
	"github.com/optionalsoftware/ask-about/store"
)

// Version is reported by the -version flag and in the startup log.
const Version = "0.2.0"

// Options is everything Build needs. The first group is what the free
// binary passes; the second is the seams.
type Options struct {
	ConfigPath string
	// CorpusPath and Addr override the config file when set.
	CorpusPath string
	Addr       string
	// Dev serves the web assets from ./web on disk rather than from Web, so
	// an edit shows on refresh.
	Dev bool
	Log *slog.Logger

	// The compiled-in files. The root package of this module provides them.
	Web            fs.FS
	SampleDocument string
	PersonPersona  string
	ProductPersona string
	Prompts        fs.FS

	// AdminAuth gates the admin pages. Nil uses the built-in HTTP Basic auth
	// configured under [admin].
	AdminAuth server.Authenticator
	// AdminPages are mounted behind AdminAuth alongside the built-in pages
	// and listed in the admin navigation.
	AdminPages []server.AdminPage
	// Observers are told about every completed turn, after the built-in
	// recorder when storage is on.
	Observers []pipeline.Observer
	// Documents supplies the document to answer from. Nil serves the one
	// loaded from the config's corpus path at startup, for the life of the
	// process.
	Documents server.Documents
}

// App is a built, not yet listening, ask-about.
type App struct {
	Config  config.Config
	Corpus  *corpus.Corpus
	Store   store.Store // nil when storage is off
	Handler http.Handler
	log     *slog.Logger
}

// Build does everything up to listening. It returns an error for anything
// that would otherwise fail at the first visitor, so a bad config is a
// startup failure with a message rather than a broken site.
func Build(opts Options) (*App, error) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	// A misspelt key parses fine and then does nothing, so the only symptom is
	// a feature that appears broken. Say so at startup instead.
	for _, key := range cfg.Unknown {
		log.Warn("unrecognised setting in the config file; it has no effect",
			"key", key, "file", opts.ConfigPath)
	}
	if opts.CorpusPath != "" {
		cfg.Corpus.Path = opts.CorpusPath
	}
	if opts.Addr != "" {
		cfg.Server.Addr = opts.Addr
	}

	// A corpus or persona path that does not exist is not an error — the copy
	// built into the binary stands in. That copy is compiled from the default
	// path, so the fallback is lossless there. Anywhere else it means a real
	// file was expected and the built-in one will serve instead, which looks
	// like a working site. Say so at startup.
	warnIfMissing(log, "corpus", cfg.Corpus.Path, config.Default().Corpus.Path,
		"answering from the built-in sample, a fictional person named Daniel Reyes")
	// The kind's own file is what the built-in persona was compiled from, so
	// naming it explicitly and not shipping it is the same as leaving it
	// empty: no warning. Any other path is a real file that is expected.
	warnIfMissing(log, "persona", cfg.Corpus.Persona, cfg.Subject.PersonaFile(),
		"using the persona built into the binary")

	persona := opts.PersonPersona
	if cfg.Subject.IsProduct() {
		persona = opts.ProductPersona
	}
	first, last, full := cfg.Subject.NameParts()
	c, err := corpus.Load(
		cfg.PersonaPath(), cfg.Corpus.Path, persona, opts.SampleDocument,
		corpus.Subject{First: first, Last: last, Full: full, Pronouns: cfg.Subject.PronounSet()},
	)
	if err != nil {
		return nil, err
	}

	provider, err := llm.New(cfg.LLM)
	if err != nil {
		return nil, err
	}

	// Persistence is optional: an empty storage.path runs the chat with
	// nothing recorded, which is what the tests and a throwaway run want.
	var db store.Store
	if cfg.Storage.Path != "" {
		db, err = store.OpenSQLite(cfg.Storage.Path)
		if err != nil {
			return nil, err
		}
		if err := checkPricing(cfg, log); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := cfg.CheckDeployable(); err != nil {
		if db != nil {
			db.Close()
		}
		return nil, err
	}

	// X-Forwarded-For is only believed from the configured proxy. Say so at
	// startup when nothing is configured and it would matter: the admin
	// allowlist and the lockout would otherwise see every client as the proxy.
	var trusted *netip.Prefix
	if p, ok, _ := cfg.Server.TrustedNet(); ok {
		trusted = &p
	} else if cfg.Admin.Enabled() {
		log.Warn("server.trusted_proxy is not set: X-Forwarded-For is ignored and " +
			"every request behind a proxy appears to come from the proxy")
	}

	web := opts.Web
	if opts.Dev {
		web = os.DirFS("web")
	}

	// No guard is implemented. The stage still runs — see pipeline.Guard.
	pipe := pipeline.New(provider, pipeline.PassThrough{}, log)
	if db != nil {
		pipe.Observe(store.NewRecorder(db, cfg.LLM.Vendor, log))
	}
	for _, o := range opts.Observers {
		pipe.Observe(o)
	}

	handler, err := server.New(pipe, c, web, server.Options{
		Features: server.Features{
			Avatar:     cfg.Avatar.Enabled,
			Background: cfg.Avatar.Background,
			Name:       cfg.Subject.FullName(),
			Tagline:    cfg.Subject.Tagline,
			Questions:  cfg.Subject.StarterQuestions(),
			Disclaimer: cfg.Subject.DisclaimerText(),
		},
		Subject:   cfg.Subject,
		PhotoPath: cfg.Avatar.PhotoPath(),
		Store:     db,
		Pricing:   cfg.Pricing,
		Admin:     cfg.Admin,
		Preview: server.Preview{
			Title:       cfg.PreviewTitle(),
			Description: cfg.PreviewDescription(),
			ImagePath:   cfg.PreviewImagePath(),
		},
		Access:       cfg.Access,
		Dev:          opts.Dev,
		Prompts:      opts.Prompts,
		TrustedProxy: trusted,
		AdminAuth:    opts.AdminAuth,
		AdminPages:   opts.AdminPages,
		Documents:    opts.Documents,
	}, log).Handler()
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, err
	}
	if cfg.Admin.Enabled() && db != nil {
		log.Info("admin enabled", "path", server.Base+"/admin", "restricted_to", cfg.Admin.IP)
	}

	return &App{Config: cfg, Corpus: c, Store: db, Handler: handler, log: log}, nil
}

// Run listens and serves until ctx is cancelled, then shuts down giving
// in-flight answers ten seconds to finish. It closes the store on return.
func (a *App) Run(ctx context.Context) error {
	if a.Store != nil {
		defer a.Store.Close()
	}
	cfg := a.Config

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: responses are long-lived SSE streams.
	}

	// Bind before logging success, so a port conflict reports the failure
	// instead of printing "listening" and then dying.
	ln, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", cfg.Server.Addr, err)
	}

	if a.Store != nil && cfg.Storage.RetainDays > 0 {
		go pruneLoop(ctx, a.Store, cfg.Storage.RetainDays, a.log)
	}

	a.log.Info("listening",
		"addr", ln.Addr().String(),
		"version", Version,
		"vendor", cfg.LLM.Vendor,
		"model", cfg.LLM.Model,
		"subject", cfg.Subject.FullName(),
		"kind", cfg.Subject.KindName(),
		"persona", cfg.PersonaPath(),
		"corpus_bytes", len(a.Corpus.Content),
	)

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		a.log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// pruneLoop applies the retention window: once at startup, then daily. A
// failed prune is logged and retried next round rather than stopping the
// server — old rows are a housekeeping problem, not an availability one.
func pruneLoop(ctx context.Context, db store.Store, days int, log *slog.Logger) {
	prune := func() {
		before := time.Now().AddDate(0, 0, -days)
		n, err := db.PruneTurns(ctx, before)
		switch {
		case err != nil && ctx.Err() != nil:
			// Shutting down mid-prune; the next start finishes the job.
		case err != nil:
			log.Error("could not prune old turns", "err", err)
		case n > 0:
			log.Info("pruned old turns", "count", n, "older_than_days", days)
		}
	}
	prune()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// warnIfMissing logs when a configured file is absent and the built-in copy
// will be used instead. The default path is exempt: the built-in copy was
// compiled from it, so nothing is lost there.
func warnIfMissing(log *slog.Logger, what, path, defaultPath, consequence string) {
	if path == "" || path == defaultPath {
		return
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		log.Warn(what+" file not found; "+consequence, "path", path)
	}
}

// checkPricing guards the rates the cost figures and the spend caps depend on.
//
// Without a cap a missing rate is only a cosmetic problem: the admin pages read
// zero. With a cap it is a silent failure of the thing you set it for — every
// turn prices at nothing, the total never reaches the limit, and you find out
// on the bill. So caps make this fatal rather than a warning.
func checkPricing(cfg config.Config, log *slog.Logger) error {
	rates, ok := cfg.Pricing.For(cfg.LLM.Model)
	if !ok {
		log.Warn("no [pricing] entry for the configured model; cost will read as zero",
			"model", cfg.LLM.Model)
		return nil
	}
	if rates.Zero() {
		log.Warn("[pricing] entry is all zeroes; fill it in from the vendor's pricing page",
			"model", cfg.LLM.Model)
		return nil
	}
	if cfg.Access.CapsEnabled() {
		log.Info("spend caps active",
			"per_session_usd", cfg.Access.MaxCostPerSession,
			"per_day_usd", cfg.Access.MaxCostPerDay,
			"model", cfg.LLM.Model)
	}
	return nil
}
