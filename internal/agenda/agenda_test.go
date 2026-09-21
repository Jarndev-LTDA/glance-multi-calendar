package agenda

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
)

type broken struct{}

func (broken) Accounts(context.Context) ([]source.Account, error) {
	return []source.Account{{ID: "x"}}, nil
}
func (broken) Fetch(context.Context, source.Window) ([]source.Event, error) {
	return nil, errors.New("boom")
}

func TestCollectMergesAndSurvivesFailure(t *testing.T) {
	loc, _ := time.LoadLocation("America/Bahia")
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) // deliberately UTC
	svc := &Service{
		Sources:  []source.Source{demo.New(loc), broken{}},
		Location: loc,
		Now:      func() time.Time { return from },
	}
	res := svc.Collect(context.Background(), source.Window{From: from, To: from.AddDate(0, 0, 7)})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if len(res.Accounts) != 3 {
		t.Fatalf("accounts = %d, want 3 (broken source contributes none)", len(res.Accounts))
	}
	if len(res.Events) == 0 {
		t.Fatal("no events from the healthy source")
	}
	if !res.Demo {
		t.Error("demo source must flag the result as demo")
	}
	if res.Timezone != "America/Bahia" || res.From.Location() != loc {
		t.Errorf("times not converted to configured zone: tz=%s from=%s", res.Timezone, res.From)
	}
	for i := 1; i < len(res.Events); i++ {
		if res.Events[i].Start.Before(res.Events[i-1].Start) {
			t.Fatalf("not sorted at %d: %s before %s", i, res.Events[i].Start, res.Events[i-1].Start)
		}
	}
	for _, e := range res.Events {
		if e.Start.Location() != loc {
			t.Errorf("%s: start not in configured zone", e.Title)
		}
	}
}

func TestLessOrdering(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	allDay := source.Event{Title: "b", Start: t0, End: t0.Add(24 * time.Hour), AllDay: true}
	long := source.Event{Title: "c", Start: t0, End: t0.Add(2 * time.Hour)}
	short := source.Event{Title: "a", Start: t0, End: t0.Add(time.Hour)}
	if !less(allDay, long) || !less(long, short) || less(short, long) {
		t.Error("expected all-day < longer < shorter at the same start")
	}
	later := source.Event{Title: "0", Start: t0.Add(time.Minute), End: t0.Add(time.Hour)}
	if !less(short, later) {
		t.Error("earlier start must come first regardless of title")
	}
}
