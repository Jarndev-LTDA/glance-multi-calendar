// Package agenda merges events from every configured Source into a single,
// sorted timeline in the configured time zone.
package agenda

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

// Service fans out to all sources and merges what comes back.
type Service struct {
	Sources  []source.Source
	Location *time.Location
	Now      func() time.Time
}

// Result is the payload of /events.json.
type Result struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Timezone    string           `json:"timezone"`
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	Accounts    []source.Account `json:"accounts"`
	Events      []source.Event   `json:"events"`
	// Errors lists sources that failed. The rest of the payload is still
	// valid: one broken account must never blank the whole widget.
	Errors []string `json:"errors,omitempty"`
	// Demo is true while the fictitious data is being served.
	Demo bool `json:"demo,omitempty"`
}

// Collect queries every source for the window. A failing source is reported
// in Result.Errors and skipped; it never fails the whole call.
func (s *Service) Collect(ctx context.Context, w source.Window) Result {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	res := Result{
		GeneratedAt: now().In(loc),
		Timezone:    loc.String(),
		From:        w.From.In(loc),
		To:          w.To.In(loc),
		Accounts:    []source.Account{},
		Events:      []source.Event{},
	}
	for i, src := range s.Sources {
		accts, err := src.Accounts(ctx)
		if err != nil {
			msg := fmt.Sprintf("source %d: accounts: %v", i, err)
			slog.Warn("agenda: source failed", "source", i, "err", err)
			res.Errors = append(res.Errors, msg)
			continue
		}
		evs, err := src.Fetch(ctx, w)
		if err != nil {
			msg := fmt.Sprintf("source %d: fetch: %v", i, err)
			slog.Warn("agenda: source failed", "source", i, "err", err)
			res.Errors = append(res.Errors, msg)
			continue
		}
		res.Accounts = append(res.Accounts, accts...)
		if wr, ok := src.(source.Warner); ok {
			res.Errors = append(res.Errors, wr.Warnings()...)
		}
		if d, ok := src.(interface{ Demo() bool }); ok && d.Demo() {
			res.Demo = true
		}
		for _, e := range evs {
			if !w.Overlaps(e) {
				continue // defensive: sources should already filter
			}
			e.Start = e.Start.In(loc)
			e.End = e.End.In(loc)
			res.Events = append(res.Events, e)
		}
	}
	sort.SliceStable(res.Events, func(i, j int) bool { return less(res.Events[i], res.Events[j]) })
	return res
}

// less orders by start, all-day before timed at the same instant, then longer
// first (so the wider block is laid out first), then title for determinism.
func less(a, b source.Event) bool {
	if !a.Start.Equal(b.Start) {
		return a.Start.Before(b.Start)
	}
	if a.AllDay != b.AllDay {
		return a.AllDay
	}
	if !a.End.Equal(b.End) {
		return a.End.After(b.End)
	}
	return strings.Compare(a.Title, b.Title) < 0
}
