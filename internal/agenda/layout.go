package agenda

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
)

// Labels are the UI strings, chosen by config `language`.
type Labels struct {
	Title    string
	Weekdays [7]string // Sunday first, like time.Weekday
	AllDay   string
	Updated  string
	Rest     string // header of the mobile list, e.g. "%s — %s"
}

var labelSets = map[string]Labels{
	"pt-BR": {Title: "Agenda", Weekdays: [7]string{"Dom", "Seg", "Ter", "Qua", "Qui", "Sex", "Sáb"},
		AllDay: "dia\ntodo", Updated: "atualizado", Rest: "%s — %s"},
	"en": {Title: "Calendar", Weekdays: [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
		AllDay: "all\nday", Updated: "updated", Rest: "%s — %s"},
}

// LabelsFor returns the label set for a language, falling back to English.
func LabelsFor(lang string) Labels {
	if l, ok := labelSets[lang]; ok {
		return l
	}
	return labelSets["en"]
}

// LayoutOptions control the geometry of the grid.
type LayoutOptions struct {
	StartHour, EndHour int // floor of the visible range
	MinHour, MaxHour   int // hard limits of the expansion
	Now                time.Time
	Labels             Labels
	// MobileDays is how many day columns the narrow layout keeps; the
	// remaining days are listed below the grid. Default 3.
	MobileDays int
}

// View is everything the template needs. All numbers are pre-formatted
// strings so the HTML is compact and the golden file stable.
type View struct {
	Title      string
	Legend     []LegendItem
	UpdatedAt  string
	H0, H1     int
	Hours      []HourMark
	Days       []DayView
	AllDay     []BandItem
	AllDayRows int
	Rest       []RestItem
	RestTitle  string
	Errors     []string
	Labels     Labels
}

// LegendItem is one account in the legend bar.
type LegendItem struct {
	Name, Color    string
	NeedsReconnect bool
}

// HourMark is a label in the hour gutter.
type HourMark struct{ Label, Top string }

// DayView is one column.
type DayView struct {
	Dow, Num string
	Today    bool
	Blocks   []Block
	NowTop   string // empty when the "now" line is not shown in this column
}

// Block is a timed event (or a per-day slice of one) positioned in %.
type Block struct {
	Title, Time, Color, URL, Tip string
	Top, Height, Left, Width     string
	Short                        bool
}

// BandItem is an all-day (or ≥24h) event in the top band.
type BandItem struct {
	Title, Color, URL, Tip string
	Col, Span, Row         int // CSS grid coordinates (Col counts the gutter)
	// MobileSpan is Span clipped to the narrow layout's day columns; Far
	// marks items that start beyond them (hidden there, listed below).
	MobileSpan int
	Far        bool
}

// RestItem is a line of the narrow-layout list.
type RestItem struct{ Day, Time, Title, Color string }

type segment struct {
	ev         source.Event
	start, end time.Time // clipped to one day
	col, cols  int
}

// Layout turns a Result into a View. It is pure: no I/O, no clock access
// beyond opt.Now.
func Layout(res Result, opt LayoutOptions) View {
	loc := res.From.Location()
	lab := opt.Labels
	if lab.Weekdays[0] == "" {
		lab = LabelsFor("en")
	}
	if opt.MobileDays <= 0 {
		opt.MobileDays = 3
	}
	now := opt.Now.In(loc)
	days := int(math.Round(res.To.Sub(res.From).Hours() / 24))
	if days < 1 {
		days = 1
	}
	dayStart := func(i int) time.Time { return res.From.AddDate(0, 0, i) }
	dayIndex := func(t time.Time) int { // index of the day containing t
		t = t.In(loc)
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		return int(math.Round(d.Sub(res.From).Hours() / 24))
	}
	colorOf := map[string]string{}
	for _, a := range res.Accounts {
		colorOf[a.ID] = a.Color
	}

	v := View{Title: lab.Title, Labels: lab, Errors: res.Errors, UpdatedAt: res.GeneratedAt.In(loc).Format("15:04")}
	for _, a := range res.Accounts {
		v.Legend = append(v.Legend, LegendItem{Name: a.Name, Color: a.Color, NeedsReconnect: a.NeedsReconnect})
	}

	// 1. Split events into the all-day band and per-day timed segments.
	var band []source.Event
	perDay := make([][]segment, days)
	h0, h1 := opt.StartHour, opt.EndHour
	for _, e := range res.Events {
		if e.AllDay || e.End.Sub(e.Start) >= 24*time.Hour {
			band = append(band, e)
			continue
		}
		s, en := e.Start.In(loc), e.End.In(loc)
		for di := max(dayIndex(s), 0); di < days; di++ {
			ds := dayStart(di)
			de := dayStart(di + 1)
			if !s.Before(de) {
				continue
			}
			if !en.After(ds) {
				break
			}
			seg := segment{ev: e, start: maxT(s, ds), end: minT(en, de)}
			perDay[di] = append(perDay[di], seg)
			// Expand the hour range so the event's START hour is always
			// visible, and its end too when it is on the same day. The tail
			// of a 23:00-01:00 event is a continuation: it never stretches
			// every day down to 00:00.
			if seg.start.Equal(s) {
				h0 = min(h0, clockHour(s))
				h1 = max(h1, clockHour(s)+1)
				if seg.end.Equal(en) {
					h1 = max(h1, clockHourCeil(en))
				}
			}
		}
	}
	h0 = max(h0, opt.MinHour)
	h1 = min(h1, opt.MaxHour)
	if h1 <= h0 {
		h0, h1 = opt.StartHour, opt.EndHour
	}
	v.H0, v.H1 = h0, h1
	span := float64(h1 - h0)
	pct := func(hours float64) string { return fmtPct(hours / span * 100) }

	// 2. Hour gutter: every 2 h when the range is wide, every hour otherwise.
	step := 2
	if h1-h0 < 8 {
		step = 1
	}
	for h := h0 + 1; h < h1; h++ {
		if step == 2 && h%2 != 0 {
			continue
		}
		v.Hours = append(v.Hours, HourMark{Label: fmt.Sprintf("%02d", h), Top: pct(float64(h - h0))})
	}

	// 3. Columns per day: cluster overlapping segments, assign columns.
	todayIdx := dayIndex(now)
	for di := 0; di < days; di++ {
		ds := dayStart(di)
		dv := DayView{Dow: lab.Weekdays[ds.Weekday()], Num: strconv.Itoa(ds.Day()), Today: di == todayIdx}
		segs := perDay[di]
		sort.SliceStable(segs, func(i, j int) bool {
			if !segs[i].start.Equal(segs[j].start) {
				return segs[i].start.Before(segs[j].start)
			}
			return segs[i].end.After(segs[j].end)
		})
		assignColumns(segs)
		for _, sg := range segs {
			top := clockFloat(sg.start) - float64(h0)
			if sg.start.Equal(ds) { // starts at midnight: it is a continuation
				top = 0
			}
			bottom := clockFloat(sg.end) - float64(h0)
			if sg.end.Equal(dayStart(di + 1)) {
				bottom = float64(h1 - h0)
			}
			top, bottom = math.Max(top, 0), math.Min(bottom, span)
			if bottom <= top {
				continue // outside the visible hours after min/max clamping
			}
			height := math.Max(bottom-top, 1.0/3) // never thinner than 20 min
			dur := sg.ev.End.Sub(sg.ev.Start)
			width := 100 / float64(sg.cols)
			dv.Blocks = append(dv.Blocks, Block{
				Title: sg.ev.Title, Time: sg.ev.Start.In(loc).Format("15:04"),
				Color: colorOf[sg.ev.Account], URL: sg.ev.URL, Tip: tip(sg.ev, loc),
				Top: pct(top), Height: pct(height),
				Left: fmtPct(float64(sg.col) * width), Width: fmtPct(width),
				Short: dur <= 30*time.Minute,
			})
		}
		if dv.Today {
			nh := clockFloat(now)
			if nh >= float64(h0) && nh <= float64(h1) {
				dv.NowTop = pct(nh - float64(h0))
			}
		}
		v.Days = append(v.Days, dv)
	}

	// 4. All-day band: explicit grid rows, first free row wins.
	sort.SliceStable(band, func(i, j int) bool {
		if !band[i].Start.Equal(band[j].Start) {
			return band[i].Start.Before(band[j].Start)
		}
		return band[i].End.After(band[j].End)
	})
	var rowEnd []int // last occupied day index per row
	for _, e := range band {
		first := max(dayIndex(e.Start), 0)
		last := min(dayIndex(e.End.Add(-time.Nanosecond)), days-1)
		if last < first {
			continue
		}
		row := 0
		for ; row < len(rowEnd); row++ {
			if rowEnd[row] < first {
				break
			}
		}
		if row == len(rowEnd) {
			rowEnd = append(rowEnd, 0)
		}
		rowEnd[row] = last
		v.AllDay = append(v.AllDay, BandItem{
			Title: e.Title, Color: colorOf[e.Account], URL: e.URL, Tip: tip(e, loc),
			Col: first + 2, Span: last - first + 1, Row: row + 1,
			MobileSpan: max(min(last, opt.MobileDays-1)-first+1, 1), Far: first >= opt.MobileDays,
		})
	}
	v.AllDayRows = max(len(rowEnd), 1)

	// 5. Narrow layout: the days that do not fit become a list.
	if days > opt.MobileDays {
		v.RestTitle = fmt.Sprintf(lab.Rest, dayLabel(lab, dayStart(opt.MobileDays)), dayLabel(lab, dayStart(days-1)))
		type item struct {
			at time.Time
			RestItem
		}
		var items []item
		for _, e := range res.Events {
			first := max(dayIndex(e.Start), 0)
			last := min(dayIndex(e.End.Add(-time.Nanosecond)), days-1)
			// Listed once, on its first day inside the list.
			di := max(first, opt.MobileDays)
			if di > last {
				continue
			}
			it := item{at: dayStart(di), RestItem: RestItem{
				Day: dayLabel(lab, dayStart(di)), Title: e.Title, Color: colorOf[e.Account],
			}}
			switch {
			case e.AllDay || e.End.Sub(e.Start) >= 24*time.Hour:
				it.Time = strings.Split(lab.AllDay, "\n")[0]
			case di == first:
				it.at = e.Start.In(loc)
				it.Time = it.at.Format("15:04")
			default:
				it.Time = "00:00"
			}
			items = append(items, it)
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
		for _, it := range items {
			v.Rest = append(v.Rest, it.RestItem)
		}
	}
	return v
}

// assignColumns groups overlapping segments into clusters and gives each a
// column; every segment learns how many columns its cluster has.
func assignColumns(segs []segment) {
	var colEnd []time.Time // end of the last segment in each column
	clusterStart := 0
	flush := func(upto int) {
		for i := clusterStart; i < upto; i++ {
			segs[i].cols = len(colEnd)
		}
		colEnd = colEnd[:0]
		clusterStart = upto
	}
	var clusterEnd time.Time
	for i := range segs {
		s := &segs[i]
		if len(colEnd) > 0 && !s.start.Before(clusterEnd) {
			flush(i)
		}
		placed := false
		for c := range colEnd {
			if !colEnd[c].After(s.start) {
				colEnd[c] = s.end
				s.col = c
				placed = true
				break
			}
		}
		if !placed {
			s.col = len(colEnd)
			colEnd = append(colEnd, s.end)
		}
		if s.end.After(clusterEnd) {
			clusterEnd = s.end
		}
	}
	flush(len(segs))
}

func tip(e source.Event, loc *time.Location) string {
	parts := []string{e.Title}
	if e.AllDay {
		parts = append(parts, e.Start.In(loc).Format("02/01"))
	} else {
		parts = append(parts, e.Start.In(loc).Format("02/01 15:04")+"–"+e.End.In(loc).Format("15:04"))
	}
	if e.Location != "" {
		parts = append(parts, e.Location)
	}
	if e.Description != "" {
		parts = append(parts, e.Description)
	}
	return strings.Join(parts, "\n")
}

func dayLabel(l Labels, t time.Time) string {
	return fmt.Sprintf("%s %d", l.Weekdays[t.Weekday()], t.Day())
}

// clockHour and friends use wall-clock time, not elapsed time since
// midnight, so DST transition days do not shift the grid.
func clockHour(t time.Time) int      { return t.Hour() }
func clockFloat(t time.Time) float64 { return float64(t.Hour()) + float64(t.Minute())/60 }
func clockHourCeil(t time.Time) int {
	if t.Minute() == 0 && t.Second() == 0 {
		return t.Hour()
	}
	return t.Hour() + 1
}

func fmtPct(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minT(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
