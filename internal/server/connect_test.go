package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/google"
)

// fakeGoogle answers the token exchange and the primary-calendar lookup.
func fakeGoogle(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "rt" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if r.Form.Get("code") != "good" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":"me@example.com","summary":"me","primary":true,"selected":true}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[]}`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newGoogleServer(t *testing.T) (http.Handler, *google.Source) {
	t.Helper()
	fake := fakeGoogle(t)
	cfg := config.Default()
	cfg.Timezone = "America/Bahia"
	cfg.Language = "pt-BR"
	cfg.GoogleClientID, cfg.GoogleClientSecret = "cid", "sec"
	cfg.Accounts = []config.Account{{ID: "pessoal", Name: "Pessoal", Color: "#7aa2f7"}}
	cfg.Validate()
	store, err := google.OpenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := google.New(store, google.Options{
		ClientID: "cid", ClientSecret: "sec", RedirectURL: cfg.OAuth.RedirectURL, Location: cfg.Location,
		BaseURL: fake.URL, Endpoint: oauth2.Endpoint{AuthURL: fake.URL + "/auth", TokenURL: fake.URL + "/token"},
		HTTP: fake.Client(),
	})
	now := func() time.Time { return time.Date(2026, 9, 21, 10, 38, 0, 0, cfg.Location) }
	src := source.Fallback{Primary: g, Alt: demo.New(cfg.Location), UsePrimary: func() bool { return g.Connected() > 0 }}
	ag := &agenda.Service{Sources: []source.Source{src}, Location: cfg.Location, Now: now}
	return New(cfg, ag, now, g), g
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func TestConnectWithoutCredentials(t *testing.T) {
	h := newTestServer(t)
	rr := do(h, "GET", "/connect")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "não configuradas") {
		t.Errorf("connect: %d %s", rr.Code, rr.Body.String()[:120])
	}
	if rr := do(h, "GET", "/oauth/start?account=pessoal"); rr.Code != 303 {
		t.Errorf("start without google: %d", rr.Code)
	}
	if rr := do(h, "GET", "/oauth/callback?state=x&code=y"); rr.Code != 404 {
		t.Errorf("callback without google: %d", rr.Code)
	}
}

func TestOAuthFlow(t *testing.T) {
	h, g := newGoogleServer(t)

	// demo while nothing is connected
	rr := do(h, "GET", "/events.json")
	var body struct {
		Demo   bool  `json:"demo"`
		Events []any `json:"events"`
	}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if !body.Demo || len(body.Events) == 0 {
		t.Fatalf("expected demo data before connecting: demo=%v events=%d", body.Demo, len(body.Events))
	}

	// connect page lists the configured account as not connected
	rr = do(h, "GET", "/connect")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "não conectada") || !strings.Contains(rr.Body.String(), `value="pessoal"`) {
		t.Fatalf("connect page: %d", rr.Code)
	}

	// start → redirect to google with a state bound to the account
	rr = do(h, "GET", "/oauth/start?account=pessoal")
	if rr.Code != 302 {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	loc, _ := url.Parse(rr.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" || loc.Query().Get("access_type") != "offline" || loc.Query().Get("prompt") != "consent" || loc.Query().Get("redirect_uri") != "http://localhost:8089/oauth/callback" {
		t.Fatalf("auth url: %s", loc)
	}
	if rr := do(h, "GET", "/oauth/start?account=Bad%20Id"); rr.Code != 303 || !strings.Contains(rr.Header().Get("Location"), "err=") {
		t.Errorf("bad id: %d %s", rr.Code, rr.Header().Get("Location"))
	}

	// callback with a bogus state is rejected; the real one connects
	if rr := do(h, "GET", "/oauth/callback?state=nope&code=good"); rr.Code != 400 {
		t.Errorf("bogus state: %d", rr.Code)
	}
	rr = do(h, "GET", "/oauth/callback?state="+state+"&code=good")
	if rr.Code != 303 || rr.Header().Get("Location") != "/connect?ok=pessoal" {
		t.Fatalf("callback: %d %s %s", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}
	if rr := do(h, "GET", "/oauth/callback?state="+state+"&code=good"); rr.Code != 400 {
		t.Errorf("state must be single-use: %d", rr.Code)
	}
	if g.Connected() != 1 {
		t.Fatalf("connected = %d", g.Connected())
	}
	rr = do(h, "GET", "/connect?ok=pessoal")
	if !strings.Contains(rr.Body.String(), "me@example.com") || !strings.Contains(rr.Body.String(), "Conta pessoal conectada") {
		t.Errorf("connect page after connecting lacks e-mail/notice")
	}

	// now the real source is served (empty calendar → no events, not demo)
	rr = do(h, "GET", "/events.json")
	body.Demo, body.Events = false, nil
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Demo || len(body.Events) != 0 {
		t.Errorf("after connecting: demo=%v events=%d", body.Demo, len(body.Events))
	}
	if st := g.Status(); len(st) != 1 || st[0].NeedsReconnect {
		t.Errorf("status after connecting: %+v", st)
	}

	// a failed exchange reports and keeps the account list intact
	rr = do(h, "GET", "/oauth/start?account=trabalho")
	loc, _ = url.Parse(rr.Header().Get("Location"))
	rr = do(h, "GET", "/oauth/callback?state="+loc.Query().Get("state")+"&code=bad")
	if rr.Code != 303 || !strings.Contains(rr.Header().Get("Location"), "err=") {
		t.Errorf("bad code: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	rr = do(h, "GET", "/oauth/start?account=familia")
	loc, _ = url.Parse(rr.Header().Get("Location"))
	if rr := do(h, "GET", "/oauth/callback?state="+loc.Query().Get("state")+"&error=access_denied"); rr.Code != 303 || !strings.Contains(rr.Header().Get("Location"), "access_denied") {
		t.Errorf("denied: %d %s", rr.Code, rr.Header().Get("Location"))
	}

	// disconnect
	if rr := do(h, "POST", "/accounts/nope/disconnect"); rr.Code != 404 {
		t.Errorf("disconnect unknown: %d", rr.Code)
	}
	if rr := do(h, "POST", "/accounts/pessoal/disconnect"); rr.Code != 303 || g.Connected() != 0 {
		t.Errorf("disconnect: %d connected=%d", rr.Code, g.Connected())
	}
	rr = do(h, "GET", "/events.json")
	body.Demo, body.Events = false, nil
	json.Unmarshal(rr.Body.Bytes(), &body)
	if !body.Demo {
		t.Error("back to demo after disconnecting")
	}
}

func TestAuthToken(t *testing.T) {
	cfg := config.Default()
	cfg.Timezone = "America/Bahia"
	cfg.AuthToken = "s3cret"
	cfg.Validate()
	now := func() time.Time { return time.Date(2026, 9, 21, 10, 38, 0, 0, cfg.Location) }
	ag := &agenda.Service{Sources: []source.Source{demo.New(cfg.Location)}, Location: cfg.Location, Now: now}
	h := New(cfg, ag, now, nil)
	for _, p := range []string{"/events.json", "/widget/week"} {
		if rr := do(h, "GET", p); rr.Code != 401 {
			t.Errorf("%s without token: %d", p, rr.Code)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		h.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Errorf("%s with token: %d", p, rr.Code)
		}
	}
	for _, p := range []string{"/healthz", "/", "/connect"} {
		if rr := do(h, "GET", p); rr.Code != 200 {
			t.Errorf("%s must stay open: %d", p, rr.Code)
		}
	}
}
