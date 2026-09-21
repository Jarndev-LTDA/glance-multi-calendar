// Package server exposes the HTTP contract: /healthz, /events.json,
// /widget/week (the fragment Glance consumes) and / (standalone page).
// The OAuth routes arrive with the Google source.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/render"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/google"
)

// Server holds the dependencies of the handlers.
type Server struct {
	cfg    config.Config
	agenda *agenda.Service
	now    func() time.Time
	labels agenda.Labels
	google *google.Source // nil when no credentials are configured
	states oauthStates
}

// New builds the router. now may be nil (defaults to time.Now); g may be
// nil (demo only, /connect explains how to configure credentials).
func New(cfg config.Config, ag *agenda.Service, now func() time.Time, g *google.Source) http.Handler {
	if now == nil {
		now = time.Now
	}
	lab := agenda.LabelsFor(cfg.Language)
	if cfg.Title != "" {
		lab.Title = cfg.Title
	}
	s := &Server{cfg: cfg, agenda: ag, now: now, labels: lab, google: g}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /events.json", requireToken(cfg.AuthToken, http.HandlerFunc(s.eventsJSON)))
	mux.Handle("GET /widget/week", requireToken(cfg.AuthToken, http.HandlerFunc(s.widgetWeek)))
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /connect", s.connect)
	mux.HandleFunc("GET /oauth/start", s.oauthStart)
	mux.HandleFunc("GET /oauth/callback", s.oauthCallback)
	mux.HandleFunc("POST /accounts/{id}/disconnect", s.disconnect)
	return logRequests(mux)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *Server) eventsJSON(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := s.collect(r, q)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		slog.Error("events.json: encode", "err", err)
	}
}

// widgetWeek is what the Glance `extension` widget fetches.
func (s *Server) widgetWeek(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	view := s.view(r, q)
	var buf bytes.Buffer
	if err := render.Fragment(&buf, view); err != nil {
		slog.Error("widget: render", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Widget-Title", view.Title)
	h.Set("Widget-Content-Type", "html")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

// page is the standalone debug page: same grid inside a fake Glance card,
// with ?theme=dark|light.
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	theme := r.URL.Query().Get("theme")
	if theme == "" {
		theme = "dark"
	}
	if _, ok := render.Themes[theme]; !ok {
		http.Error(w, "theme must be one of "+strings.Join(render.ThemeNames(), ", "), http.StatusBadRequest)
		return
	}
	view := s.view(r, q)
	var buf bytes.Buffer
	if err := render.Page(&buf, view, s.cfg.Language, theme); err != nil {
		slog.Error("page: render", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

type query struct {
	win                source.Window
	startHour, endHour int
	accounts           []string // empty = all
}

// parseQuery reads days, from, start_hour, end_hour and accounts.
func (s *Server) parseQuery(r *http.Request) (query, error) {
	loc := s.cfg.Location
	v := r.URL.Query()
	q := query{startHour: s.cfg.Window.StartHour, endHour: s.cfg.Window.EndHour}

	days := s.cfg.Window.Days
	if raw := v.Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 31 {
			return q, fmt.Errorf("days must be an integer 1..31, got %q", raw)
		}
		days = n
	}
	now := s.now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if raw := v.Get("from"); raw != "" {
		d, err := time.ParseInLocation("2006-01-02", raw, loc)
		if err != nil {
			return q, fmt.Errorf("from must be YYYY-MM-DD, got %q", raw)
		}
		from = d
	}
	q.win = source.Window{From: from, To: from.AddDate(0, 0, days)}

	hour := func(name string, cur int) (int, error) {
		raw := v.Get(name)
		if raw == "" {
			return cur, nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < s.cfg.Window.MinHour || n > s.cfg.Window.MaxHour {
			return 0, fmt.Errorf("%s must be an integer %d..%d, got %q", name, s.cfg.Window.MinHour, s.cfg.Window.MaxHour, raw)
		}
		return n, nil
	}
	var err error
	if q.startHour, err = hour("start_hour", q.startHour); err != nil {
		return q, err
	}
	if q.endHour, err = hour("end_hour", q.endHour); err != nil {
		return q, err
	}
	if q.startHour >= q.endHour {
		return q, fmt.Errorf("start_hour (%d) must be before end_hour (%d)", q.startHour, q.endHour)
	}
	if raw := v.Get("accounts"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				q.accounts = append(q.accounts, id)
			}
		}
	}
	return q, nil
}

// collect runs the agenda, applies the config's labels/colours per account
// and the ?accounts= filter.
func (s *Server) collect(r *http.Request, q query) agenda.Result {
	res := s.agenda.Collect(r.Context(), q.win)
	override := map[string]config.Account{}
	for _, a := range s.cfg.Accounts {
		override[a.ID] = a
	}
	accounts := res.Accounts[:0]
	for _, a := range res.Accounts {
		if o, ok := override[a.ID]; ok {
			if o.Name != "" {
				a.Name = o.Name
			}
			if o.Color != "" {
				a.Color = o.Color
			}
		}
		if len(q.accounts) == 0 || slices.Contains(q.accounts, a.ID) {
			accounts = append(accounts, a)
		}
	}
	res.Accounts = accounts
	if len(q.accounts) > 0 {
		events := res.Events[:0]
		for _, e := range res.Events {
			if slices.Contains(q.accounts, e.Account) {
				events = append(events, e)
			}
		}
		res.Events = events
	}
	return res
}

func (s *Server) view(r *http.Request, q query) agenda.View {
	return agenda.Layout(s.collect(r, q), agenda.LayoutOptions{
		StartHour: q.startHour, EndHour: q.endHour,
		MinHour: s.cfg.Window.MinHour, MaxHour: s.cfg.Window.MaxHour,
		Now: s.now(), Labels: s.labels,
	})
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
