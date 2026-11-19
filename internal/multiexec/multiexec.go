// Package multiexec runs one command on many nodes with
// `tsh ssh login@host <command>`, a bounded number at a time, each with a
// timeout. The command is a single argv element: it is never given to a
// local shell (the remote login shell runs it, as with ssh).
package multiexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// Defaults.
const (
	DefaultConcurrency = 5
	DefaultTimeout     = 60 * time.Second
	// MaxOutput bounds what is kept of each host's stdout and of its stderr.
	MaxOutput = 256 << 10
)

// Target is one node to run on.
type Target struct {
	Proxy string
	// Cluster is the node's Teleport cluster; empty means the selected one.
	Cluster string
	Login   string
	Host    string
}

// Name is the host, qualified with its cluster when it has one, as the
// audit log and the results show it.
func (t Target) Name() string {
	if t.Cluster == "" {
		return t.Host
	}
	return t.Cluster + "/" + t.Host
}

// Status is the state of one host's run.
type Status int

// Statuses.
const (
	Pending Status = iota
	Running
	// OK: exit status 0.
	OK
	// Failed: the command or tsh exited non-zero.
	Failed
	// TimedOut: killed after Options.Timeout.
	TimedOut
	// Cancelled: the whole run was cancelled first.
	Cancelled
	// Error: tsh could not be run at all.
	Error
)

func (s Status) String() string {
	return [...]string{"pending", "running", "ok", "failed", "timeout", "cancelled", "error"}[s]
}

// Done reports whether the host has finished, one way or another.
func (s Status) Done() bool { return s >= OK }

// Result is one host's outcome.
type Result struct {
	Target    Target
	Status    Status
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	Err       error
	Duration  time.Duration
}

// Options configures Run.
type Options struct {
	// Concurrency is how many hosts run at once; <= 0 means 5.
	Concurrency int
	// Timeout bounds each host; <= 0 means 60s.
	Timeout  time.Duration
	Insecure bool
	// OnUpdate, if set, is called from Run's goroutines whenever host i
	// changes status. It must not block for long.
	OnUpdate func(i int, r Result)
}

// Execer runs tsh without a terminal (tsh.Client.Exec).
type Execer interface {
	Exec(ctx context.Context, args []string, o tsh.ExecOptions) (tsh.Result, error)
}

// ValidateCommand checks the user's command.
func ValidateCommand(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == "":
		return "", errors.New("no command given")
	case strings.ContainsRune(cmd, 0):
		return "", errors.New("the command contains a NUL byte")
	case strings.HasPrefix(cmd, "-"):
		// tsh would read it as a flag.
		return "", errors.New("the command must not start with '-'")
	}
	return cmd, nil
}

// Args is the tsh argv for running command on t.
func Args(t Target, command string, insecure bool) ([]string, error) {
	switch {
	case t.Host == "", strings.HasPrefix(t.Host, "-"), strings.ContainsAny(t.Host, "@ \t\r\n\x00"):
		return nil, fmt.Errorf("invalid server name %q", t.Host)
	case t.Login == "", strings.HasPrefix(t.Login, "-"), strings.ContainsAny(t.Login, "@ \t\r\n\x00"):
		return nil, fmt.Errorf("invalid login %q", t.Login)
	}
	command, err := ValidateCommand(command)
	if err != nil {
		return nil, err
	}
	args := []string{"ssh"}
	if t.Proxy != "" {
		args = append(args, "--proxy="+t.Proxy)
	}
	if t.Cluster != "" {
		args = append(args, "--cluster="+t.Cluster)
	}
	if insecure {
		args = append(args, "--insecure")
	}
	return append(args, t.Login+"@"+t.Host, command), nil
}

// ForEach calls fn(ctx, i) for i in [0, n), at most limit at a time, and
// returns when every call has returned. Once ctx is done no new call
// starts; skipped(i) is called instead.
func ForEach(ctx context.Context, n, limit int, fn func(ctx context.Context, i int), skipped func(i int)) {
	if limit <= 0 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			for j := i; j < n; j++ {
				skipped(j)
			}
			break
		}
		wg.Add(1)
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			fn(ctx, i)
		}(i)
	}
	wg.Wait()
}

// Run runs command on every target and returns their results in target
// order. Cancel ctx to stop: running hosts are killed, pending ones are
// marked Cancelled.
func Run(ctx context.Context, ex Execer, targets []Target, command string, o Options) []Result {
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.OnUpdate == nil {
		o.OnUpdate = func(int, Result) {}
	}
	results := make([]Result, len(targets))
	for i, t := range targets {
		results[i] = Result{Target: t, Status: Pending, ExitCode: -1}
	}
	ForEach(ctx, len(targets), o.Concurrency, func(ctx context.Context, i int) {
		r := results[i]
		r.Status = Running
		o.OnUpdate(i, r)
		r = runOne(ctx, ex, r.Target, command, o)
		results[i] = r
		o.OnUpdate(i, r)
	}, func(i int) {
		results[i].Status = Cancelled
		results[i].Err = context.Canceled
		o.OnUpdate(i, results[i])
	})
	return results
}

func runOne(ctx context.Context, ex Execer, t Target, command string, o Options) Result {
	r := Result{Target: t, ExitCode: -1}
	args, err := Args(t, command, o.Insecure)
	if err != nil {
		r.Status, r.Err = Error, err
		return r
	}
	// The remote command stays out of the debug log; the audit log has it.
	logArgs := append(append([]string(nil), args[:len(args)-1]...), "[command]")
	start := time.Now()
	res, err := ex.Exec(ctx, args, tsh.ExecOptions{Timeout: o.Timeout, MaxOutput: MaxOutput, LogArgs: logArgs})
	r.Duration = time.Since(start)
	r.Stdout, r.Stderr, r.Truncated, r.ExitCode = res.Stdout, res.Stderr, res.Truncated, res.ExitCode
	switch {
	case ctx.Err() != nil:
		r.Status, r.Err = Cancelled, context.Canceled
	case errors.Is(err, tsh.ErrTimeout):
		r.Status, r.Err = TimedOut, fmt.Errorf("timed out after %s", o.Timeout)
	case err != nil:
		r.Status, r.Err = Error, err
	case res.ExitCode == 0:
		r.Status = OK
	default:
		r.Status = Failed
		if msg := tsh.CleanOutput(res.Stderr); strings.HasPrefix(msg, "ERROR:") {
			r.Err = errors.New(strings.TrimSpace(strings.TrimPrefix(lastLine(msg), "ERROR:")))
		}
	}
	return r
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "ERROR:") {
			return strings.TrimSpace(lines[i])
		}
	}
	return lines[0]
}
