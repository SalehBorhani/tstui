package multiexec

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AuditEntry is one line of the audit log (JSON). It holds what was run
// where and when; tstui never has secrets to put in it, and output is not
// recorded.
type AuditEntry struct {
	Time    time.Time `json:"time"`
	Event   string    `json:"event"` // "start" or "finish"
	Proxy   string    `json:"proxy"`
	Login   string    `json:"login"`
	Hosts   []string  `json:"hosts"`
	Command string    `json:"command"`
	Risky   []string  `json:"risky,omitempty"`
	// Results maps host to its status and exit code, on "finish".
	Results map[string]string `json:"results,omitempty"`
}

// Audit appends entries to a local JSON-lines file (mode 0600).
type Audit struct{ path string }

// NewAudit writes to path; "" disables the audit log.
func NewAudit(path string) *Audit { return &Audit{path: path} }

// Path is the file the audit log goes to.
func (a *Audit) Path() string { return a.path }

// Write appends e.
func (a *Audit) Write(e AuditEntry) error {
	if a == nil || a.path == "" {
		return nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("audit log: %w", werr)
	}
	return nil
}

// FinishEntry summarises results for the audit log.
func FinishEntry(start AuditEntry, results []Result, now time.Time) AuditEntry {
	e := start
	e.Time, e.Event, e.Results = now, "finish", map[string]string{}
	for _, r := range results {
		e.Results[r.Target.Name()] = fmt.Sprintf("%s exit=%d", r.Status, r.ExitCode)
	}
	return e
}

// Export writes a plain-text report of a run.
func Export(w io.Writer, command string, when time.Time, results []Result) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# tstui multi-exec %s\n# command: %s\n# hosts: %d\n", when.Format(time.RFC3339), command, len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "\n=== %s@%s  status=%s exit=%d duration=%s", r.Target.Login, r.Target.Name(), r.Status, r.ExitCode,
			r.Duration.Round(time.Millisecond))
		if r.Err != nil {
			fmt.Fprintf(&b, "  error=%s", r.Err)
		}
		b.WriteString("\n")
		if len(r.Stdout) > 0 {
			b.WriteString("--- stdout\n")
			b.Write(r.Stdout)
			if !strings.HasSuffix(string(r.Stdout), "\n") {
				b.WriteString("\n")
			}
		}
		if len(r.Stderr) > 0 {
			b.WriteString("--- stderr\n")
			b.Write(r.Stderr)
			if !strings.HasSuffix(string(r.Stderr), "\n") {
				b.WriteString("\n")
			}
		}
		if r.Truncated {
			b.WriteString("--- (output truncated)\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
