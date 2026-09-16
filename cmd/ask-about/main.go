// Command ask-about serves a grounded chat bot over a single document.
//
// The binary is self-contained: the UI, the sample document, the personas
// and the interview prompts are compiled in. Flags and the config file
// override any of them from disk, so swapping the document or the model
// needs no rebuild.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	askabout "github.com/optionalsoftware/ask-about"
	"github.com/optionalsoftware/ask-about/app"
)

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
		log.Info("ask-about", "version", app.Version)
		return
	}

	a, err := app.Build(app.Options{
		ConfigPath:     *configPath,
		CorpusPath:     *corpusPath,
		Addr:           *addr,
		Dev:            *dev,
		Log:            log,
		Web:            askabout.Web(),
		SampleDocument: askabout.SampleDocument,
		ProductSample:  askabout.ProductSample,
		CompanySample:  askabout.CompanySample,
		PersonPersona:  askabout.PersonPersona,
		ProductPersona: askabout.ProductPersona,
		Prompts:        askabout.Prompts(),
	})
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}
