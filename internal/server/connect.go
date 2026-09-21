package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/config"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/render"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/google"
)

var accountIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// oauthStates holds the pending consent flows: state → account id.
type oauthStates struct {
	mu      sync.Mutex
	pending map[string]pendingState
}

type pendingState struct {
	account string
	expires time.Time
}

func (o *oauthStates) add(account string, now time.Time) string {
	b := make([]byte, 24)
	rand.Read(b)
	state := hex.EncodeToString(b)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending == nil {
		o.pending = map[string]pendingState{}
	}
	for k, p := range o.pending {
		if now.After(p.expires) {
			delete(o.pending, k)
		}
	}
	o.pending[state] = pendingState{account: account, expires: now.Add(10 * time.Minute)}
	return state
}

func (o *oauthStates) take(state string, now time.Time) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.pending[state]
	delete(o.pending, state)
	if !ok || now.After(p.expires) {
		return "", false
	}
	return p.account, true
}

func (s *Server) connectView(r *http.Request) render.ConnectView {
	q := r.URL.Query()
	v := render.ConnectView{
		Title: s.labels.Title, Lang: s.cfg.Language, RedirectURL: s.cfg.OAuth.RedirectURL,
		Configured: s.google != nil,
	}
	if ok := q.Get("ok"); ok != "" {
		v.Notice = "Conta " + ok + " conectada."
	}
	if e := q.Get("err"); e != "" {
		v.Error = e
	}
	if s.google == nil {
		return v
	}
	status := map[string]google.AccountStatus{}
	for _, st := range s.google.Status() {
		status[st.ID] = st
	}
	seen := map[string]bool{}
	add := func(a config.Account) {
		if seen[a.ID] {
			return
		}
		seen[a.ID] = true
		row := render.ConnectAccount{ID: a.ID, Name: a.Name, Color: a.Color}
		if row.Name == "" {
			row.Name = a.ID
		}
		if row.Color == "" {
			row.Color = "#8a8d9c"
		}
		if st, ok := status[a.ID]; ok {
			row.Connected = true
			row.Email = st.Email
			row.ConnectedAt = st.ConnectedAt.In(s.cfg.Location).Format("02/01/2006 15:04")
			row.NeedsReconnect = st.NeedsReconnect
		}
		v.Accounts = append(v.Accounts, row)
	}
	for _, a := range s.cfg.Accounts {
		add(a)
	}
	for _, st := range s.google.Status() {
		add(config.Account{ID: st.ID})
	}
	return v
}

// connect renders the account-management page.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := render.Connect(&buf, s.connectView(r)); err != nil {
		slog.Error("connect: render", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

func (s *Server) redirectConnect(w http.ResponseWriter, r *http.Request, key, value string) {
	http.Redirect(w, r, "/connect?"+url.Values{key: {value}}.Encode(), http.StatusSeeOther)
}

// oauthStart sends the browser to Google's consent screen.
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		s.redirectConnect(w, r, "err", "credenciais do Google não configuradas")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("account"))
	if !accountIDRe.MatchString(id) {
		s.redirectConnect(w, r, "err", "id de conta inválido: use letras minúsculas, dígitos, - ou _")
		return
	}
	state := s.states.add(id, s.now())
	http.Redirect(w, r, s.google.AuthCodeURL(state), http.StatusFound)
}

// oauthCallback exchanges the code and stores the credential.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		http.Error(w, "google not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	account, ok := s.states.take(q.Get("state"), s.now())
	if !ok {
		http.Error(w, "invalid or expired OAuth state; start again from /connect", http.StatusBadRequest)
		return
	}
	if e := q.Get("error"); e != "" {
		s.redirectConnect(w, r, "err", "Google recusou: "+e)
		return
	}
	cred, err := s.google.Exchange(r.Context(), q.Get("code"))
	if err != nil {
		slog.Warn("oauth: exchange failed", "account", account, "err", err)
		s.redirectConnect(w, r, "err", "falha ao trocar o código: "+err.Error())
		return
	}
	if err := s.google.Connect(account, cred); err != nil {
		slog.Error("oauth: store credential", "account", account, "err", err)
		s.redirectConnect(w, r, "err", "não consegui gravar tokens.json: "+err.Error())
		return
	}
	slog.Info("oauth: account connected", "account", account, "email", cred.Email)
	s.redirectConnect(w, r, "ok", account)
}

// disconnect forgets an account's credential.
func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		http.Error(w, "google not configured", http.StatusNotFound)
		return
	}
	id := r.PathValue("id")
	found := false
	for _, st := range s.google.Status() {
		if st.ID == id {
			found = true
		}
	}
	if !found {
		http.Error(w, "account not connected", http.StatusNotFound)
		return
	}
	if err := s.google.Disconnect(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.redirectConnect(w, r, "err", "Conta "+id+" esquecida. Para revogar no Google: myaccount.google.com/permissions")
}

// requireToken protects the routes Glance fetches when GMC_AUTH_TOKEN is set.
// The OAuth pages are left open: a browser redirect cannot carry a header,
// and they are reachable only through the SSH tunnel by design.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
