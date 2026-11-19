// Package tsh wraps the Teleport `tsh` binary. Every interaction with tsh
// in tstui goes through the Client interface so callers can be tested with
// the in-memory Fake or with the fake tsh script in testdata/fake-tsh.
package tsh

import (
	"context"
	"errors"
	"time"
)

// Client is the single entry point for everything tstui asks of Teleport.
// Implementations must be safe for concurrent use: the UI calls them from
// background goroutines.
type Client interface {
	// Version returns the local tsh client version. It does not contact a proxy.
	Version(ctx context.Context) (VersionInfo, error)
	// ServerPing calls https://<proxy>/webapi/ping. It needs no credentials.
	ServerPing(ctx context.Context, proxy string) (PingInfo, error)
	// Status reports the login state of the profile for proxy. A missing or
	// expired profile is not an error: it is reported in the Status fields.
	Status(ctx context.Context, proxy string) (Status, error)
	// ListClusters lists the root cluster of proxy and its leaf (trusted)
	// clusters, as `tsh clusters` reports them.
	ListClusters(ctx context.Context, proxy string) ([]Cluster, error)
	// ListServers lists the SSH nodes of cluster visible through proxy;
	// cluster "" means the profile's selected cluster. The servers'
	// Cluster field is set to cluster.
	ListServers(ctx context.Context, proxy, cluster string) ([]Server, error)
	// Profiles lists the proxies tsh has profiles for (~/.tsh/*.yaml).
	Profiles() ([]Profile, error)
	// StartPTY runs `tsh <args>` attached to a new pseudo-terminal, for
	// commands that talk to a user: login prompts, ssh sessions. The process
	// is killed when ctx is done. Never put secrets in args.
	StartPTY(ctx context.Context, args []string, o PTYOptions) (Process, error)
	// Exec runs `tsh <args>` without a terminal and waits for it: stdin is
	// empty, stdout and stderr are captured (bounded by o.MaxOutput). It is
	// for non-interactive commands such as `tsh scp` and `tsh ssh host cmd`.
	// A non-zero exit is reported in Result.ExitCode, not as an error.
	// Never put secrets in args.
	Exec(ctx context.Context, args []string, o ExecOptions) (Result, error)
	// Logout removes the profile and keys for proxy (and user, if given).
	Logout(ctx context.Context, proxy, user string) error
}

// DefaultMaxOutput bounds each of stdout and stderr captured by Exec.
const DefaultMaxOutput = 1 << 20

// ExecOptions configures Client.Exec.
type ExecOptions struct {
	// Timeout bounds the run on top of the context; zero means no bound.
	Timeout time.Duration
	// MaxOutput caps stdout and stderr each, in bytes; zero means
	// DefaultMaxOutput. Output past the cap is dropped and
	// Result.Truncated is set.
	MaxOutput int
	// LogArgs, when non-nil, is logged instead of args, e.g. to keep a
	// user's remote command out of the debug log.
	LogArgs []string
}

// VersionInfo is the output of `tsh version --client --format=json`.
type VersionInfo struct {
	Version string `json:"version"`
	Gitref  string `json:"gitref"`
	Runtime string `json:"runtime"`
}

// PingInfo is the subset of /webapi/ping that tstui shows or uses.
type PingInfo struct {
	ServerVersion    string
	MinClientVersion string
	ClusterName      string
	Edition          string
	// AuthType is the cluster's default auth type: local, oidc, saml or github.
	AuthType string
	// ConnectorName is the default connector name; empty for plain local auth.
	ConnectorName string
	// SecondFactor is e.g. "otp", "webauthn", "on", "optional", "off".
	SecondFactor      string
	PreferredLocalMFA string
	// Latency is the round-trip time of the ping request.
	Latency time.Duration
}

// Status is the login state of one proxy profile.
type Status struct {
	// LoggedIn is true when a profile exists for the proxy, expired or not.
	LoggedIn   bool
	ProfileURL string
	User       string
	Cluster    string
	Roles      []string
	Logins     []string
	ValidUntil time.Time
	Extensions []string
}

// Expired reports whether the certificate is past its validity at now.
func (s Status) Expired(now time.Time) bool {
	return s.LoggedIn && !s.ValidUntil.IsZero() && !now.Before(s.ValidUntil)
}

// Remaining is the certificate lifetime left at now, or 0 when expired.
func (s Status) Remaining(now time.Time) time.Duration {
	if !s.LoggedIn || s.ValidUntil.IsZero() {
		return 0
	}
	if d := s.ValidUntil.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Cluster is one Teleport cluster from `tsh clusters`: the root cluster
// of a proxy or a leaf cluster trusted by it.
type Cluster struct {
	Name string
	// Status is "online" or "offline".
	Status string
	// Type is "root" or "leaf".
	Type string
	// Selected marks the profile's selected cluster (the default for
	// commands without --cluster).
	Selected bool
}

// Root reports whether c is the proxy's root cluster.
func (c Cluster) Root() bool { return c.Type == "root" }

// Online reports whether tsh saw the cluster online; an unknown status
// counts as online, so the listing is tried.
func (c Cluster) Online() bool { return c.Status != "offline" }

// Server is one SSH node from `tsh ls`.
type Server struct {
	// Name is the node's resource name (usually a UUID).
	Name     string
	Hostname string
	// Cluster is the Teleport cluster the node belongs to; empty when the
	// listing did not say.
	Cluster string
	// Addr is empty for nodes connected through a reverse tunnel.
	Addr    string
	Version string
	// Labels holds static labels merged with the results of command labels.
	Labels map[string]string
	// Expires is when the node's last heartbeat runs out (metadata.expires);
	// zero when tsh did not say. A node keeps renewing it while it is
	// connected to the cluster.
	Expires time.Time
}

// Profile is one tsh profile file found in the tsh home directory.
type Profile struct {
	// Name is the file name without .yaml, normally the proxy host.
	Name         string
	WebProxyAddr string
	User         string
	Cluster      string
	MFAMode      string
	// Current is true for the profile named in ~/.tsh/current-profile.
	Current bool
}

var (
	// ErrNotFound means the tsh binary could not be found or executed.
	ErrNotFound = errors.New("tsh binary not found")
	// ErrNotLoggedIn means there is no profile, or its certificate has
	// expired, for the requested proxy.
	ErrNotLoggedIn = errors.New("not logged in")
	// ErrAccessDenied means Teleport rejected the request for lack of permission.
	ErrAccessDenied = errors.New("access denied")
	// ErrTimeout means a tsh command did not finish before its deadline.
	ErrTimeout = errors.New("tsh command timed out")
)
