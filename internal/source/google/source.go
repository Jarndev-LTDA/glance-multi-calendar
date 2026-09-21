package google

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

// Scope is the only permission requested: read-only calendars.
const Scope = "https://www.googleapis.com/auth/calendar.readonly"

// Endpoint is Google's OAuth 2.0 endpoint (same values as
// golang.org/x/oauth2/google, without pulling the GCP metadata module).
var Endpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// Options configure the source. Zero values pick production defaults.
type Options struct {
	ClientID, ClientSecret, RedirectURL string
	Location                            *time.Location
	IgnoreCalendars                     []string

	BaseURL  string          // API root; tests use a fake server
	Endpoint oauth2.Endpoint // token endpoint; tests use a fake server
	HTTP     *http.Client
	Sleep    func(time.Duration)
	Now      func() time.Time
}

// Source is the Google Calendar backend for N accounts.
type Source struct {
	opt   Options
	store *Store
	oauth *oauth2.Config

	mu        sync.Mutex
	tokens    map[string]oauth2.TokenSource
	reconnect map[string]bool
	snap      *snapshot
}

// snapshot is the cached answer served between polls.
type snapshot struct {
	window   source.Window
	at       time.Time
	accounts []source.Account
	events   []source.Event
	warnings []string
}

// AccountStatus is what /connect shows per account.
type AccountStatus struct {
	ID             string
	Email          string
	ConnectedAt    time.Time
	NeedsReconnect bool
}

// New builds a Source over a credential store.
func New(store *Store, opt Options) *Source {
	if opt.Location == nil {
		opt.Location = time.UTC
	}
	if opt.BaseURL == "" {
		opt.BaseURL = DefaultBaseURL
	}
	if opt.Endpoint.TokenURL == "" {
		opt.Endpoint = Endpoint
	}
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if opt.Sleep == nil {
		opt.Sleep = time.Sleep
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Source{
		opt:   opt,
		store: store,
		oauth: &oauth2.Config{
			ClientID: opt.ClientID, ClientSecret: opt.ClientSecret, RedirectURL: opt.RedirectURL,
			Scopes: []string{Scope}, Endpoint: opt.Endpoint,
		},
		tokens:    map[string]oauth2.TokenSource{},
		reconnect: map[string]bool{},
	}
}

// Connected is the number of accounts with stored credentials.
func (s *Source) Connected() int { return len(s.store.IDs()) }

// Status lists every stored account for the /connect page.
func (s *Source) Status() []AccountStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AccountStatus
	for _, id := range s.store.IDs() {
		c, _ := s.store.Get(id)
		out = append(out, AccountStatus{ID: id, Email: c.Email, ConnectedAt: c.ConnectedAt, NeedsReconnect: s.reconnect[id]})
	}
	return out
}

// AuthCodeURL starts the consent flow. offline + consent guarantees a
// refresh token even when the account had already approved the app.
func (s *Source) AuthCodeURL(state string) string {
	return s.oauth.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// Exchange turns the callback code into a credential and discovers the
// account's e-mail from its primary calendar (no extra scope needed).
func (s *Source) Exchange(ctx context.Context, code string) (Credential, error) {
	ctx = s.httpCtx(ctx)
	tok, err := s.oauth.Exchange(ctx, code)
	if err != nil {
		return Credential{}, fmt.Errorf("exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		return Credential{}, errors.New("google did not return a refresh token; remove the app at myaccount.google.com/permissions and connect again")
	}
	a := &api{base: s.opt.BaseURL, http: oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))}
	cals, err := a.listCalendars(ctx)
	if err != nil {
		return Credential{}, fmt.Errorf("list calendars: %w", err)
	}
	email := ""
	for _, c := range cals {
		if c.Primary {
			email = c.ID
		}
	}
	return Credential{Email: email, RefreshToken: tok.RefreshToken, ConnectedAt: s.opt.Now()}, nil
}

// Connect stores a credential under an account id and invalidates the cache.
func (s *Source) Connect(id string, c Credential) error {
	if err := s.store.Put(id, c); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.tokens, id)
	delete(s.reconnect, id)
	s.snap = nil
	s.mu.Unlock()
	return nil
}

// Disconnect forgets an account's credential (does not revoke at Google).
func (s *Source) Disconnect(id string) error {
	if err := s.store.Delete(id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.tokens, id)
	delete(s.reconnect, id)
	s.snap = nil
	s.mu.Unlock()
	return nil
}

// Accounts returns the cached accounts, refreshing first if there is no cache.
func (s *Source) Accounts(ctx context.Context) ([]source.Account, error) {
	snap, err := s.snapshotFor(ctx, s.defaultWindow(7))
	if err != nil {
		return nil, err
	}
	return slices.Clone(snap.accounts), nil
}

