package demo

import (
	"context"
	"testing"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

func window(t *testing.T, loc *time.Location, days int) source.Window {
	t.Helper()
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, loc)
	return source.Window{From: from, To: from.AddDate(0, 0, days)}
}

func TestFetchFullWeek(t *testing.T) {
	loc, _ := time.LoadLocation("America/Bahia")
	s := New(loc)
	w := window(t, loc, 7)
	evs, err := s.Fetch(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 17 {
		t.Fatalf("got %d events, want 17", len(evs))
	}
	accts, _ := s.Accounts(context.Background())
	known := map[string]bool{}
	for _, a := range accts {
		known[a.ID] = true
	}
	ids := map[string]bool{}
	for _, e := range evs {
		if !w.Overlaps(e) {
			t.Errorf("%s outside window: %s..%s", e.Title, e.Start, e.End)
		}
		if !known[e.Account] {
			t.Errorf("%s references unknown account %q", e.Title, e.Account)
		}
		if ids[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		ids[e.ID] = true
		if !e.End.After(e.Start) {
			t.Errorf("%s: end %s not after start %s", e.Title, e.End, e.Start)
		}
		if e.Start.Location() != loc {
			t.Errorf("%s: start not in configured zone", e.Title)
		}
	}
}

func TestFetchFiltersToWindow(t *testing.T) {
	loc, _ := time.LoadLocation("America/Bahia")
	s := New(loc)
	w := window(t, loc, 3) // days 0..2 only
	evs, err := s.Fetch(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 9 {
		t.Fatalf("got %d events for 3 days, want 9", len(evs))
	}
	for _, e := range evs {
		if !e.Start.Before(w.To) {
			t.Errorf("%s starts after window end", e.Title)
		}
	}
}

// All-day events end at the NEXT midnight (exclusive end, as in the Google
// API and RFC 5545). A two-day trip on days 4-5 therefore ends on day 6.
func TestAllDayEndIsExclusive(t *testing.T) {
	loc, _ := time.LoadLocation("America/Bahia")
	s := New(loc)
	evs, _ := s.Fetch(context.Background(), window(t, loc, 7))
	var birthday, trip *source.Event
	for i := range evs {
		switch {
		case evs[i].AllDay && evs[i].Account == "familia":
			birthday = &evs[i]
		case evs[i].AllDay && evs[i].Account == "pessoal":
			trip = &evs[i]
		}
	}
	if birthday == nil || trip == nil {
		t.Fatal("all-day events missing")
	}
	day := func(d int) time.Time { return time.Date(2026, 9, 21+d, 0, 0, 0, 0, loc) }
	if !birthday.Start.Equal(day(3)) || !birthday.End.Equal(day(4)) {
		t.Errorf("birthday = %s..%s, want day3..day4", birthday.Start, birthday.End)
	}
	if !trip.Start.Equal(day(4)) || !trip.End.Equal(day(6)) {
		t.Errorf("trip = %s..%s, want day4..day6", trip.Start, trip.End)
	}
	// A window covering only day 5 still sees the trip (it spans days 4-5)...
	only5 := source.Window{From: day(5), To: day(6)}
	if !only5.Overlaps(*trip) {
		t.Error("trip should overlap day 5")
	}
	// ...but a window starting on day 6 must not (exclusive end).
	only6 := source.Window{From: day(6), To: day(7)}
	if only6.Overlaps(*trip) {
		t.Error("trip must not overlap day 6")
	}
}

func TestWindowAnchorsOnFromDate(t *testing.T) {
	loc, _ := time.LoadLocation("America/Bahia")
	s := New(loc)
	from := time.Date(2027, 3, 10, 15, 30, 0, 0, loc) // mid-day: day 0 is still the 10th
	evs, _ := s.Fetch(context.Background(), source.Window{From: from, To: from.AddDate(0, 0, 7)})
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	// Day 0 is the 10th even though the window starts mid-afternoon: the
	// 14:00-16:00 event still overlaps, the 09:00 one is correctly dropped.
	first := evs[0]
	if first.Start.Day() != 10 || first.Start.Hour() != 14 {
		t.Errorf("first event at %s, want 10th 14:00", first.Start)
	}
	for _, e := range evs {
		if e.Start.Day() == 10 && e.Start.Hour() == 9 {
			t.Errorf("09:00 event should be outside a window starting 15:30")
		}
	}
}

func TestEmptyWindowErrors(t *testing.T) {
	s := New(time.UTC)
	now := time.Now()
	if _, err := s.Fetch(context.Background(), source.Window{From: now, To: now}); err == nil {
		t.Fatal("expected error")
	}
}
