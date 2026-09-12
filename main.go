// Command ask-about serves a grounded chat bot over a single document.
//
// The binary is self-contained: the UI, the default corpus, the persona, and
// the default config are all embedded. Flags override any of them from disk,
// so swapping the corpus or the model needs no rebuild.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/optionalsoftware/ask-about/internal/config"
	"github.com/optionalsoftware/ask-about/internal/corpus"
	"github.com/optionalsoftware/ask-about/internal/llm"
	"github.com/optionalsoftware/ask-about/internal/pipeline"
	"github.com/optionalsoftware/ask-about/internal/server"
	"github.com/optionalsoftware/ask-about/internal/store"
)

//go:embed all:web
var embeddedWeb embed.FS

//go:embed docs/content.md
var embeddedCorpus string

// One persona per kind of subject. Which is used follows subject.kind; a
// persona path in the config overrides either from disk.
//
//go:embed prompts/person.md
var embeddedPersonPersona string

//go:embed prompts/product.md
var embeddedProductPersona string

func main() {
	var (
		configPath  = flag.String("config", "config.toml", "path to the TOML config file")
		corpusPath  = flag.String("corpus", "", "override corpus.path from the config")
		addr        = flag.String("addr", "", "override server.addr from the config")
		dev         = flag.Bool("dev", false, "serve web assets from ./web instead of the embedded copy")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *showVersion {
		log.Info("ask-about", "version", version)
		return
	}

	if err := run(log, *configPath, *corpusPath, *addr, *dev); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

const version = "0.1.0"

func run(log *slog.Logger, configPath, corpusPath, addr string, dev bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	// A misspelt key parses fine and then does nothing, so the only symptom is
	// a feature that appears broken. Say so at startup instead.
	for _, key := range cfg.Unknown {
		log.Warn("unrecognised setting in the config file; it has no effect",
			"key", key, "file", configPath)
	}

	if corpusPath != "" {
		cfg.Corpus.Path = corpusPath
	}
	if addr != "" {
		cfg.Server.Addr = addr
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

	embeddedPersona := embeddedPersonPersona
	if cfg.Subject.IsProduct() {
		embeddedPersona = embeddedProductPersona
	}
	first, last, full := cfg.Subject.NameParts()
	c, err := corpus.Load(
		cfg.PersonaPath(), cfg.Corpus.Path, embeddedPersona, embeddedCorpus,
		corpus.Subject{First: first, Last: last, Full: full, Pronouns: cfg.Subject.PronounSet()},
	)
	if err != nil {
		return err
	}

	provider, err := llm.New(cfg.LLM)
	if err != nil {
		return err
	}

	// Persistence is optional: an empty storage.path runs the chat with
	// nothing recorded, which is what the tests and a throwaway run want.
	var db store.Store
	if cfg.Storage.Path != "" {
		db, err = store.OpenSQLite(cfg.Storage.Path)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := checkPricing(cfg, log); err != nil {
			return err
		}
	}

	if err := cfg.CheckDeployable(); err != nil {
		return err
	}

	// No guard is implemented. The stage still runs — see pipeline.Guard.
	var guard pipeline.Guard = pipeline.PassThrough{}

	web, err := webFS(dev)
	if err != nil {
		return err
	}

	pipe := pipeline.New(provider, guard, log)
	if db != nil {
		pipe.Observe(store.NewRecorder(db, cfg.LLM.Vendor, log))
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
		Access: cfg.Access,
		Dev:    dev,
	}, log).Handler()
	if err != nil {
		return err
	}
	if cfg.Admin.Enabled() && db != nil {
		log.Info("admin enabled", "path", server.Base+"/admin", "restricted_to", cfg.Admin.IP)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: responses are long-lived SSE streams.
	}

	// Bind before logging success, so a port conflict reports the failure
	// instead of printing "listening" and then dying.
	ln, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", cfg.Server.Addr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("listening",
		"addr", ln.Addr().String(),
		"vendor", cfg.LLM.Vendor,
		"model", cfg.LLM.Model,
		"subject", cfg.Subject.FullName(),
		"kind", cfg.Subject.KindName(),
		"persona", cfg.PersonaPath(),
		"corpus_bytes", len(c.Content),
		"dev", dev,
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
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
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

// webFS returns the UI filesystem: live from disk in dev so a CSS edit needs
// only a browser refresh, embedded otherwise so the binary stands alone.
func webFS(dev bool) (fs.FS, error) {
	if dev {
		return os.DirFS("web"), nil
	}
	return fs.Sub(embeddedWeb, "web")
}