// Fetch serves from the cache when the window fits, otherwise live.
func (s *Source) Fetch(ctx context.Context, w source.Window) ([]source.Event, error) {
	s.mu.Lock()
	snap := s.snap
	s.mu.Unlock()
	if snap != nil && !w.From.Before(snap.window.From) && !w.To.After(snap.window.To) {
		return filter(snap.events, w), nil
	}
	if snap == nil {
		var err error
		if snap, err = s.snapshotFor(ctx, s.defaultWindow(7)); err != nil {
			return nil, err
		}
		if !w.From.Before(snap.window.From) && !w.To.After(snap.window.To) {
			return filter(snap.events, w), nil
		}
	}
	live := s.fetchAll(ctx, w)
	return live.events, nil
}

// Warnings lists per-account/per-calendar failures of the last refresh.
func (s *Source) Warnings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap == nil {
		return nil
	}
	return slices.Clone(s.snap.warnings)
}

// Refresh polls every account for the window and replaces the cache.
func (s *Source) Refresh(ctx context.Context, w source.Window) {
	snap := s.fetchAll(ctx, w)
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

// Run polls forever: every interval, a window of yesterday..today+days+1,
// recomputed each tick so it rolls with the calendar.
func (s *Source) Run(ctx context.Context, interval time.Duration, days int) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if s.Connected() > 0 {
			s.Refresh(ctx, s.defaultWindow(days))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Source) defaultWindow(days int) source.Window {
	now := s.opt.Now().In(s.opt.Location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.opt.Location)
	return source.Window{From: today.AddDate(0, 0, -1), To: today.AddDate(0, 0, days+1)}
}

func (s *Source) snapshotFor(ctx context.Context, w source.Window) (*snapshot, error) {
	s.mu.Lock()
	snap := s.snap
	s.mu.Unlock()
	if snap != nil {
		return snap, nil
	}
	s.Refresh(ctx, w)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap, nil
}

func filter(evs []source.Event, w source.Window) []source.Event {
	var out []source.Event
	for _, e := range evs {
		if w.Overlaps(e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *Source) httpCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, s.opt.HTTP)
}

func (s *Source) tokenSource(ctx context.Context, id string) (oauth2.TokenSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts, ok := s.tokens[id]; ok {
		return ts, nil
	}
	c, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("account %q not connected", id)
	}
	// ReuseTokenSource keeps the access token between polls; the config's
	// source refreshes it with the stored refresh token when it expires.
	ts := oauth2.ReuseTokenSource(nil, s.oauth.TokenSource(s.httpCtx(context.Background()), &oauth2.Token{RefreshToken: c.RefreshToken}))
	s.tokens[id] = ts
	return ts, nil
}

func (s *Source) dropToken(id string) {
	s.mu.Lock()
	delete(s.tokens, id)
	s.mu.Unlock()
}

// errReconnect marks a credential that no longer works.
var errReconnect = errors.New("account needs to be reconnected")

// call runs fn against the account's API with the failure policy:
//   - invalid_grant on refresh, or a second 401 → errReconnect
//   - first 401 → drop the cached access token and retry once
//   - rate limit → exponential backoff, three tries
func (s *Source) call(ctx context.Context, id string, fn func(a *api) error) error {
	retried401 := false
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		ts, err := s.tokenSource(ctx, id)
		if err != nil {
			return err
		}
		a := &api{base: s.opt.BaseURL, http: oauth2.NewClient(s.httpCtx(ctx), ts)}
		err = fn(a)
		if err == nil {
			if tok, terr := ts.Token(); terr == nil && tok.RefreshToken != "" {
				if uerr := s.store.UpdateRefreshToken(id, tok.RefreshToken); uerr != nil {
					slog.Warn("google: persist rotated refresh token", "account", id, "err", uerr)
				}
			}
			return nil
		}
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			if re.ErrorCode == "invalid_grant" || re.Response != nil && re.Response.StatusCode == http.StatusBadRequest {
				return fmt.Errorf("%w: %v", errReconnect, re.ErrorCode)
			}
			return err
		}
		var ae *APIError
		if errors.As(err, &ae) {
			switch {
			case ae.IsUnauthorized() && !retried401:
				retried401 = true
				s.dropToken(id)
				continue
			case ae.IsUnauthorized():
				return fmt.Errorf("%w: token rejected twice", errReconnect)
			case ae.IsRateLimit() && attempt < 2:
				d := backoff + time.Duration(rand.Int64N(int64(backoff/2)))
				slog.Warn("google: rate limited, backing off", "account", id, "wait", d)
				s.opt.Sleep(d)
				backoff *= 2
				continue
			}
		}
		return err
	}
}

