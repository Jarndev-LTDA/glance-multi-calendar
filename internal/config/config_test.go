package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAreValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yml"), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Window.Days != 7 || c.Listen != ":8080" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml"), false); err == nil {
		t.Fatal("expected error for missing file when allowMissing=false")
	}
}

// Sanity check of the configured zone. Note: this does NOT prove the tzdata
// embedding (Go falls back to the system zoneinfo before the embedded copy);
// the real proof is running the scratch image, done in CI's docker build step
// and documented in README.
func TestTimezoneResolves(t *testing.T) {
	t.Setenv("ZONEINFO", filepath.Join(t.TempDir(), "empty.zip"))
	c := Default()
	c.Timezone = "America/Bahia"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	_, off := time.Date(2026, 9, 21, 12, 0, 0, 0, c.Location).Zone()
	if off != -3*3600 {
		t.Fatalf("America/Bahia offset = %d, want -10800", off)
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(p, []byte(`
timezone: America/Bahia
refresh: 2m
window: {days: 5, start_hour: 8, end_hour: 20}
accounts:
  - {id: pessoal, name: Pessoal, color: "#7aa2f7"}
  - {id: trabalho, name: Trabalho, color: "#9ece6a"}
`), 0o600)
	c, err := Load(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Refresh != 2*time.Minute || c.Window.Days != 5 || c.Window.MaxHour != 24 || len(c.Accounts) != 2 {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Config){
		"bad tz":       func(c *Config) { c.Timezone = "Mars/Olympus" },
		"days 0":       func(c *Config) { c.Window.Days = 0 },
		"start>=end":   func(c *Config) { c.Window.StartHour, c.Window.EndHour = 10, 10 },
		"min>start":    func(c *Config) { c.Window.MinHour = 9 },
		"max>24":       func(c *Config) { c.Window.MaxHour = 25 },
		"refresh":      func(c *Config) { c.Refresh = time.Second },
		"bad color":    func(c *Config) { c.Accounts = []Account{{ID: "a", Color: "blue"}} },
		"bad id":       func(c *Config) { c.Accounts = []Account{{ID: "Pessoal"}} },
		"duplicate id": func(c *Config) { c.Accounts = []Account{{ID: "a"}, {ID: "a"}} },
	}
	for name, mutate := range cases {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		} else if strings.Contains(err.Error(), "%!") {
			t.Errorf("%s: malformed message %q", name, err)
		}
	}
}
