// Package portfwd runs local port forwards through Teleport with
// `tsh ssh -N --forward=[bind:]port:host:port login@node`, one tsh process
// per forward. tsh has no other way to forward to an arbitrary host:port
// behind an SSH node: `tsh proxy ssh` is an OpenSSH ProxyCommand, and
// `tsh proxy app|db` only reach registered apps and databases.
package portfwd

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultBind is the local address forwards listen on unless told
// otherwise: loopback only, so other machines cannot use the tunnel.
const DefaultBind = "127.0.0.1"

// Spec is one forward: BindAddr:LocalPort on this machine to
// RemoteHost:RemotePort as seen from Host.
type Spec struct {
	// Name is an optional label, e.g. the favorite's name.
	Name  string
	Proxy string
	// Cluster is the node's Teleport cluster; empty means the selected one.
	Cluster    string
	Insecure   bool
	Login      string
	Host       string
	BindAddr   string
	LocalPort  int
	RemoteHost string
	RemotePort int
}

// ParseForward reads "[bind:]localport:remotehost:remoteport", the format
// of ssh -L and tsh ssh -L. "localport:remoteport" is short for
// "localport:localhost:remoteport".
func ParseForward(s string) (bind string, lport int, rhost string, rport int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	switch len(parts) {
	case 2:
		parts = []string{DefaultBind, parts[0], "localhost", parts[1]}
	case 3:
		parts = append([]string{DefaultBind}, parts...)
	case 4:
	default:
		return "", 0, "", 0, fmt.Errorf("forward %q: want [bind:]localport:host:port", s)
	}
	if lport, err = parsePort(parts[1]); err != nil {
		return "", 0, "", 0, fmt.Errorf("forward %q: local %w", s, err)
	}
	if rport, err = parsePort(parts[3]); err != nil {
		return "", 0, "", 0, fmt.Errorf("forward %q: remote %w", s, err)
	}
	return parts[0], lport, parts[2], rport, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %q must be a number from 1 to 65535", s)
	}
	return n, nil
}

// Normalize fills in defaults (bind 127.0.0.1, remote host localhost).
func (s Spec) Normalize() Spec {
	if s.BindAddr == "" {
		s.BindAddr = DefaultBind
	}
	if s.RemoteHost == "" {
		s.RemoteHost = "localhost"
	}
	return s
}

// Validate checks a normalized spec. Everything in it may have been typed
// by the user, so nothing may look like a flag or carry a separator.
func (s Spec) Validate() error {
	switch {
	case s.Host == "":
		return errors.New("no server given")
	case strings.HasPrefix(s.Host, "-"), strings.ContainsAny(s.Host, "@: \t\r\n\x00"):
		return fmt.Errorf("invalid server name %q", s.Host)
	case s.Login == "":
		return errors.New("no login given")
	case strings.HasPrefix(s.Login, "-"), strings.ContainsAny(s.Login, "@: \t\r\n\x00"):
		return fmt.Errorf("invalid login %q", s.Login)
	case s.LocalPort < 1 || s.LocalPort > 65535:
		return fmt.Errorf("local port %d must be from 1 to 65535", s.LocalPort)
	case s.RemotePort < 1 || s.RemotePort > 65535:
		return fmt.Errorf("remote port %d must be from 1 to 65535", s.RemotePort)
	case s.RemoteHost == "" || strings.HasPrefix(s.RemoteHost, "-") || strings.ContainsAny(s.RemoteHost, ": \t\r\n\x00/"):
		return fmt.Errorf("invalid remote host %q", s.RemoteHost)
	}
	if ip := net.ParseIP(s.BindAddr); s.BindAddr != "localhost" && (ip == nil || ip.To4() == nil) {
		return fmt.Errorf("bind address %q must be an IPv4 address or localhost", s.BindAddr)
	}
	return nil
}

// ForwardSpec is the -L value: bind:localport:remotehost:remoteport.
func (s Spec) ForwardSpec() string {
	return fmt.Sprintf("%s:%d:%s:%d", s.BindAddr, s.LocalPort, s.RemoteHost, s.RemotePort)
}

// Local is bind:port.
func (s Spec) Local() string { return net.JoinHostPort(s.BindAddr, strconv.Itoa(s.LocalPort)) }

// Remote is remotehost:port via login@host.
func (s Spec) Remote() string {
	return fmt.Sprintf("%s:%d via %s@%s", s.RemoteHost, s.RemotePort, s.Login, s.Host)
}

// Args is the tsh argv for s, which must be normalized and valid. It is
// for exec, never for a shell.
func Args(s Spec) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	args := []string{"ssh"}
	if s.Proxy != "" {
		args = append(args, "--proxy="+s.Proxy)
	}
	if s.Cluster != "" {
		args = append(args, "--cluster="+s.Cluster)
	}
	if s.Insecure {
		args = append(args, "--insecure")
	}
	return append(args, "-N", "--forward="+s.ForwardSpec(), s.Login+"@"+s.Host), nil
}

// ErrConflict means the local port is already taken.
var ErrConflict = errors.New("local port in use")

// Prober checks whether a local address can be listened on.
type Prober interface {
	// Free returns nil when bind:port is free, and an error saying why not.
	Free(bind string, port int) error
}

// NetProber probes by listening on the address and closing it at once.
type NetProber struct{}

// Free implements Prober.
func (NetProber) Free(bind string, port int) error {
	l, err := net.Listen("tcp4", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("%w: %s:%d: %w", ErrConflict, bind, port, err)
	}
	return l.Close()
}

// overlaps reports whether two binds on the same port collide: equal
// addresses, or either is the wildcard.
func overlaps(a, b string) bool {
	norm := func(s string) string {
		if s == "localhost" {
			return "127.0.0.1"
		}
		return s
	}
	a, b = norm(a), norm(b)
	return a == b || a == "0.0.0.0" || b == "0.0.0.0"
}

// Conflict checks s against forwards tstui already runs, then asks p
// whether anything else holds the port. Both checks are needed: a forward
// that is still starting has not bound its port yet.
func Conflict(running []Spec, s Spec, p Prober) error {
	for _, r := range running {
		if r.LocalPort == s.LocalPort && overlaps(r.BindAddr, s.BindAddr) {
			return fmt.Errorf("%w: %s is already forwarded to %s", ErrConflict, s.Local(), r.Remote())
		}
	}
	if p != nil {
		return p.Free(s.BindAddr, s.LocalPort)
	}
	return nil
}
