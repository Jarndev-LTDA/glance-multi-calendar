// Package render turns an agenda.View into HTML: the bare fragment Glance's
// `extension` widget consumes, and a standalone page for debugging.
//
// Everything that comes from a calendar goes through html/template's
// contextual escaping. The only template.CSS/HTML values are our own
// embedded assets and theme constants, never calendar data.
package render

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/jarndev-ltda/glance-multi-calendar/internal/agenda"
)

//go:embed week.css
var weekCSS string

//go:embed week.html
var weekHTML string

//go:embed page.html
var pageHTML string

//go:embed connect.html
var connectHTML string

var tpl = template.Must(template.New("").Funcs(template.FuncMap{
	"lines": func(s string) []string { return strings.Split(s, "\n") },
}).Parse(weekHTML + pageHTML + connectHTML))

type fragmentData struct {
	agenda.View
	CSS template.CSS
}

// Fragment writes <style> + <div class="ag">…</div>. No html/body wrapper.
func Fragment(w io.Writer, v agenda.View) error {
	return tpl.ExecuteTemplate(w, "fragment", fragmentData{View: v, CSS: template.CSS(weekCSS)})
}

// Themes are approximations of Glance's default dark/light variables, used
// only by the standalone page so the fragment can be checked in both.
var Themes = map[string]map[string]string{
	"dark": {
		"--color-background": "#1b1c22", "--color-widget-background": "#22232b",
		"--color-text-base": "#a3a5b4", "--color-text-paragraph": "#c7c9d4", "--color-text-highlight": "#e7e8ee",
		"--color-text-subdue": "#6b6d7c", "--color-separator": "#33343e",
		"--color-primary": "#c6a0f6", "--color-negative": "#f07178", "--color-positive": "#9ece6a",
	},
	"light": {
		"--color-background": "#e9ebf1", "--color-widget-background": "#f6f7fa",
		"--color-text-base": "#555869", "--color-text-paragraph": "#34363f", "--color-text-highlight": "#16171c",
		"--color-text-subdue": "#8a8d9c", "--color-separator": "#d3d5de",
		"--color-primary": "#2f5fd3", "--color-negative": "#c8323f", "--color-positive": "#3c8a2e",
	},
}

// ThemeNames lists the themes the page accepts.
func ThemeNames() []string { return []string{"dark", "light"} }

type pageData struct {
	Title, Lang string
	ThemeCSS    template.CSS
	Fragment    fragmentData
}

// Page writes a complete HTML document embedding the fragment, with the
// Glance variables of the named theme defined on :root.
func Page(w io.Writer, v agenda.View, lang, theme string) error {
	if _, ok := Themes[theme]; !ok {
		return fmt.Errorf("unknown theme %q", theme)
	}
	return tpl.ExecuteTemplate(w, "page", pageData{
		Title: v.Title, Lang: lang, ThemeCSS: themeCSS(theme),
		Fragment: fragmentData{View: v, CSS: template.CSS(weekCSS)},
	})
}

// themeCSS renders a theme's variables as a `{...}` block for :root.
func themeCSS(theme string) template.CSS {
	vars := Themes[theme]
	var b strings.Builder
	b.WriteString("{")
	for _, k := range []string{"--color-background", "--color-widget-background", "--color-text-base", "--color-text-paragraph",
		"--color-text-highlight", "--color-text-subdue", "--color-separator", "--color-primary", "--color-negative", "--color-positive"} {
		fmt.Fprintf(&b, "%s:%s;", k, vars[k])
	}
	b.WriteString("}")
	return template.CSS(b.String())
}

// ConnectAccount is one row of the /connect page.
type ConnectAccount struct {
	ID, Name, Color, Email, ConnectedAt string
	Connected, NeedsReconnect           bool
}

// ConnectView is the data of the /connect page.
type ConnectView struct {
	Title, Lang, RedirectURL string
	Configured               bool
	Accounts                 []ConnectAccount
	Notice, Error            string
}

type connectData struct {
	ConnectView
	ThemeCSS template.CSS
}

// Connect writes the account-management page (always in the dark theme; it
// is an admin page, not a widget).
func Connect(w io.Writer, v ConnectView) error {
	return tpl.ExecuteTemplate(w, "connect", connectData{ConnectView: v, ThemeCSS: themeCSS("dark")})
}
