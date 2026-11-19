package tsh

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Default timeouts for tsh calls. Network calls get more room than local ones.
const (
	LocalTimeout   = 10 * time.Second
	NetworkTimeout = 30 * time.Second
)

// CLI is the Client backed by the real tsh binary.
type CLI struct {
	runner Runner
	http   *http.Client
	home   string
	env    []string
	bin    string
	logger *slog.Logger
}

// Options configures NewCLI. Zero values give sensible defaults.
type Options struct {
	// Bin is the tsh binary; defaults to "tsh" looked up in PATH.
	Bin string
	// Home is the tsh home directory; defaults to $TELEPORT_HOME or ~/.tsh.
	Home string
	// Insecure skips TLS verification for /webapi/ping, like tsh --insecure.
	Insecure bool
	Logger   *slog.Logger
	// Runner overrides the exec runner (tests).
	Runner Runner
	// HTTPClient overrides the client used for /webapi/ping (tests).
	HTTPClient *http.Client
	// Environ is the base environment for child processes; defaults to
	// os.Environ(). Secret variables are always removed.
	Environ []string
}

// NewCLI builds a CLI client.
func NewCLI(o Options) *CLI {
	if o.Bin == "" {
		o.Bin = "tsh"
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	c := &CLI{
		runner: o.Runner,
		http:   o.HTTPClient,
		home:   o.Home,
		env:    SafeEnv(o.Environ),
		bin:    o.Bin,
		logger: o.Logger,
	}
	if c.logger == nil {
		c.logger = discardLogger()
	}
	if c.runner == nil {
		c.runner = ExecRunner{Bin: o.Bin, Logger: o.Logger}
	}
	if c.http == nil {
		c.http = newHTTPClient(o.Insecure)
	}
	if c.home == "" {
		c.home = DefaultHome(o.Environ)
	}
	return c
}

var _ Client = (*CLI)(nil)

func (c *CLI) run(ctx context.Context, timeout time.Duration, args ...string) (Result, error) {
	return c.runner.Run(ctx, Command{Args: args, Env: c.env, Timeout: timeout})
}

// runWithFallback runs args; if this tsh does not know optional (an older
// version), it runs again without that flag.
func (c *CLI) runWithFallback(ctx context.Context, timeout time.Duration, optional string, args ...string) (Result, error) {
	res, err := c.run(ctx, timeout, append(args, optional)...)
	if err != nil || res.ExitCode == 0 {
		return res, err
	}
	if strings.Contains(CleanOutput(res.Stderr), "unknown long flag '"+optional+"'") {
		return c.run(ctx, timeout, args...)
	}
	return res, nil
}

// Version implements Client.
func (c *CLI) Version(ctx context.Context) (VersionInfo, error) {
	res, err := c.runWithFallback(ctx, LocalTimeout, "--client", "version", "--format=json")
	if err != nil {
		return VersionInfo{}, err
	}
	if res.ExitCode != 0 {
		return VersionInfo{}, classifyError(res.Stderr)
	}
	return parseVersion(res.Stdout)
}

// Status implements Client. It uses `--client` so no network round trip is
// needed. tsh prints the profile JSON on stdout even when the certificate
// has expired, and exits 1 at the same time, so stdout is parsed first.
func (c *CLI) Status(ctx context.Context, proxy string) (Status, error) {
	args := []string{"status", "--format=json"}
	if proxy != "" {
		args = append(args, "--proxy="+proxy)
	}
	res, err := c.runWithFallback(ctx, LocalTimeout, "--client", args...)
	if err != nil {
		return Status{}, err
	}
	if len(strings.TrimSpace(string(res.Stdout))) > 0 {
		return parseStatus(res.Stdout)
	}
	if res.ExitCode == 0 {
		return Status{}, nil
	}
	if err := classifyError(res.Stderr); !errors.Is(err, ErrNotLoggedIn) {
		return Status{}, err
	}
	return Status{}, nil
}

// ListClusters implements Client.
func (c *CLI) ListClusters(ctx context.Context, proxy string) ([]Cluster, error) {
	args := []string{"clusters", "--format=json"}
	if proxy != "" {
		args = append(args, "--proxy="+proxy)
	}
	res, err := c.run(ctx, NetworkTimeout, args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, classifyError(res.Stderr)
	}
	return parseClusters(res.Stdout)
}

// ListServers implements Client.
func (c *CLI) ListServers(ctx context.Context, proxy, cluster string) ([]Server, error) {
	args := []string{"ls", "--format=json"}
	if proxy != "" {
		args = append(args, "--proxy="+proxy)
	}
	if cluster != "" {
		// One argument, so a name can never be read as another flag.
		args = append(args, "--cluster="+cluster)
	}
	res, err := c.run(ctx, NetworkTimeout, args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, classifyError(res.Stderr)
	}
	servers, err := parseServers(res.Stdout)
	for i := range servers {
		servers[i].Cluster = cluster
	}
	return servers, err
}

// ServerPing implements Client.
func (c *CLI) ServerPing(ctx context.Context, proxy string) (PingInfo, error) {
	hostPort, err := WebAddr(proxy)
	if err != nil {
		return PingInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, NetworkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostPort+"/webapi/ping", nil)
	if err != nil {
		return PingInfo{}, fmt.Errorf("build ping request for %s: %w", hostPort, err)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return PingInfo{}, fmt.Errorf("ping %s: %w", hostPort, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	latency := time.Since(start)
	if err != nil {
		return PingInfo{}, fmt.Errorf("ping %s: read body: %w", hostPort, err)
	}
	if resp.StatusCode != http.StatusOK {
		return PingInfo{}, fmt.Errorf("ping %s: HTTP %d", hostPort, resp.StatusCode)
	}
	info, err := parsePing(body)
	if err != nil {
		return PingInfo{}, fmt.Errorf("ping %s: %w", hostPort, err)
	}
	info.Latency = latency
	return info, nil
}

// Exec implements Client.
func (c *CLI) Exec(ctx context.Context, args []string, o ExecOptions) (Result, error) {
	if o.MaxOutput <= 0 {
		o.MaxOutput = DefaultMaxOutput
	}
	return c.runner.Run(ctx, Command{Args: args, Env: c.env, Timeout: o.Timeout, MaxOutput: o.MaxOutput, LogArgs: o.LogArgs})
}

// Profiles implements Client.
func (c *CLI) Profiles() ([]Profile, error) {
	return ReadProfiles(c.home)
}

// WebAddr turns a proxy address as tsh accepts it ("host", "host:port" or
// "host:webport,sshport") into the host:port of the web endpoint.
// The default web port is 443.
func WebAddr(proxy string) (string, error) {
	web, _, _ := strings.Cut(strings.TrimSpace(proxy), ",")
	web = strings.TrimPrefix(web, "https://")
	web = strings.TrimSuffix(web, "/")
	if web == "" {
		return "", fmt.Errorf("empty proxy address")
	}
	host, port, err := net.SplitHostPort(web)
	if err != nil {
		// No port given.
		host, port = web, "443"
	}
	if host == "" {
		return "", fmt.Errorf("invalid proxy address %q: no host", proxy)
	}
	return net.JoinHostPort(host, port), nil
}

func newHTTPClient(insecure bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user asked for --insecure
	}
	return &http.Client{Transport: tr}
}
