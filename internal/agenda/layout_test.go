package agenda

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
)

var bahia = func() *time.Location { l, _ := time.LoadLocation("America/Bahia"); return l }()

func opts(now time.Time) LayoutOptions {
	return LayoutOptions{StartHour: 7, EndHour: 22, MinHour: 0, MaxHour: 24, Now: now, Labels: LabelsFor("pt-BR")}
}

func demoView(t *testing.T) View {
	t.Helper()
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	now := time.Date(2026, 9, 21, 10, 40, 0, 0, bahia)
	svc := &Service{Sources: []source.Source{demo.New(bahia)}, Location: bahia, Now: func() time.Time { return now }}
	res := svc.Collect(context.Background(), source.Window{From: from, To: from.AddDate(0, 0, 7)})
	return Layout(res, opts(now))
}

func result(from time.Time, days int, evs ...source.Event) Result {
	return Result{
		GeneratedAt: from, From: from, To: from.AddDate(0, 0, days),
		Accounts: []source.Account{{ID: "a", Name: "A", Color: "#111111"}, {ID: "b", Name: "B", Color: "#222222"}},
		Events:   evs,
	}
}

func at(day, hh, mm, minutes int, acc, title string) source.Event {
	s := time.Date(2026, 9, 21+day, hh, mm, 0, 0, bahia)
	return source.Event{ID: title, Account: acc, Title: title, Start: s, End: s.Add(time.Duration(minutes) * time.Minute)}
}

func TestDemoMatchesMockupGeometry(t *testing.T) {
	v := demoView(t)
	if v.H0 != 7 || v.H1 != 22 {
		t.Fatalf("hours %d..%d, want 7..22 (nothing outside the floor)", v.H0, v.H1)
	}
	if len(v.Days) != 7 || !v.Days[0].Today || v.Days[0].Dow != "Seg" || v.Days[0].Num != "21" {
		t.Fatalf("days: %+v", v.Days[0])
	}
	// Monday: Daily 09:00-10:40 → top 13.33%, height 11.11% (as in the mockup)
	daily := v.Days[0].Blocks[0]
	if daily.Top != "13.33" || daily.Height != "11.11" || daily.Left != "0" || daily.Width != "100" {
		t.Errorf("daily = %+v", daily)
	}
	// Lunch is 30 min → short
	if lunch := v.Days[0].Blocks[1]; !lunch.Short || lunch.Height != "3.33" {
		t.Errorf("lunch = %+v", lunch)
	}
	// Now line at 10:40 → (3.67/15) = 24.44%
	if v.Days[0].NowTop != "24.44" {
		t.Errorf("now = %q", v.Days[0].NowTop)
	}
	for i := 1; i < 7; i++ {
		if v.Days[i].NowTop != "" {
			t.Errorf("day %d has a now line", i)
		}
	}
	// Wednesday: Roble and Dentista side by side, Workshop full width
	wed := v.Days[2].Blocks
	if len(wed) != 3 {
		t.Fatalf("wed blocks = %d", len(wed))
	}
	if wed[0].Left != "0" || wed[0].Width != "50" || wed[1].Left != "50" || wed[1].Width != "50" {
		t.Errorf("overlap columns: %+v / %+v", wed[0], wed[1])
	}
	if wed[2].Width != "100" {
		t.Errorf("workshop should be full width: %+v", wed[2])
	}
	// All-day band: birthday on Thu (col 5), trip Fri-Sat (col 6 span 2), one row
	if len(v.AllDay) != 2 || v.AllDayRows != 1 {
		t.Fatalf("band = %+v rows=%d", v.AllDay, v.AllDayRows)
	}
	if b := v.AllDay[0]; b.Col != 5 || b.Span != 1 || b.Row != 1 || !b.Far {
		t.Errorf("birthday = %+v", b)
	}
	if b := v.AllDay[1]; b.Col != 6 || b.Span != 2 || b.Row != 1 || !b.Far {
		t.Errorf("trip = %+v", b)
	}
	// Hour marks: even hours 08..20
	if len(v.Hours) != 7 || v.Hours[0].Label != "08" || v.Hours[6].Label != "20" || v.Hours[0].Top != "6.67" {
		t.Errorf("hours = %+v", v.Hours)
	}
	// Mobile list: Thu..Sun, 8 lines (the mockup forgot "Call rápida"),
	// the two-day trip listed once, sorted by time
	if v.RestTitle != "Qui 24 — Dom 27" || len(v.Rest) != 8 {
		t.Errorf("rest = %q %+v", v.RestTitle, v.Rest)
	}
	if v.Rest[0].Title != "🎂 Aniversário da Ana" || v.Rest[0].Time != "dia" {
		t.Errorf("rest[0] = %+v", v.Rest[0])
	}
	if v.Legend[2].Name != "Família" || v.Legend[2].Color != "#e0af68" || v.UpdatedAt != "10:40" {
		t.Errorf("legend/updated: %+v %s", v.Legend, v.UpdatedAt)
	}
}

