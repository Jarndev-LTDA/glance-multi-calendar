package render

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source"
	"github.com/jarndev-ltda/glance-multi-calendar/internal/source/demo"
)

var update = flag.Bool("update", false, "rewrite golden files")

var bahia = func() *time.Location { l, _ := time.LoadLocation("America/Bahia"); return l }()

func demoView(t *testing.T, extra ...source.Event) agenda.View {
	t.Helper()
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, bahia)
	now := time.Date(2026, 9, 21, 10, 40, 0, 0, bahia)
	svc := &agenda.Service{Sources: []source.Source{demo.New(bahia)}, Location: bahia, Now: func() time.Time { return now }}
	res := svc.Collect(context.Background(), source.Window{From: from, To: from.AddDate(0, 0, 7)})
	res.Events = append(res.Events, extra...)
	return agenda.Layout(res, agenda.LayoutOptions{StartHour: 7, EndHour: 22, MaxHour: 24, Now: now, Labels: agenda.LabelsFor("pt-BR")})
}

// The rendered demo week is the concrete form of "reproduces the mockup".
// Run `go test ./internal/render -update` after an intentional change.
func TestGoldenFragment(t *testing.T) {
	var buf bytes.Buffer
	if err := Fragment(&buf, demoView(t)); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "week.golden.html")
	if *update {
		os.WriteFile(golden, buf.Bytes(), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("fragment differs from %s; run with -update if intended", golden)
	}
}

func TestFragmentShape(t *testing.T) {
	var buf bytes.Buffer
	if err := Fragment(&buf, demoView(t)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<style>") || strings.Contains(out, "<html") || strings.Contains(out, "<body") {
		t.Error("fragment must be <style> + div only")
	}
	if strings.Contains(out, ":root") {
		t.Error("fragment must not define :root variables (it would override Glance's theme)")
	}
	for _, s := range []string{`class="ag-d ag-today"`, `--col:6;--span:2;--mspan:1`, `ag-far`, `class="ag-now"`, `left:calc(50% + 2px)`, `ag-curto`, `Qui 24 — Dom 27`} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q", s)
		}
	}
	// every selector in the CSS is scoped under .ag
	inComment := false
	for _, line := range strings.Split(weekCSS, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/*") {
			inComment = true
		}
		if inComment {
			inComment = !strings.HasSuffix(line, "*/")
			continue
		}
		if line == "" || strings.HasPrefix(line, "@") || line == "}" {
			continue
		}
		if !strings.HasPrefix(line, ".ag") {
			t.Errorf("unscoped CSS rule: %s", line)
		}
	}
}

// Account colours (--c:#hex) and var() fallbacks are the only literal
// colours allowed; anything else would ignore the Glance theme.
func TestNoFixedColors(t *testing.T) {
	var buf bytes.Buffer
	Fragment(&buf, demoView(t))
	hex := regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	allowed := regexp.MustCompile(`(--c:#[0-9a-fA-F]{6})|(var\(--[a-z-]+,#[0-9a-fA-F]{6}\))`)
	stripped := allowed.ReplaceAllString(buf.String(), "")
	if m := hex.FindAllString(stripped, -1); len(m) > 0 {
		t.Errorf("fixed colours outside --c and var() fallbacks: %v", m)
	}
}

func TestEscaping(t *testing.T) {
	hostile := `<script>alert("x")</script> "q" & 'a'`
	s := time.Date(2026, 9, 22, 9, 0, 0, 0, bahia)
	ev := source.Event{ID: "h", Account: "pessoal", Title: hostile, Location: hostile, Description: hostile,
		URL: "javascript:alert(1)", Start: s, End: s.Add(time.Hour)}
	allDay := source.Event{ID: "h2", Account: "pessoal", Title: hostile, AllDay: true,
		URL: "javascript:alert(2)", Start: s.Add(-9 * time.Hour), End: s.AddDate(0, 0, 1).Add(-9 * time.Hour)}
	var buf bytes.Buffer
	if err := Fragment(&buf, demoView(t, ev, allDay)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>") || strings.Contains(out, `alert("x")`) {
		t.Fatal("raw title leaked into HTML")
	}
	if strings.Contains(out, "javascript:") {
		t.Fatal("javascript: URL leaked")
	}
	if !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&#34;q&#34;") {
		t.Error("expected escaped title in text and title= attribute")
	}
	if strings.Count(out, "&lt;script&gt;") < 4 { // 2 events × (title attr + text)
		t.Errorf("hostile string should appear escaped in both text and attributes, got %d", strings.Count(out, "&lt;script&gt;"))
	}
}

func TestLegendNotes(t *testing.T) {
	v := demoView(t)
	v.Demo = true
	v.Errors = []string{`trabalho (x@y): needs reconnect`, `pessoal / "Projetos": http 500`}
	v.ErrorsTip = "2 aviso(s):\n" + strings.Join(v.Errors, "\n")
	v.Legend[1].NeedsReconnect = true
	var buf bytes.Buffer
	if err := Fragment(&buf, v); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`class="ag-demo">dados de demonstração<`, `class="ag-warn"`, `<em>reconectar</em>`, `class="ag-err" title="2 aviso(s):`, `&#34;Projetos&#34;`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, `"Projetos"`) {
		t.Error("error text must be escaped inside title=")
	}
	// none of it when everything is fine (the demo source itself is flagged demo)
	buf.Reset()
	clean := demoView(t)
	clean.Demo = false
	Fragment(&buf, clean)
	if strings.Contains(buf.String(), `class="ag-demo"`) || strings.Contains(buf.String(), `class="ag-err"`) || strings.Contains(buf.String(), `class="ag-warn"`) {
		t.Error("notes must be absent on a clean, non-demo view")
	}
	buf.Reset()
	Fragment(&buf, demoView(t))
	if !strings.Contains(buf.String(), `class="ag-demo"`) {
		t.Error("demo source view must carry the demo note")
	}
}

func TestPage(t *testing.T) {
	for _, th := range ThemeNames() {
		var buf bytes.Buffer
		if err := Page(&buf, demoView(t), "pt-BR", th); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.HasPrefix(out, "<!doctype html>") || !strings.Contains(out, ":root{--color-background:"+Themes[th]["--color-background"]) {
			t.Errorf("%s: page lacks doctype or theme vars", th)
		}
		if !strings.Contains(out, `<div class="ag">`) {
			t.Errorf("%s: fragment missing", th)
		}
	}
	if err := Page(&bytes.Buffer{}, demoView(t), "en", "neon"); err == nil {
		t.Error("unknown theme should fail")
	}
}
