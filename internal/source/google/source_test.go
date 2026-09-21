package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

var bahia = func() *time.Location { l, _ := time.LoadLocation("America/Bahia"); return l }()

// fake is a stand-in for accounts.google.com + the Calendar API.
type fake struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []string          // "METHOD path" log
	inject   map[string][]int  // path prefix → queued status codes to return first
	tokens   map[string]string // refresh → access
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, inject: map[string][]int{}, tokens: map[string]string{"rt-pessoal": "at-pessoal", "rt-work": "at-work"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.log(r)
		w.Header().Set("Content-Type", "application/json") // without it oauth2 parses the body as a form
		rt := r.Form.Get("refresh_token")
		if r.Form.Get("grant_type") == "authorization_code" {
			if r.Form.Get("code") == "good-code" {
				json.NewEncoder(w).Encode(map[string]any{"access_token": "at-pessoal", "refresh_token": "rt-new", "token_type": "Bearer", "expires_in": 3600})
				return
			}
			if r.Form.Get("code") == "no-refresh" {
				json.NewEncoder(w).Encode(map[string]any{"access_token": "at-pessoal", "token_type": "Bearer", "expires_in": 3600})
				return
			}
		}
		at, ok := f.tokens[rt]
		if !ok {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.log(r)
		if code := f.pop(r.URL.Path); code != 0 {
			w.WriteHeader(code)
			if code == 403 {
				f.file(w, "rate_limit.json")
			}
			return
		}
		at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if at != "at-pessoal" && at != "at-work" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/users/me/calendarList":
			if at == "at-work" {
				f.file(w, "calendarlist_work.json")
			} else {
				f.file(w, "calendarlist.json")
			}
		case strings.HasSuffix(r.URL.Path, "/events"):
			if r.URL.Query().Get("singleEvents") != "true" || r.URL.Query().Get("orderBy") != "startTime" {
				t.Errorf("events query lacks singleEvents/orderBy: %s", r.URL.RawQuery)
			}
			cal := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/calendars/"), "/events")
			switch cal {
			case "ana@example.com":
				f.file(w, "events_primary.json")
			case "abc123@group.calendar.google.com":
				if r.URL.Query().Get("pageToken") == "page2" {
					f.file(w, "events_projetos_page2.json")
				} else {
					f.file(w, "events_projetos.json")
				}
			case "ana@work.example":
				f.file(w, "events_work.json")
			default:
				w.Write([]byte(`{"items":[]}`))
			}
		default:
			w.WriteHeader(404)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) log(r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
}

func (f *fake) pop(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for prefix, codes := range f.inject {
		if strings.HasPrefix(path, prefix) && len(codes) > 0 {
			f.inject[prefix] = codes[1:]
			return codes[0]
		}
	}
	return 0
}

func (f *fake) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.Contains(r, substr) {
			n++
		}
	}
	return n
}

func (f *fake) file(w http.ResponseWriter, name string) {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func newSource(t *testing.T, f *fake, accounts map[string]string) (*Source, *[]time.Duration) {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	for id, rt := range accounts {
		st.Put(id, Credential{Email: id + "@example.com", RefreshToken: rt})
	}
	var sleeps []time.Duration
	s := New(st, Options{
		ClientID: "cid", ClientSecret: "sec", RedirectURL: "http://localhost:8089/oauth/callback",
		Location: bahia, IgnoreCalendars: []string{"pt.brazilian#holiday@group.v.calendar.google.com"},
		BaseURL:  f.srv.URL,
		Endpoint: oauth2.Endpoint{AuthURL: f.srv.URL + "/auth", TokenURL: f.srv.URL + "/token"},
		HTTP:     f.srv.Client(),
		Sleep:    func(d time.Duration) { sleeps = append(sleeps, d) },
		Now:      func() time.Time { return time.Date(2026, 9, 21, 10, 0, 0, 0, bahia) },
	})
	return s, &sleeps
}

var week = source.Window{From: time.Date(2026, 9, 21, 0, 0, 0, 0, bahia), To: time.Date(2026, 9, 28, 0, 0, 0, 0, bahia)}

func titles(evs []source.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Title)
	}
	return out
}

func find(evs []source.Event, title string) *source.Event {
	for i := range evs {
		if evs[i].Title == title {
			return &evs[i]
		}
	}
	return nil
}

