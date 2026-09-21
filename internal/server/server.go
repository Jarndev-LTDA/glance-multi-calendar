// Package server exposes the HTTP contract: /healthz and /events.json for now;
// /widget/week, / and the OAuth routes arrive in later phases.
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

// Server holds the dependencies of the handlers.
type Server struct {
	cfg    config.Config
	agenda *agenda.Service
	now    func() time.Time
}

// New builds the router. now may be nil (defaults to time.Now).
func New(cfg config.Config, ag *agenda.Service, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	s := &Server{cfg: cfg, agenda: ag, now: now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /events.json", s.eventsJSON)
	return logRequests(mux)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *Server) eventsJSON(w http.ResponseWriter, r *http.Request) {
	win, err := s.window(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := s.agenda.Collect(r.Context(), win)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		slog.Error("events.json: encode", "err", err)
	}
}

// window parses ?days= and ?from=YYYY-MM-DD (in the configured zone).
// Defaults: today at midnight, config window.days.
func (s *Server) window(r *http.Request) (source.Window, error) {
	loc := s.cfg.Location
	q := r.URL.Query()

	days := s.cfg.Window.Days
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 31 {
			return source.Window{}, fmt.Errorf("days must be an integer 1..31, got %q", v)
		}
		days = n
	}

	now := s.now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if v := q.Get("from"); v != "" {
		d, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return source.Window{}, fmt.Errorf("from must be YYYY-MM-DD, got %q", v)
		}
		from = d
	}
	return source.Window{From: from, To: from.AddDate(0, 0, days)}, nil
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
