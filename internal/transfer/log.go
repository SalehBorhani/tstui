package transfer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// maxLogInMemory is how many records Log keeps for display.
const maxLogInMemory = 50

// Log records finished transfers in memory and, when it has a path,
// appends them to a file (mode 0600). It is safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	path    string
	records []Record
}

// NewLog returns a log that also appends to path; "" keeps it in memory.
func NewLog(path string) *Log { return &Log{path: path} }

// Add records r. A failure to write the file is returned, and r is still
// kept in memory.
func (l *Log) Add(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
	if len(l.records) > maxLogInMemory {
		l.records = l.records[len(l.records)-maxLogInMemory:]
	}
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("transfer log: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("transfer log: %w", err)
	}
	_, werr := fmt.Fprintln(f, strings.ReplaceAll(r.String(), "\n", " "))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("transfer log: %w", werr)
	}
	return nil
}

// Records returns the transfers kept in memory, oldest first.
func (l *Log) Records() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Record(nil), l.records...)
}

// maxCompletions bounds CompleteLocal's answer.
const maxCompletions = 50

// CompleteLocal lists the local paths that input could become: entries of
// its directory whose names start with its last element. Directories end
// in "/". Hidden entries are offered only when the typed name starts with
// '.'. "~/" is expanded with home for reading but kept in the answers.
func CompleteLocal(input, home string) []string {
	dirPart, prefix := "", input
	if i := strings.LastIndex(input, "/"); i >= 0 {
		dirPart, prefix = input[:i+1], input[i+1:]
	}
	readDir := dirPart
	switch {
	case input == "~":
		return []string{"~/"}
	case strings.HasPrefix(dirPart, "~/"):
		if home == "" {
			return nil
		}
		readDir = filepath.Join(home, strings.TrimPrefix(dirPart, "~/"))
	case dirPart == "":
		readDir = "."
	}
	entries, err := os.ReadDir(readDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		c := dirPart + name
		if e.IsDir() {
			c += "/"
		} else if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(readDir, name)); err == nil && st.IsDir() {
				c += "/"
			}
		}
		out = append(out, c)
		if len(out) == maxCompletions {
			break
		}
	}
	sort.Strings(out)
	return out
}
