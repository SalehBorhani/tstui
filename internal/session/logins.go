package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// LoginMemory remembers the login last used for each server, so the login
// picker can preselect it. It is stored as JSON (mode 0600) in tstui's
// state directory; a missing or unreadable file starts empty.
//
// Methods must be called from one goroutine.
type LoginMemory struct {
	path   string
	logins map[string]map[string]string // proxy -> host -> login
}

// DefaultLoginsPath is $XDG_STATE_HOME/tstui/logins.json, falling back to
// ~/.local/state/tstui/logins.json.
func DefaultLoginsPath(getenv func(string) string) (string, error) {
	if x := getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "tstui", "logins.json"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("find state directory: %w", err)
		}
	}
	return filepath.Join(home, ".local", "state", "tstui", "logins.json"), nil
}

// LoadLogins reads the memory at path. An empty path keeps it in memory
// only. A missing file is not an error.
func LoadLogins(path string) (*LoginMemory, error) {
	m := &LoginMemory{path: path, logins: map[string]map[string]string{}}
	if path == "" {
		return m, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("read login memory: %w", err)
	}
	if err := json.Unmarshal(b, &m.logins); err != nil {
		m.logins = map[string]map[string]string{}
		return m, fmt.Errorf("parse login memory %s: %w", path, err)
	}
	return m, nil
}

// Get returns the login last used for host on proxy.
func (m *LoginMemory) Get(proxy, host string) string { return m.logins[proxy][host] }

// Set records login for host on proxy and saves the file.
func (m *LoginMemory) Set(proxy, host, login string) error {
	if m.logins[proxy] == nil {
		m.logins[proxy] = map[string]string{}
	}
	if m.logins[proxy][host] == login {
		return nil
	}
	m.logins[proxy][host] = login
	return m.save()
}

func (m *LoginMemory) save() error {
	if m.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(m.logins, "", "  ")
	if err != nil {
		return fmt.Errorf("encode login memory: %w", err)
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save login memory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".logins-*.json")
	if err != nil {
		return fmt.Errorf("save login memory: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save login memory: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save login memory: %w", err)
	}
	if err := os.Rename(tmp.Name(), m.path); err != nil {
		return fmt.Errorf("save login memory: %w", err)
	}
	return nil
}
