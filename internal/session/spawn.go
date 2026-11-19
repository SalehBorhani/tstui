package session

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/root-ali/tstui/internal/tsh"
)

// TermEnv is what every embedded program is told about its terminal.
var TermEnv = []string{"TERM=xterm-256color", "COLORTERM=truecolor"}

// Spec says what a session connects to.
type Spec struct {
	Proxy string
	// Cluster is the Teleport cluster of the node; empty means the
	// profile's selected cluster.
	Cluster string
	Host    string
	Login   string
	// Name is the tab label; it defaults to Host.
	Name string
}

// Key identifies the server: opening a server that already has a session
// focuses that session.
func (s Spec) Key() string { return s.Proxy + "\x00" + s.Cluster + "\x00" + s.Host }

func (s Spec) label() string {
	if s.Name != "" {
		return s.Name
	}
	return s.Host
}

// Spawner starts the program of a session in a pseudo-terminal.
type Spawner interface {
	Spawn(ctx context.Context, spec Spec, o tsh.PTYOptions) (tsh.Process, error)
	// SeparateStderr reports whether the program's stderr should be read
	// apart from the terminal, to explain failures (tsh: yes, a shell: no,
	// since it writes its prompt there).
	SeparateStderr() bool
}

// TshSpawner runs `tsh ssh login@host --proxy=...` through c.
type TshSpawner struct {
	Client   tsh.Client
	Insecure bool
}

// Spawn implements Spawner.
func (t TshSpawner) Spawn(ctx context.Context, spec Spec, o tsh.PTYOptions) (tsh.Process, error) {
	if spec.Host == "" || strings.HasPrefix(spec.Host, "-") || strings.HasPrefix(spec.Login, "-") {
		return nil, fmt.Errorf("invalid ssh target %q", spec.Login+"@"+spec.Host)
	}
	return t.Client.StartPTY(ctx, SSHArgs(spec, t.Insecure), o)
}

// SeparateStderr implements Spawner.
func (TshSpawner) SeparateStderr() bool { return true }

// SSHArgs is the tsh command line for spec. The host is the node's
// hostname; tsh resolves it on the proxy.
func SSHArgs(spec Spec, insecure bool) []string {
	args := []string{"ssh"}
	if spec.Proxy != "" {
		args = append(args, "--proxy="+spec.Proxy)
	}
	if spec.Cluster != "" {
		args = append(args, "--cluster="+spec.Cluster)
	}
	if insecure {
		args = append(args, "--insecure")
	}
	target := spec.Host
	if spec.Login != "" {
		target = spec.Login + "@" + spec.Host
	}
	// Spawn refuses hosts and logins that start with "-", so the target
	// can never be read as a flag.
	return append(args, target)
}

// ShellSpawner runs a local shell instead of tsh (the hidden --demo-shell
// mode), so the terminal can be tried without a cluster.
type ShellSpawner struct {
	// Shell is the program; empty means bash from PATH, else /bin/sh.
	Shell string
	// Environ is the base environment (os.Environ()).
	Environ []string
}

// DefaultShell is bash if it is installed, else /bin/sh.
func DefaultShell() string {
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/sh"
}

// Spawn implements Spawner.
func (s ShellSpawner) Spawn(ctx context.Context, _ Spec, o tsh.PTYOptions) (tsh.Process, error) {
	shell := s.Shell
	if shell == "" {
		shell = DefaultShell()
	}
	return tsh.StartCommandPTY(ctx, shell, nil, s.Environ, o)
}

// SeparateStderr implements Spawner.
func (ShellSpawner) SeparateStderr() bool { return false }