func TestHourRangeExpands(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	v := Layout(result(from, 7, at(0, 5, 30, 60, "a", "early"), at(1, 21, 0, 90, "b", "late")), opts(from))
	if v.H0 != 5 || v.H1 != 23 {
		t.Errorf("hours %d..%d, want 5..23 (floor 05:30, ceil 22:30)", v.H0, v.H1)
	}
	// clamped by min/max
	o := opts(from)
	o.MinHour, o.MaxHour = 6, 22
	v = Layout(result(from, 7, at(0, 5, 30, 60, "a", "early"), at(1, 21, 0, 90, "b", "late")), o)
	if v.H0 != 6 || v.H1 != 22 {
		t.Errorf("clamped hours %d..%d, want 6..22", v.H0, v.H1)
	}
	if b := v.Days[0].Blocks[0]; b.Top != "0" {
		t.Errorf("early event should be clipped to the top: %+v", b)
	}
	// an event ending exactly at 22:00 does not push to 23
	v = Layout(result(from, 7, at(0, 21, 0, 60, "a", "x")), opts(from))
	if v.H1 != 22 {
		t.Errorf("ceil(22:00) gave %d", v.H1)
	}
}

func TestMidnightCrossingIsSlicedNotStretched(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	v := Layout(result(from, 7, at(0, 23, 0, 120, "a", "party")), opts(from))
	if v.H0 != 7 || v.H1 != 24 {
		t.Fatalf("hours %d..%d, want 7..24 (real start 23:00 expands the end, the 01:00 clip does not touch the start)", v.H0, v.H1)
	}
	if len(v.Days[0].Blocks) != 1 || v.Days[0].Blocks[0].Height != "5.88" { // 1h of 17h
		t.Errorf("day0 = %+v", v.Days[0].Blocks)
	}
	// The 00:00-01:00 tail on day 1 is outside 07..24 and is dropped.
	if len(v.Days[1].Blocks) != 0 {
		t.Errorf("day1 should have no visible slice: %+v", v.Days[1].Blocks)
	}
	// With min_hour reaching 0 the tail shows as a continuation at top 0.
	o := opts(from)
	o.StartHour = 0
	v = Layout(result(from, 7, at(0, 23, 0, 120, "a", "party")), o)
	if len(v.Days[1].Blocks) != 1 || v.Days[1].Blocks[0].Top != "0" || v.Days[1].Blocks[0].Time != "23:00" {
		t.Errorf("continuation = %+v", v.Days[1].Blocks)
	}
}

func TestLongTimedEventGoesToBand(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	v := Layout(result(from, 7, at(1, 9, 0, 36*60, "a", "offsite")), opts(from))
	if len(v.AllDay) != 1 || v.AllDay[0].Col != 3 || v.AllDay[0].Span != 2 {
		t.Errorf("band = %+v", v.AllDay)
	}
}

