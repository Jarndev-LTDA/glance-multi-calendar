package google

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.IDs()) != 0 {
		t.Fatal("fresh store should be empty")
	}
	if err := s.Put("pessoal", Credential{Email: "a@example.com", RefreshToken: "rt1", ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("trabalho", Credential{Email: "b@example.com", RefreshToken: "rt2"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}

	// reopen: survives a restart
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if ids := s2.IDs(); len(ids) != 2 || ids[0] != "pessoal" || ids[1] != "trabalho" {
		t.Fatalf("ids = %v", ids)
	}
	if c, ok := s2.Get("trabalho"); !ok || c.RefreshToken != "rt2" || c.Email != "b@example.com" {
		t.Fatalf("get = %+v %v", c, ok)
	}
	if err := s2.UpdateRefreshToken("trabalho", "rt2-rotated"); err != nil {
		t.Fatal(err)
	}
	if err := s2.UpdateRefreshToken("nope", "x"); err != nil {
		t.Fatal("unknown id must be a no-op")
	}
	if err := s2.Delete("pessoal"); err != nil {
		t.Fatal(err)
	}
	s3, _ := OpenStore(path)
	if ids := s3.IDs(); len(ids) != 1 || ids[0] != "trabalho" {
		t.Fatalf("after delete ids = %v", ids)
	}
	if c, _ := s3.Get("trabalho"); c.RefreshToken != "rt2-rotated" {
		t.Errorf("rotation not persisted: %+v", c)
	}
}

func TestStoreCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := OpenStore(path); err == nil {
		t.Fatal("expected parse error")
	}
}
