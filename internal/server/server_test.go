package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 9, 21, 10, 38, 0, 0, cfg.Location) }
	ag := &agenda.Service{Sources: []source.Source{demo.New(cfg.Location)}, Location: cfg.Location, Now: now}
	return New(cfg, ag, now)
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
