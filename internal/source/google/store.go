// Package google is the Source backed by the Google Calendar API, plus the
// OAuth pieces needed to connect accounts.
package google

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Credential is what we keep per connected account. Only the refresh token
// is secret; a leaked one reads every calendar of that account until it is
// revoked at myaccount.google.com/permissions.
type Credential struct {
	Email        string    `json:"email"`
	RefreshToken string    `json:"refresh_token"`
	ConnectedAt  time.Time `json:"connected_at"`
}

// Store persists credentials in a JSON file (mode 0600), written atomically.
type Store struct {
	path string
	mu   sync.Mutex
	data map[string]Credential // account id → credential
}

// OpenStore loads path if it exists. The parent directory must exist.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]Credential{}}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// first run
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

// Get returns the credential of an account.
func (s *Store) Get(id string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data[id]
	return c, ok
}

// IDs lists the connected account ids, sorted.
func (s *Store) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.data))
	for id := range s.data {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Put saves (or replaces) a credential and writes the file.
func (s *Store) Put(id string, c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = c
	return s.write()
}

// Delete forgets an account. It does not revoke the token at Google.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	return s.write()
}

// UpdateRefreshToken persists a rotated refresh token, if it changed.
func (s *Store) UpdateRefreshToken(id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data[id]
	if !ok || token == "" || c.RefreshToken == token {
		return nil
	}
	c.RefreshToken = token
	s.data[id] = c
	return s.write()
}

func (s *Store) write() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".tokens-*.json")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
