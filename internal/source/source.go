// Package source defines the data model shared by every calendar backend
// (Google API, ICS, demo) and the interface the rest of the service consumes.
package source

import (
	"context"
	"time"
)

// Account is one connected identity (a Google account, an ICS feed...).
// The UI groups and colours events by account, never by calendar.
type Account struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Color     string     `json:"color"`
	Calendars []Calendar `json:"calendars"`
	// NeedsReconnect is set when the stored credentials stopped working.
	// Events from other accounts keep flowing; the UI shows a discreet hint.
	NeedsReconnect bool `json:"needs_reconnect,omitempty"`
}

// Calendar is one agenda inside an account.
type Calendar struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Event is a single occurrence. Recurring series are always expanded by the
// backend before reaching here, so there is no RRULE anywhere in this model.
//
// Times are absolute instants; the renderer converts them to the configured
// zone. For all-day events Start and End are midnights in the configured
// zone and End is EXCLUSIVE (a one-day event on the 24th has End = the 25th),
// matching both the Google API and RFC 5545.
type Event struct {
	ID          string    `json:"id"`
	Account     string    `json:"account"`
	Calendar    string    `json:"calendar"`
	Title       string    `json:"title"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day"`
	Location    string    `json:"location,omitempty"`
	URL         string    `json:"url,omitempty"`
	Description string    `json:"description,omitempty"`
}

// Window is the half-open interval [From, To) the caller wants events for.
type Window struct {
	From time.Time
	To   time.Time
}

// Overlaps reports whether any part of the event falls inside the window.
func (w Window) Overlaps(e Event) bool {
	return e.Start.Before(w.To) && e.End.After(w.From)
}

// Source is a read-only calendar backend. The renderer never knows where an
// event came from; it only sees Accounts and Events.
type Source interface {
	// Accounts lists the accounts this source knows about, with their calendars.
	Accounts(ctx context.Context) ([]Account, error)
	// Fetch returns every event overlapping the window, in any order.
	Fetch(ctx context.Context, w Window) ([]Event, error)
}
