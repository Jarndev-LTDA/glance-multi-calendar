// Package config loads config.yml and the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	// Embed the IANA database so `timezone: America/Bahia` also works inside
	// a `FROM scratch` image that has no /usr/share/zoneinfo.
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

// Window controls how many days are shown and which hours the grid covers.
type Window struct {
	Days      int `yaml:"days"`
	StartHour int `yaml:"start_hour"` // floor: the grid never shows less than this...
	EndHour   int `yaml:"end_hour"`
	MinHour   int `yaml:"min_hour"` // ...but stretches up to these when an event needs it
	MaxHour   int `yaml:"max_hour"`
}

// Account is the label and colour of a connected account. Credentials never
// live here; they are stored in tokens.json inside DataDir.
type Account struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Color string `yaml:"color"`
}

// Config is the whole config.yml.
type Config struct {
	Listen          string        `yaml:"listen"`
	Timezone        string        `yaml:"timezone"`
	Refresh         time.Duration `yaml:"refresh"`
	DataDir         string        `yaml:"data_dir"`
	Window          Window        `yaml:"window"`
	Accounts        []Account     `yaml:"accounts"`
	IgnoreCalendars []string      `yaml:"ignore_calendars"`

	// Location is Timezone resolved; filled by Validate.
	Location *time.Location `yaml:"-"`
}

// Default is the configuration used when config.yml is absent or omits a key.
func Default() Config {
	return Config{
		Listen:   ":8080",
		Timezone: "UTC",
		Refresh:  5 * time.Minute,
		DataDir:  "/data",
		Window:   Window{Days: 7, StartHour: 7, EndHour: 22, MinHour: 0, MaxHour: 24},
	}
}

// Load reads path on top of Default. A missing file is not an error when
// allowMissing is set: the service then runs entirely on defaults.
func Load(path string, allowMissing bool) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && allowMissing:
		// defaults only
	default:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Validate checks ranges and resolves the time zone.
func (c *Config) Validate() error {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return fmt.Errorf("timezone %q: %w", c.Timezone, err)
	}
	c.Location = loc

	w := c.Window
	if w.Days < 1 || w.Days > 31 {
		return fmt.Errorf("window.days must be 1..31, got %d", w.Days)
	}
	if w.MinHour < 0 || w.MaxHour > 24 || w.MinHour > w.StartHour || w.StartHour >= w.EndHour || w.EndHour > w.MaxHour {
		return fmt.Errorf("window hours must satisfy 0 <= min_hour <= start_hour < end_hour <= max_hour <= 24, got min=%d start=%d end=%d max=%d",
			w.MinHour, w.StartHour, w.EndHour, w.MaxHour)
	}
	if c.Refresh < 30*time.Second {
		return fmt.Errorf("refresh must be at least 30s, got %s", c.Refresh)
	}
	seen := map[string]bool{}
	for i, a := range c.Accounts {
		if !idRe.MatchString(a.ID) {
			return fmt.Errorf("accounts[%d].id %q: use lowercase letters, digits, '-' or '_'", i, a.ID)
		}
		if seen[a.ID] {
			return fmt.Errorf("accounts[%d].id %q is duplicated", i, a.ID)
		}
		seen[a.ID] = true
		if a.Color != "" && !colorRe.MatchString(a.Color) {
			return fmt.Errorf("accounts[%d].color %q must be #rrggbb", i, a.Color)
		}
	}
	return nil
}
