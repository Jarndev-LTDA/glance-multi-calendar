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
	"path/filepath"
	"syscall"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/server"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/google"
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
	cfg.LoadEnv()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		src source.Source
		g   *google.Source
	)
	demoSrc := demo.New(cfg.Location)
	if cfg.HasGoogle() {
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return fmt.Errorf("data_dir %s: %w", cfg.DataDir, err)
		}
		store, err := google.OpenStore(filepath.Join(cfg.DataDir, "tokens.json"))
		if err != nil {
			return err
		}
		g = google.New(store, google.Options{
			ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, RedirectURL: cfg.OAuth.RedirectURL,
			Location: cfg.Location, IgnoreCalendars: cfg.IgnoreCalendars,
		})
		go g.Run(ctx, cfg.Refresh, cfg.Window.Days)
		src = source.Fallback{Primary: g, Alt: demoSrc, UsePrimary: func() bool { return g.Connected() > 0 }}
		if n := g.Connected(); n == 0 {
			slog.Warn("google credentials set but no account connected yet: serving DEMO data; open /connect (through the SSH tunnel)")
		} else {
			slog.Info("google source ready", "accounts", n, "refresh", cfg.Refresh)
		}
	} else {
		slog.Warn("GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET not set: serving DEMO data")
		src = demoSrc
	}

	ag := &agenda.Service{Sources: []source.Source{src}, Location: cfg.Location}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, ag, nil, g),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Listen, "timezone", cfg.Timezone, "version", version, "auth_token", cfg.AuthToken != "")
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
