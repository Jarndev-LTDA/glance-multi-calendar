// Command glance-multi-calendar serves a merged, multi-account calendar as
// JSON and as an HTML fragment for the Glance `extension` widget.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/server"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
)

var version = "dev"

func main() {
	configPath := flag.String("config", envOr("GMC_CONFIG", "config.yml"), "path to config.yml")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*configPath); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// A missing config.yml is fine: the service runs on defaults (demo mode).
	cfg, err := config.Load(configPath, !flagWasSet("config"))
	if err != nil {
		return err
	}

	// Later phases append the Google (and ICS) sources here once accounts
	// are connected. With nothing connected we serve the demo data so the
	// widget can be evaluated without any credentials.
	var sources []source.Source
	if len(sources) == 0 {
		slog.Warn("no account connected: serving DEMO data")
		sources = append(sources, demo.New(cfg.Location))
	}

	ag := &agenda.Service{Sources: sources, Location: cfg.Location}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, ag, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Listen, "timezone", cfg.Timezone, "version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set || os.Getenv("GMC_CONFIG") != ""
}