func TestCalendarDiscoveryAndConversion(t *testing.T) {
	f := newFake(t)
	s, _ := newSource(t, f, map[string]string{"pessoal": "rt-pessoal"})
	accts, err := s.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// case 1: 5 entries → primary + Projetos (selected:false, hidden and ignored holidays skipped)
	if len(accts) != 1 || len(accts[0].Calendars) != 2 || accts[0].Calendars[0].Name != "Pessoal" || accts[0].Calendars[1].Name != "Projetos" {
		t.Fatalf("accounts = %+v", accts)
	}
	evs, err := s.Fetch(context.Background(), week)
	if err != nil {
		t.Fatal(err)
	}
	got := titles(evs)
	// case 2: +01:00 → 10:00 in Bahia
	if e := find(evs, "Reunião em Lisboa"); e == nil || e.Start.Hour() != 10 || e.Start.Location() != bahia || e.URL == "" || e.Location != "Lisboa" {
		t.Errorf("offset conversion: %+v", e)
	}
	// case 3: all-day with exclusive end
	if e := find(evs, "Aniversário"); e == nil || !e.AllDay || e.Start.Day() != 24 || e.End.Day() != 25 || e.End.Hour() != 0 {
		t.Errorf("all-day: %+v", e)
	}
	// case 4: multi-day crossing the window end is kept whole
	if e := find(evs, "Congresso"); e == nil || e.Start.Day() != 26 || e.End.Day() != 30 {
		t.Errorf("multi-day: %+v", e)
	}
	// case 5: moved instance kept, cancelled dropped; declined and workingLocation dropped; untitled placeholder
	if find(evs, "Daily (movida)") == nil || find(evs, "Reunião recusada") != nil || find(evs, "Escritório") != nil || find(evs, untitled) == nil {
		t.Errorf("instance filtering wrong: %v", got)
	}
	if n := 0; true {
		for _, e := range evs {
			if e.Title == "Daily" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("Daily instances = %d, want 1 (21st only; 23rd cancelled)", n)
		}
	}
	// pagination
	if find(evs, "Sprint review") == nil || find(evs, "Retro") == nil {
		t.Errorf("pagination lost events: %v", got)
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Start.Before(evs[i-1].Start) {
			t.Fatal("not sorted")
		}
	}
	if len(s.Warnings()) != 0 {
		t.Errorf("unexpected warnings: %v", s.Warnings())
	}
}

func TestDedupAcrossAccounts(t *testing.T) {
	f := newFake(t)
	s, _ := newSource(t, f, map[string]string{"pessoal": "rt-pessoal", "trabalho": "rt-work"})
	evs, err := s.Fetch(context.Background(), week)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, e := range evs {
		n[e.Title]++
	}
	// case 6: same invite in both accounts → once; recurring series shares iCalUID
	// but instances have different starts → 21st (both, deduped), 22nd moved, 24th work-only
	if n["Planejamento Q4"] != 1 || n["Daily"] != 2 || n["Daily (movida)"] != 1 || n["Só no trabalho"] != 1 {
		t.Errorf("dedup counts: %v", n)
	}
	if e := find(evs, "Planejamento Q4"); e == nil || e.Account != "pessoal" {
		t.Errorf("first account should win: %+v", e)
	}
	accts, _ := s.Accounts(context.Background())
	if len(accts) != 2 || accts[0].Color == accts[1].Color {
		t.Errorf("accounts: %+v", accts)
	}
}

func TestBrokenAccountIsIsolated(t *testing.T) {
	f := newFake(t)
	s, _ := newSource(t, f, map[string]string{"pessoal": "rt-pessoal", "quebrada": "rt-revoked"})
	accts, err := s.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// case 7: invalid_grant → flagged, the other account still delivers
	if len(accts) != 2 {
		t.Fatalf("accounts = %+v", accts)
	}
	for _, a := range accts {
		if (a.ID == "quebrada") != a.NeedsReconnect {
			t.Errorf("reconnect flag wrong: %+v", a)
		}
	}
	evs, _ := s.Fetch(context.Background(), week)
	if len(evs) == 0 {
		t.Fatal("healthy account produced no events")
	}
	if w := s.Warnings(); len(w) != 1 || !strings.Contains(w[0], "needs reconnect") {
		t.Errorf("warnings = %v", w)
	}
	st := s.Status()
	if len(st) != 2 || !st[1].NeedsReconnect || st[0].NeedsReconnect {
		t.Errorf("status = %+v", st)
	}
	// reconnecting clears the flag and the cache
	if err := s.Connect("quebrada", Credential{Email: "q@example.com", RefreshToken: "rt-work"}); err != nil {
		t.Fatal(err)
	}
	accts, _ = s.Accounts(context.Background())
	for _, a := range accts {
		if a.NeedsReconnect {
			t.Errorf("still flagged after reconnect: %+v", a)
		}
	}
}

