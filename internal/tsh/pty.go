package tsh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// Process is a tsh child process attached to a pseudo-terminal. Reads
// return what tsh writes to the terminal (stdout and stderr are merged);
// writes are typed into it.
type Process interface {
	io.Reader
	io.Writer
	// Resize changes the terminal size seen by the process.
	Resize(rows, cols uint16) error
	// Done is closed when the process has exited.
	Done() <-chan struct{}
	// ExitCode is the exit status after Done is closed: -1 when the process
	// was killed by a signal or could not be waited for.
	ExitCode() int
	// Kill kills the process and everything it started.
	Kill() error
	// Close releases the terminal and kills the process if it still runs.
	Close() error
}

// PTYOptions configures StartPTY.
type PTYOptions struct {
	// Rows and Cols are the initial terminal size; zero means 24x80.
	Rows, Cols uint16
	// NoEcho turns terminal echo off before the process starts, so typed
	// input (an OTP, say) is not written back into the output. Programs that
	// manage echo themselves may still turn it on.
	NoEcho bool
	// Env adds NAME=value entries to the child's environment, replacing
	// variables of the same name (TERM for an embedded terminal, say).
	Env []string
	// Stderr, when set, receives the child's stderr through a pipe instead
	// of the terminal, so its error messages can be told apart from the
	// program's output. The writer gets the raw bytes: no newline
	// translation, as the terminal's line discipline is bypassed. Exit is
	// reported (Done) only after stderr is drained or 2 s after the
	// process ends, whichever is first.
	Stderr io.Writer
}

// ErrPTYUnsupported means this platform cannot run tsh in a pseudo-terminal.
var ErrPTYUnsupported = errors.New("pseudo-terminals are not supported on this platform")

// StartPTY implements Client.
func (c *CLI) StartPTY(ctx context.Context, args []string, o PTYOptions) (Process, error) {
	if o.Rows == 0 || o.Cols == 0 {
		o.Rows, o.Cols = 24, 80
	}
	p, err := startPTY(ctx, c.bin, args, c.env, o)
	if err != nil {
		return nil, fmt.Errorf("start tsh %s in a terminal: %w", firstArg(args), err)
	}
	logger := c.logger
	start := time.Now()
	go func() {
		<-p.Done()
		logger.Debug("tsh pty run",
			"args", RedactArgs(args),
			"exit", p.ExitCode(),
			"duration", time.Since(start).Round(time.Millisecond),
		)
	}()
	return p, nil
}

// StartCommandPTY runs any program in a new pseudo-terminal, the same way
// StartPTY runs tsh: a new session with the terminal as its controlling
// terminal, killed as a process group. It is for the --demo-shell mode and
// tests; tsh itself must go through Client.StartPTY. env is the complete
// environment and is passed through SafeEnv.
func StartCommandPTY(ctx context.Context, bin string, args, env []string, o PTYOptions) (Process, error) {
	if o.Rows == 0 || o.Cols == 0 {
		o.Rows, o.Cols = 24, 80
	}
	p, err := startPTY(ctx, bin, args, SafeEnv(env), o)
	if err != nil {
		return nil, fmt.Errorf("start %s in a terminal: %w", bin, err)
	}
	return p, nil
}

// mergeEnv returns env with the entries of extra added, replacing entries
// of the same name.
func mergeEnv(env, extra []string) []string {
	if len(extra) == 0 {
		return env
	}
	names := map[string]bool{}
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		names[name] = true
	}
	out := make([]string, 0, len(env)+len(extra))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !names[name] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// Logout implements Client. Logging out of a proxy that has no profile is
// not an error.
func (c *CLI) Logout(ctx context.Context, proxy, user string) error {
	args := []string{"logout"}
	if proxy != "" {
		args = append(args, "--proxy="+proxy)
	}
	if user != "" {
		args = append(args, "--user="+user)
	}
	res, err := c.run(ctx, NetworkTimeout, args...)
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return nil
	}
	if err := classifyError(res.Stderr); !errors.Is(err, ErrNotLoggedIn) {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
