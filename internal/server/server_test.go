package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.Timezone = "America/Bahia"
	cfg.Language = "pt-BR"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 9, 21, 10, 38, 0, 0, cfg.Location) }
	ag := &agenda.Service{Sources: []source.Source{demo.New(cfg.Location)}, Location: cfg.Location, Now: now}
	return New(cfg, ag, now, nil)
}

func TestHealthz(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer(t).ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 || rr.Body.String() != "ok\n" {
		t.Fatalf("got %d %q", rr.Code, rr.Body.String())
	}
}

func TestEventsJSONContract(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer(t).ServeHTTP(rr, httptest.NewRequest("GET", "/events.json", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	var body struct {
		GeneratedAt string `json:"generated_at"`
		Timezone    string `json:"timezone"`
		Accounts    []struct {
			ID, Name, Color string
			Calendars       []struct{ ID, Name string }
		} `json:"accounts"`
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, rr.Body.String())
	}
	if body.Timezone != "America/Bahia" || body.GeneratedAt == "" {
		t.Errorf("header fields: %+v", body)
	}
	if len(body.Accounts) != 3 || body.Accounts[0].Color == "" || len(body.Accounts[0].Calendars) == 0 {
		t.Errorf("accounts: %+v", body.Accounts)
	}
	if len(body.Events) != 17 {
		t.Fatalf("events = %d, want 17", len(body.Events))
	}
	for _, key := range []string{"id", "account", "calendar", "title", "start", "end", "all_day"} {
		if _, ok := body.Events[0][key]; !ok {
			t.Errorf("event lacks %q: %v", key, body.Events[0])
		}
	}
	// Times must carry the configured offset, not Z.
	if s, _ := body.Events[0]["start"].(string); len(s) < 6 || s[len(s)-6:] != "-03:00" {
		t.Errorf("start %q not in America/Bahia", body.Events[0]["start"])
	}
}

func TestEventsJSONParams(t *testing.T) {
	h := newTestServer(t)
	get := func(q string) (*httptest.ResponseRecorder, int) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/events.json"+q, nil))
		var body struct {
			Events []any  `json:"events"`
			From   string `json:"from"`
		}
		json.Unmarshal(rr.Body.Bytes(), &body)
		return rr, len(body.Events)
	}
	if rr, n := get("?days=3"); rr.Code != 200 || n != 9 {
		t.Errorf("days=3: %d events, status %d", n, rr.Code)
	}
	if rr, n := get("?from=2027-01-04&days=1"); rr.Code != 200 || n != 4 {
		t.Errorf("from+days=1: %d events, status %d", n, rr.Code)
	}
	for _, bad := range []string{"?days=0", "?days=99", "?days=x", "?from=21/09/2026"} {
		if rr, _ := get(bad); rr.Code != 400 {
			t.Errorf("%s: status %d, want 400", bad, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/events.json", nil))
	if rr.Code != 405 {
		t.Errorf("POST: status %d, want 405", rr.Code)
	}
}

func TestWidgetWeekHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer(t).ServeHTTP(rr, httptest.NewRequest("GET", "/widget/week", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	// Exact header names and values from glance docs/extensions.md.
	if got := rr.Header().Get("Widget-Title"); got != "Agenda" {
		t.Errorf("Widget-Title = %q", got)
	}
	if got := rr.Header().Get("Widget-Content-Type"); got != "html" {
		t.Errorf("Widget-Content-Type = %q", got)
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, "<style>") || strings.Contains(body, "<html") {
		t.Error("fragment must be style + div only")
	}
	// "+" is escaped to &#43; by html/template, so check a plain title.
	if !strings.Contains(body, "Dentista") || !strings.Contains(body, `class="ag-now"`) {
		t.Error("demo content or now-line missing")
	}
}

func TestWidgetParamsAndFilters(t *testing.T) {
	h := newTestServer(t)
	get := func(q string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/widget/week"+q, nil))
		return rr
	}
	if rr := get("?accounts=familia"); rr.Code != 200 || strings.Contains(rr.Body.String(), "Dentista") || !strings.Contains(rr.Body.String(), "Churrasco") || strings.Contains(rr.Body.String(), "<b>Trabalho</b>") {
		t.Errorf("accounts filter: status %d, legend/events not filtered", rr.Code)
	}
	if rr := get("?start_hour=9&end_hour=18"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "--h0:8;--h1:22") {
		// floor 9..18 stretches to 8 (1:1 at 08:00) and 22 (Jantar ends 21:30)
		t.Errorf("hour override: status %d body lacks --h0:8;--h1:22", rr.Code)
	}
	if rr := get("?start_hour=9&end_hour=18&accounts=trabalho&days=1"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "--h0:9;--h1:18") {
		// Monday work-only: 09:00-10:40 and 14:00-16:00 fit the floor
		t.Errorf("hour floor kept: status %d body lacks --h0:9;--h1:18", rr.Code)
	}
	for _, bad := range []string{"?start_hour=25", "?end_hour=-1", "?start_hour=20&end_hour=8", "?days=40"} {
		if rr := get(bad); rr.Code != 400 {
			t.Errorf("%s: status %d, want 400", bad, rr.Code)
		}
	}
}

func TestPageThemes(t *testing.T) {
	h := newTestServer(t)
	for _, th := range []string{"", "?theme=dark", "?theme=light"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/"+th, nil))
		if rr.Code != 200 || !strings.HasPrefix(rr.Body.String(), "<!doctype html>") {
			t.Errorf("/%s: status %d", th, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/?theme=neon", nil))
	if rr.Code != 400 {
		t.Errorf("bad theme: status %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/nope", nil))
	if rr.Code != 404 {
		t.Errorf("/nope: status %d", rr.Code)
	}
}

func TestConfigOverridesAccountLabels(t *testing.T) {
	cfg := config.Default()
	cfg.Timezone = "America/Bahia"
	cfg.Language = "en"
	cfg.Title = "Family"
	cfg.Accounts = []config.Account{{ID: "familia", Name: "Casa", Color: "#ff00ff"}}
	cfg.Validate()
	now := func() time.Time { return time.Date(2026, 9, 21, 10, 38, 0, 0, cfg.Location) }
	ag := &agenda.Service{Sources: []source.Source{demo.New(cfg.Location)}, Location: cfg.Location, Now: now}
	h := New(cfg, ag, now, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/widget/week", nil))
	b := rr.Body.String()
	if rr.Header().Get("Widget-Title") != "Family" {
		t.Errorf("title = %q", rr.Header().Get("Widget-Title"))
	}
	if !strings.Contains(b, "<b>Casa</b>") || !strings.Contains(b, "--c:#ff00ff") || strings.Contains(b, "Família") {
		t.Error("config name/colour override not applied")
	}
	if !strings.Contains(b, `class="ag-dow">Mon<`) || !strings.Contains(b, "updated 10:38") {
		t.Error("english labels not applied")
	}
}
