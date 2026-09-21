// Package demo is a Source with fictitious events. It is what runs when no
// account is connected, so the grid can be evaluated without a Google Cloud
// project. The data mirrors mockup/agenda.html, anchored to the requested
// window so it never goes stale: "day 0" is the first day of the window.
package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

// Source implements source.Source with generated data.
type Source struct {
	loc *time.Location
}

// New returns a demo source rendering its dates in loc.
func New(loc *time.Location) *Source {
	if loc == nil {
		loc = time.UTC
	}
	return &Source{loc: loc}
}

var accounts = []source.Account{
	{ID: "pessoal", Name: "Pessoal", Color: "#7aa2f7", Calendars: []source.Calendar{
		{ID: "pessoal@example.com", Name: "Pessoal"},
		{ID: "pt.brazilian#holiday@group.v.calendar.google.com", Name: "Feriados no Brasil"},
	}},
	{ID: "trabalho", Name: "Trabalho", Color: "#9ece6a", Calendars: []source.Calendar{
		{ID: "trabalho@example.com", Name: "Trabalho"},
		{ID: "sprints@example.com", Name: "Sprints"},
	}},
	{ID: "familia", Name: "Família", Color: "#e0af68", Calendars: []source.Calendar{
		{ID: "familia@group.calendar.google.com", Name: "Família"},
	}},
}

// Accounts returns the three fictitious accounts of the mockup.
func (s *Source) Accounts(ctx context.Context) ([]source.Account, error) {
	out := make([]source.Account, len(accounts))
	copy(out, accounts)
	return out, nil
}

// Fetch generates the mockup's week relative to w.From and keeps only what
// overlaps the window.
func (s *Source) Fetch(ctx context.Context, w source.Window) ([]source.Event, error) {
	if !w.To.After(w.From) {
		return nil, fmt.Errorf("demo: empty window %s..%s", w.From, w.To)
	}
	f := w.From.In(s.loc)
	day0 := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, s.loc)

	n := 0
	timed := func(day, hh, mm, minutes int, acc, title string) source.Event {
		n++
		start := day0.AddDate(0, 0, day).Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
		return source.Event{
			ID:       fmt.Sprintf("demo-%02d", n),
			Account:  acc,
			Calendar: accounts[idx(acc)].Calendars[0].ID,
			Title:    title,
			Start:    start,
			End:      start.Add(time.Duration(minutes) * time.Minute),
		}
	}
	allDay := func(day, days int, acc, title string) source.Event {
		n++
		start := day0.AddDate(0, 0, day)
		return source.Event{
			ID:       fmt.Sprintf("demo-%02d", n),
			Account:  acc,
			Calendar: accounts[idx(acc)].Calendars[0].ID,
			Title:    title,
			Start:    start,
			End:      start.AddDate(0, 0, days), // exclusive
			AllDay:   true,
		}
	}

	all := []source.Event{
		// day 0 ("today" in the mockup)
		timed(0, 9, 0, 100, "trabalho", "Daily + planning"),
		timed(0, 12, 0, 30, "pessoal", "Almoço"),
		timed(0, 14, 0, 120, "trabalho", "Code review — Ownlar"),
		timed(0, 20, 0, 90, "familia", "Jantar em família"),
		// day 1
		timed(1, 8, 0, 80, "trabalho", "1:1 com o time"),
		timed(1, 16, 0, 120, "pessoal", "Academia"),
		// day 2: two overlapping events, rendered side by side
		timed(2, 10, 0, 120, "trabalho", "Cliente Roble"),
		timed(2, 11, 0, 150, "pessoal", "Dentista"),
		timed(2, 15, 0, 180, "trabalho", "Workshop LLM local"),
		// day 3: an all-day event plus timed ones
		allDay(3, 1, "familia", "🎂 Aniversário da Ana"),
		timed(3, 9, 0, 60, "trabalho", "Deploy janela"),
		timed(3, 18, 0, 120, "pessoal", "Aula de inglês"),
		// day 4: a two-day all-day event (days 4 and 5, End = day 6 exclusive)
		allDay(4, 2, "pessoal", "✈️ Viagem — Chapada"),
		timed(4, 9, 0, 100, "trabalho", "Retro da sprint"),
		timed(4, 13, 0, 30, "trabalho", "Call rápida"),
		// day 5
		timed(5, 11, 0, 180, "pessoal", "Trilha — Chapada"),
		// day 6
		timed(6, 17, 0, 150, "familia", "Churrasco"),
	}
	all[2].Location = "Sala 2 / Meet"
	all[2].Description = "Revisar o PR do ledger antes do deploy."
	all[7].Location = "Clínica Sorriso, Pituba"

	out := all[:0]
	for _, e := range all {
		if w.Overlaps(e) {
			out = append(out, e)
		}
	}
	return out, nil
}

func idx(id string) int {
	for i, a := range accounts {
		if a.ID == id {
			return i
		}
	}
	panic("demo: unknown account " + id)
}
