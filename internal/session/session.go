// Package session persists the login session (JWT + metadata) in the user's
// config dir so that research/inspect/borrow can reuse it without logging in
// every time. The file has mode 0600 since the JWT acts as a bearer token.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Session is the persisted login state.
type Session struct {
	JWT       string    `json:"jwt"`
	LoginID   string    `json:"login_id"`
	User      string    `json:"user"`
	UserName  string    `json:"user_name"`
	Display   string    `json:"display_name"`
	ViewID    string    `json:"view_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Expired reports whether the JWT expiry time has passed (with 5 min skew).
func (s *Session) Expired() bool {
	return time.Now().Add(5 * time.Minute).After(s.ExpiresAt)
}

func dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "librarian")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "session.json"), nil
}

// Save writes the session with mode 0600.
func Save(s *Session) error {
	p, err := path()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o600)
}

// Load reads the session. It returns an error if none exists.
func Load() (*Session, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("no saved session (run `librarian auth login` first): %w", err)
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("corrupt session file %s: %w", p, err)
	}
	return &s, nil
}

// Clear deletes the saved session.
func Clear() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path returns the session file path (for display).
func Path() string {
	p, err := path()
	if err != nil {
		return "(unknown)"
	}
	return p
}