var palette = []string{"#7aa2f7", "#9ece6a", "#e0af68", "#f7768e", "#bb9af7", "#7dcfff", "#ff9e64"}

// fetchAll queries every connected account. One broken account never fails
// the others: it is returned with NeedsReconnect and a warning.
func (s *Source) fetchAll(ctx context.Context, w source.Window) *snapshot {
	snap := &snapshot{window: w, at: s.opt.Now()}
	seen := map[string]bool{} // iCalUID + start: the same invite in two accounts
	for i, id := range s.store.IDs() {
		cred, _ := s.store.Get(id)
		acct := source.Account{ID: id, Name: id, Color: palette[i%len(palette)]}

		var cals []calendarEntry
		err := s.call(ctx, id, func(a *api) (err error) { cals, err = a.listCalendars(ctx); return })
		if err != nil {
			s.noteFailure(snap, &acct, id, cred.Email, "calendar list", err)
			snap.accounts = append(snap.accounts, acct)
			continue
		}
		s.setReconnect(id, false)
		var skipped []string
		for _, c := range cals {
			switch {
			case slices.Contains(s.opt.IgnoreCalendars, c.ID):
				skipped = append(skipped, c.name()+" (ignored)")
				continue
			case c.Hidden || c.Deleted:
				skipped = append(skipped, c.name()+" (hidden)")
				continue
			case !c.visible():
				skipped = append(skipped, c.name()+" (unselected)")
				continue
			}
			acct.Calendars = append(acct.Calendars, source.Calendar{ID: c.ID, Name: c.name()})
			var items []eventEntry
			err := s.call(ctx, id, func(a *api) (err error) { items, err = a.listEvents(ctx, c.ID, w.From, w.To); return })
			if err != nil {
				if errors.Is(err, errReconnect) {
					s.noteFailure(snap, &acct, id, cred.Email, "events of "+c.name(), err)
					break
				}
				snap.warnings = append(snap.warnings, fmt.Sprintf("%s / %s: %v", id, c.name(), err))
				slog.Warn("google: calendar failed", "account", id, "calendar", c.name(), "err", err)
				continue
			}
			for _, it := range items {
				ev, ok := s.convert(it, id, c.ID)
				if !ok {
					continue
				}
				key := it.ICalUID + "|" + ev.Start.UTC().Format(time.RFC3339)
				if it.ICalUID != "" && seen[key] {
					continue
				}
				seen[key] = true
				if w.Overlaps(ev) {
					snap.events = append(snap.events, ev)
				}
			}
		}
		// One line per account so the first real connection shows which
		// calendars were read and why the others were not.
		slog.Info("google: calendars discovered", "account", id, "total", len(cals), "shown", len(acct.Calendars), "skipped", skipped)
		snap.accounts = append(snap.accounts, acct)
	}
	slices.SortStableFunc(snap.events, func(a, b source.Event) int { return a.Start.Compare(b.Start) })
	return snap
}

func (s *Source) noteFailure(snap *snapshot, acct *source.Account, id, email, what string, err error) {
	if errors.Is(err, errReconnect) {
		acct.NeedsReconnect = true
		s.setReconnect(id, true)
		snap.warnings = append(snap.warnings, fmt.Sprintf("%s (%s): needs reconnect", id, email))
		slog.Warn("google: account needs reconnect", "account", id, "err", err)
		return
	}
	snap.warnings = append(snap.warnings, fmt.Sprintf("%s: %s: %v", id, what, err))
	slog.Warn("google: account failed", "account", id, "what", what, "err", err)
}

func (s *Source) setReconnect(id string, v bool) {
	s.mu.Lock()
	s.reconnect[id] = v
	s.mu.Unlock()
}

// convert maps a Google event to the shared model. Cancelled instances,
// declined invitations and working-location markers are dropped.
func (s *Source) convert(it eventEntry, account, calendar string) (source.Event, bool) {
	if it.Status == "cancelled" || it.declined() || it.EventType == "workingLocation" {
		return source.Event{}, false
	}
	start, allDay, err := parseEventTime(it.Start, s.opt.Location)
	if err != nil {
		slog.Warn("google: bad start", "event", it.ID, "err", err)
		return source.Event{}, false
	}
	end, _, err := parseEventTime(it.End, s.opt.Location)
	if err != nil {
		slog.Warn("google: bad end", "event", it.ID, "err", err)
		return source.Event{}, false
	}
	if !end.After(start) {
		end = start.Add(time.Minute)
	}
	return source.Event{
		ID: account + ":" + it.ID, Account: account, Calendar: calendar,
		Title: titleOf(it), Start: start, End: end, AllDay: allDay,
		Location: it.Location, URL: it.HTMLLink, Description: it.Description,
	}, true
}