func TestBandClipsToWindowAndStacks(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	mk := func(startDay, days int, title string) source.Event {
		s := from.AddDate(0, 0, startDay)
		return source.Event{ID: title, Account: "a", Title: title, Start: s, End: s.AddDate(0, 0, days), AllDay: true}
	}
	v := Layout(result(from, 7, mk(-2, 4, "before"), mk(1, 2, "mid"), mk(5, 5, "after"), mk(0, 1, "single")), opts(from))
	if v.AllDayRows != 2 {
		t.Fatalf("rows = %d, want 2: %+v", v.AllDayRows, v.AllDay)
	}
	byTitle := map[string]BandItem{}
	for _, b := range v.AllDay {
		byTitle[b.Title] = b
	}
	if b := byTitle["before"]; b.Col != 2 || b.Span != 2 || b.Row != 1 || b.Far || b.MobileSpan != 2 { // days -2..1 → 0..1
		t.Errorf("before = %+v", b)
	}
	if b := byTitle["mid"]; b.MobileSpan != 2 || b.Far { // days 1..2 fit the 3 mobile columns
		t.Errorf("mid mobile = %+v", b)
	}
	v2 := Layout(result(from, 7, mk(2, 3, "edge")), opts(from)) // days 2..4 → clipped to 1 column on mobile
	if b := v2.AllDay[0]; b.MobileSpan != 1 || b.Far {
		t.Errorf("edge mobile = %+v", b)
	}
	if b := byTitle["single"]; b.Col != 2 || b.Span != 1 || b.Row != 2 { // collides with "before"
		t.Errorf("single = %+v", b)
	}
	if b := byTitle["mid"]; b.Col != 3 || b.Span != 2 || b.Row != 2 { // starts on day1 where row1 is busy
		t.Errorf("mid = %+v", b)
	}
	if b := byTitle["after"]; b.Col != 7 || b.Span != 2 || b.Row != 1 { // days 5..9 → 5..6
		t.Errorf("after = %+v", b)
	}
	// Fully outside events never appear.
	v = Layout(result(from, 7, mk(-3, 3, "gone"), mk(7, 1, "future")), opts(from))
	if len(v.AllDay) != 0 || v.AllDayRows != 1 {
		t.Errorf("outside band = %+v rows=%d", v.AllDay, v.AllDayRows)
	}
}

func TestThreeWayOverlap(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	v := Layout(result(from, 1,
		at(0, 9, 0, 180, "a", "long"), at(0, 9, 30, 60, "b", "m1"), at(0, 10, 0, 60, "a", "m2"), at(0, 10, 30, 30, "b", "m3"),
		at(0, 15, 0, 60, "a", "alone")), opts(from))
	b := v.Days[0].Blocks
	if len(b) != 5 {
		t.Fatalf("blocks = %d", len(b))
	}
	// long=col0, m1=col1, m2=col2 (m1 still running), m3=col1 (m1 ended 10:30)
	want := []struct{ Left, Width string }{{"0", "33.33"}, {"33.33", "33.33"}, {"66.67", "33.33"}, {"33.33", "33.33"}, {"0", "100"}}
	for i, w := range want {
		if b[i].Left != w.Left || b[i].Width != w.Width {
			t.Errorf("%s: left=%s width=%s want %+v", b[i].Title, b[i].Left, b[i].Width, w)
		}
	}
}

func TestEmptyAndNowOutsideRange(t *testing.T) {
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	v := Layout(result(from, 7), opts(time.Date(2026, 9, 21, 23, 30, 0, 0, bahia)))
	if len(v.Days) != 7 || v.AllDayRows != 1 || len(v.Rest) != 0 || v.RestTitle == "" {
		t.Errorf("empty view: %+v", v)
	}
	if v.Days[0].NowTop != "" {
		t.Errorf("now at 23:30 is outside 7..22, got %q", v.Days[0].NowTop)
	}
	if !strings.HasPrefix(v.Labels.AllDay, "dia") {
		t.Errorf("labels not pt-BR: %+v", v.Labels)
	}
}