func TestUnauthorizedRetriesOnce(t *testing.T) {
	f := newFake(t)
	f.inject["/users/me/calendarList"] = []int{401}
	s, _ := newSource(t, f, map[string]string{"pessoal": "rt-pessoal"})
	accts, _ := s.Accounts(context.Background())
	if accts[0].NeedsReconnect || len(accts[0].Calendars) != 2 {
		t.Errorf("single 401 should be retried with a fresh token: %+v", accts[0])
	}
	if f.count("/token") != 2 {
		t.Errorf("token endpoint calls = %d, want 2", f.count("/token"))
	}
	// twice in a row → reconnect
	f2 := newFake(t)
	f2.inject["/users/me/calendarList"] = []int{401, 401}
	s2, _ := newSource(t, f2, map[string]string{"pessoal": "rt-pessoal"})
	accts, _ = s2.Accounts(context.Background())
	if !accts[0].NeedsReconnect {
		t.Errorf("double 401 should flag reconnect: %+v", accts[0])
	}
}

func TestRateLimitBackoff(t *testing.T) {
	f := newFake(t)
	f.inject["/users/me/calendarList"] = []int{403, 403}
	s, sleeps := newSource(t, f, map[string]string{"pessoal": "rt-pessoal"})
	accts, _ := s.Accounts(context.Background())
	// case 8: two 403s then success; backoff grows; no warning
	if len(accts[0].Calendars) != 2 || len(*sleeps) != 2 || (*sleeps)[1] < (*sleeps)[0] || len(s.Warnings()) != 0 {
		t.Errorf("backoff: calendars=%d sleeps=%v warnings=%v", len(accts[0].Calendars), *sleeps, s.Warnings())
	}
	// persistent limit → warning, account kept, no events, no reconnect flag
	f2 := newFake(t)
	f2.inject["/users/me/calendarList"] = []int{403, 403, 403, 403}
	s2, _ := newSource(t, f2, map[string]string{"pessoal": "rt-pessoal"})
	accts, _ = s2.Accounts(context.Background())
	if len(accts) != 1 || accts[0].NeedsReconnect || len(s2.Warnings()) != 1 || !strings.Contains(s2.Warnings()[0], "rateLimitExceeded") {
		t.Errorf("persistent limit: %+v warnings=%v", accts, s2.Warnings())
	}
}

func TestCacheServesWithoutRequests(t *testing.T) {
	f := newFake(t)
	s, _ := newSource(t, f, map[string]string{"pessoal": "rt-pessoal"})
	s.Refresh(context.Background(), s.defaultWindow(7)) // yesterday .. today+8
	before := len(f.requests)
	if _, err := s.Fetch(context.Background(), week); err != nil {
		t.Fatal(err)
	}
	s.Accounts(context.Background())
	if len(f.requests) != before {
		t.Errorf("cached window should not hit the API: %v", f.requests[before:])
	}
	// a window outside the cache goes live
	far := source.Window{From: week.From.AddDate(0, 1, 0), To: week.To.AddDate(0, 1, 0)}
	s.Fetch(context.Background(), far)
	if len(f.requests) == before {
		t.Error("window outside the cache should fetch live")
	}
}

func TestExchange(t *testing.T) {
	f := newFake(t)
	s, _ := newSource(t, f, nil)
	if u := s.AuthCodeURL("st4te"); !strings.Contains(u, "access_type=offline") || !strings.Contains(u, "prompt=consent") || !strings.Contains(u, "state=st4te") || !strings.Contains(u, "calendar.readonly") {
		t.Errorf("auth url = %s", u)
	}
	c, err := s.Exchange(context.Background(), "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshToken != "rt-new" || c.Email != "ana@example.com" || c.ConnectedAt.IsZero() {
		t.Errorf("credential = %+v", c)
	}
	if _, err := s.Exchange(context.Background(), "no-refresh"); err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Errorf("missing refresh token must be a clear error, got %v", err)
	}
	if s.Connected() != 0 {
		t.Error("exchange must not store anything by itself")
	}
	if err := s.Connect("pessoal", c); err != nil || s.Connected() != 1 {
		t.Fatalf("connect: %v", err)
	}
	if err := s.Disconnect("pessoal"); err != nil || s.Connected() != 0 {
		t.Fatalf("disconnect: %v", err)
	}
}
