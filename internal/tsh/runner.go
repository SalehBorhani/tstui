package tsh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Command is one invocation of the tsh binary.
type Command struct {
	Args []string
	// Env replaces the child's environment when non-nil. See SafeEnv.
	Env   []string
	Stdin io.Reader
	// Timeout bounds the run on top of the context. Zero means no extra bound.
	Timeout time.Duration
	// MaxOutput caps stdout and stderr each; zero means no cap.
	MaxOutput int
	// LogArgs, when non-nil, is logged instead of Args.
	LogArgs []string
}

// Result is the captured outcome of a Command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
	// Truncated is set when output went past Command.MaxOutput.
	Truncated bool
}

// Runner runs tsh commands. ExecRunner is the real implementation; tests
// can substitute their own.
type Runner interface {
	Run(ctx context.Context, cmd Command) (Result, error)
}

// ExecRunner runs a binary with os/exec and logs each run without secrets.
// A non-zero exit code is reported in Result.ExitCode, not as an error:
// tsh often writes useful JSON and exits 1 at the same time.
type ExecRunner struct {
	Bin    string
	Logger *slog.Logger
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, c Command) (Result, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, r.Bin, c.Args...)
	cmd.Env = c.Env
	cmd.Stdin = c.Stdin
	stdout, stderr := &cappedBuffer{max: c.MaxOutput}, &cappedBuffer{max: c.MaxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	// Stop waiting for output pipes shortly after cancellation even if some
	// process we could not kill still holds them.
	cmd.WaitDelay = time.Second

	start := time.Now()
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Duration: time.Since(start),
		Truncated: stdout.dropped || stderr.dropped}

	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("%w after %s: tsh %s", ErrTimeout, res.Duration.Round(time.Millisecond), firstArg(c.Args))
		res.ExitCode = -1
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		err = nil
	case err != nil:
		err = fmt.Errorf("%w (%s): %w", ErrNotFound, r.Bin, err)
		res.ExitCode = -1
	}

	// Output is never logged: stdout/stderr could echo anything. Callers
	// turn stderr into user-facing errors via classifyError instead.
	logArgs := c.Args
	if c.LogArgs != nil {
		logArgs = c.LogArgs
	}
	r.logger().Debug("tsh run",
		"args", RedactArgs(logArgs),
		"exit", res.ExitCode,
		"duration", res.Duration.Round(time.Millisecond),
		"stderr_bytes", len(res.Stderr),
		"err", err,
	)
	return res, err
}

// cappedBuffer keeps the first max bytes written to it (all of them when
// max is 0) and silently drops the rest, so a chatty command cannot use up
// memory. It never fails a write: the child must not get EPIPE.
type cappedBuffer struct {
	buf     bytes.Buffer
	max     int
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return b.buf.Write(p)
	}
	room := b.max - b.buf.Len()
	if len(p) > room {
		b.dropped = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

func (r ExecRunner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// secretFlagHints are substrings of flag names whose values must never be logged.
var secretFlagHints = []string{"password", "passwd", "secret", "token", "otp", "pin"}

// RedactArgs returns a copy of args with the value of any secret-looking
// flag replaced. tstui never passes secrets as arguments, so this is a
// second line of defence for logs.
//
// A secret flag without "=" takes the next argument as its value, whatever
// it looks like (Go's flag package and kingpin both do), so that argument
// is redacted even when it starts with "-"; it is then still checked as a
// flag itself. `--password-stdin --mfa-token 123456` thus redacts both
// arguments after the first, at the cost of over-redacting sometimes.
// Flags ending in "-stdin" are booleans and take no value.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	redactNext := false
	for i, a := range args {
		out[i] = a
		if redactNext {
			out[i] = Redacted
			redactNext = false
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case !isSecretFlag(name):
		case hasValue:
			out[i] = a[:strings.Index(a, "=")+1] + Redacted
		case !strings.HasSuffix(name, "-stdin"):
			redactNext = true
		}
	}
	return out
}

// Redacted replaces secret values in logs.
const Redacted = "[REDACTED]"

func isSecretFlag(name string) bool {
	name = strings.ToLower(name)
	for _, h := range secretFlagHints {
		if strings.Contains(name, h) {
			return true
		}
	}
	return false
}

// secretEnvVars are removed from the environment of every tsh child process.
var secretEnvVars = []string{"TSTUI_PASSWORD", "TSTUI_MFA"}

// SafeEnv returns environ without tstui's secret variables, so credentials
// given through the environment never reach a child process.
func SafeEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		secret := false
		for _, s := range secretEnvVars {
			if name == s {
				secret = true
				break
			}
		}
		if !secret {
			out = append(out, kv)
		}
	}
	return out
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
