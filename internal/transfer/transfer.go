// Package transfer copies files to and from nodes with `tsh scp`. The
// command line is always an argv built by Args: user-typed paths are never
// handed to a local shell, and no argument can be read as a flag.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// Direction says which way a transfer goes.
type Direction int

// Directions.
const (
	Upload Direction = iota
	Download
)

func (d Direction) String() string {
	if d == Download {
		return "download"
	}
	return "upload"
}

// Spec is one transfer.
type Spec struct {
	Proxy string
	// Cluster is the node's Teleport cluster; empty means the selected one.
	Cluster  string
	Insecure bool
	Login    string
	Host     string
	// Direction is Upload (local to remote) or Download.
	Direction Direction
	// Local is the local path. "~/" is expanded; a relative path is taken
	// from the current directory.
	Local string
	// Remote is the remote path; empty means the login's home directory.
	Remote    string
	Recursive bool
}

// Target is login@host, as shown to the user.
func (s Spec) Target() string { return s.Login + "@" + s.Host }

// ValidateTarget checks a login and a hostname typed or picked by the
// user: neither may look like a flag or contain separators that would
// change how tsh splits login@host:path.
func ValidateTarget(login, host string) error {
	switch {
	case host == "":
		return errors.New("no server given")
	case strings.HasPrefix(host, "-"), strings.ContainsAny(host, "@:/ \t\r\n\x00"):
		return fmt.Errorf("invalid server name %q", host)
	case login == "":
		return errors.New("no login given")
	case strings.HasPrefix(login, "-"), strings.ContainsAny(login, "@:/ \t\r\n\x00"):
		return fmt.Errorf("invalid login %q", login)
	}
	return nil
}

// LocalPath turns the typed local path into a clean absolute path, so it
// can never start with "-". home expands a leading "~/".
func LocalPath(p, home string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("no local path given")
	}
	if strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("the local path contains control characters")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home == "" {
			return "", errors.New("cannot expand ~: no home directory")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("local path %q: %w", p, err)
	}
	// tsh scp reads any argument with a ':' as host:path (unverified for
	// paths that start with '/'; see docs/tsh-notes.md), so refuse it.
	if strings.Contains(abs, ":") {
		return "", fmt.Errorf("local path %q contains ':', which tsh scp would read as a remote path", abs)
	}
	return abs, nil
}

// RemotePath checks the remote path. It is passed inside login@host:path,
// so it cannot become a flag.
func RemotePath(p string) (string, error) {
	if strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("the remote path contains control characters")
	}
	return strings.TrimSpace(p), nil
}

// Args builds the `tsh scp` argv for s. home expands "~/" in the local
// path. The result is meant for exec, never for a shell.
func Args(s Spec, home string) ([]string, error) {
	if err := ValidateTarget(s.Login, s.Host); err != nil {
		return nil, err
	}
	local, err := LocalPath(s.Local, home)
	if err != nil {
		return nil, err
	}
	remote, err := RemotePath(s.Remote)
	if err != nil {
		return nil, err
	}
	args := []string{"scp"}
	if s.Proxy != "" {
		args = append(args, "--proxy="+s.Proxy)
	}
	if s.Cluster != "" {
		args = append(args, "--cluster="+s.Cluster)
	}
	if s.Insecure {
		args = append(args, "--insecure")
	}
	if s.Recursive {
		args = append(args, "--recursive")
	}
	rspec := s.Login + "@" + s.Host + ":" + remote
	if s.Direction == Download {
		return append(args, rspec, local), nil
	}
	return append(args, local, rspec), nil
}

// Execer runs tsh without a terminal (tsh.Client.Exec).
type Execer interface {
	Exec(ctx context.Context, args []string, o tsh.ExecOptions) (tsh.Result, error)
}

// Run performs the transfer and waits for it; cancel ctx to abort. An
// upload of a directory is made recursive automatically.
func Run(ctx context.Context, ex Execer, s Spec, home string) error {
	if s.Direction == Upload && !s.Recursive {
		if local, err := LocalPath(s.Local, home); err == nil {
			if st, err := os.Stat(local); err == nil && st.IsDir() {
				s.Recursive = true
			}
		}
	}
	args, err := Args(s, home)
	if err != nil {
		return err
	}
	res, err := ex.Exec(ctx, args, tsh.ExecOptions{MaxOutput: 64 << 10})
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s cancelled", s.Direction)
		}
		return err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimPrefix(tsh.CleanOutput(res.Stderr), "ERROR: ")
		if msg == "" {
			msg = fmt.Sprintf("tsh scp exited with status %d", res.ExitCode)
		}
		return errors.New(msg)
	}
	return nil
}

// Record is one finished transfer in the log.
type Record struct {
	Spec     Spec
	Start    time.Time
	Duration time.Duration
	Err      error
}

// String is the one-line form used in the dialog and the log file.
func (r Record) String() string {
	from, to := r.Spec.Local, r.Spec.Target()+":"+r.Spec.Remote
	if r.Spec.Direction == Download {
		from, to = to, from
	}
	result := "ok"
	if r.Err != nil {
		result = "error: " + r.Err.Error()
	}
	return fmt.Sprintf("%s %s %s -> %s (%s) %s", r.Start.Format(time.DateTime), r.Spec.Direction, from, to,
		r.Duration.Round(100*time.Millisecond), result)
}
