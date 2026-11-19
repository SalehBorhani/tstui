// Package config loads, validates and saves tstui's configuration file
// (~/.config/tstui/config.yaml by default).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/root-ali/tstui/internal/portfwd"
)

// FileMode is the permission tstui uses for the config file. It may hold
// user names and connector names, so it is private to the user.
const FileMode fs.FileMode = 0o600

// DefaultFocusKey switches focus between the server list and the terminal.
const DefaultFocusKey = "Ctrl-B"

// DefaultSkin is used when no skin is configured.
const DefaultSkin = "default"

// Config is the content of config.yaml.
type Config struct {
	// DefaultProxy is the Name of the proxy selected at start-up.
	DefaultProxy string  `yaml:"default_proxy,omitempty"`
	Proxies      []Proxy `yaml:"proxies,omitempty"`
	// Favorites maps a proxy name to the hostnames marked as favorite.
	Favorites   map[string][]string `yaml:"favorites,omitempty"`
	Skin        string              `yaml:"skin,omitempty"`
	Keybindings Keybindings         `yaml:"keybindings,omitempty"`
	Terminal    Terminal            `yaml:"terminal,omitempty"`
	// TshPrompts adds regular expressions that recognise tsh login prompts
	// and errors, keyed by kind (password, otp, otp_or_tap, tap, pin,
	// choice, key_tapped, sso_link, locked, connector_not_found,
	// wrong_credentials, tls, network). They are tried before the built-in
	// ones, case-insensitively, one output line at a time. Use them when a
	// tsh version words its prompts differently.
	TshPrompts map[string][]string `yaml:"tsh_prompts,omitempty"`
	// PortForwards are saved port forwards, started from the :pf view.
	PortForwards []PortForward `yaml:"port_forwards,omitempty"`
	// Exec configures multi-exec (:exec).
	Exec Exec `yaml:"exec,omitempty"`
}

// PortForward is a saved local port forward.
type PortForward struct {
	// Name labels the forward in the :pf view.
	Name string `yaml:"name"`
	// Proxy is a proxy name or address; empty means the current proxy.
	Proxy string `yaml:"proxy,omitempty"`
	// Cluster is the node's Teleport cluster; empty means the one the
	// server list finds the host in, else the proxy's selected cluster.
	Cluster string `yaml:"cluster,omitempty"`
	Host    string `yaml:"host"`
	Login   string `yaml:"login,omitempty"`
	// Forward is "[bind:]localport:remotehost:remoteport" as for ssh -L.
	Forward string `yaml:"forward"`
}

// Multi-exec defaults and bounds.
const (
	DefaultExecConcurrency = 5
	MaxExecConcurrency     = 50
	DefaultExecTimeout     = 60 * time.Second
)

// Exec configures multi-exec.
type Exec struct {
	// Concurrency is how many servers run the command at once (default 5).
	Concurrency int `yaml:"concurrency,omitempty"`
	// Timeout bounds each server's run, e.g. "60s" (default 60s).
	Timeout string `yaml:"timeout,omitempty"`
}

// TimeoutDuration is Timeout parsed; Validate has checked it.
func (e Exec) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(e.Timeout)
	if err != nil || d <= 0 {
		return DefaultExecTimeout
	}
	return d
}

// Proxy is one Teleport proxy the user can switch to.
type Proxy struct {
	// Name is a short label shown in the proxies panel, e.g. "a-teleport".
	Name string `yaml:"name"`
	// Addr is the proxy address as tsh accepts it: host[:port][,sshport].
	Addr string `yaml:"addr"`
	// Shortcut is the single letter that switches to this proxy.
	Shortcut string `yaml:"shortcut,omitempty"`
	// User is the Teleport user to log in as.
	User string `yaml:"user,omitempty"`
	// Auth is the auth connector name (tsh --auth).
	Auth string `yaml:"auth,omitempty"`
}

// Keybindings holds user-configurable keys.
type Keybindings struct {
	// Focus is the prefix key that moves focus out of the terminal.
	Focus string `yaml:"focus,omitempty"`
}

// DefaultScrollback is the number of lines a session keeps by default.
const DefaultScrollback = 10000

// MaxScrollback bounds terminal.scrollback (memory: about 100 bytes per
// cell of a stored line).
const MaxScrollback = 1000000

// Terminal configures the embedded terminal.
type Terminal struct {
	// Scrollback is the number of lines kept per session.
	Scrollback int `yaml:"scrollback,omitempty"`
	// AutoReconnect restarts a session that dropped: one that ended with a
	// non-zero exit status and no tsh error, or with "host unreachable".
	AutoReconnect bool `yaml:"auto_reconnect,omitempty"`
	// Mouse enables mouse support (selection, wheel scrolling, clicks);
	// default true. Without it the host terminal's own selection works.
	Mouse *bool `yaml:"mouse,omitempty"`
}

// MouseEnabled reports whether mouse support is on.
func (t Terminal) MouseEnabled() bool { return t.Mouse == nil || *t.Mouse }

// Default returns a configuration with every default applied.
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.Skin == "" {
		c.Skin = DefaultSkin
	}
	if c.Keybindings.Focus == "" {
		c.Keybindings.Focus = DefaultFocusKey
	}
	if c.Favorites == nil {
		c.Favorites = map[string][]string{}
	}
	if c.Terminal.Scrollback == 0 {
		c.Terminal.Scrollback = DefaultScrollback
	}
	if c.Exec.Concurrency == 0 {
		c.Exec.Concurrency = DefaultExecConcurrency
	}
}

// DefaultPath returns $XDG_CONFIG_HOME/tstui/config.yaml, falling back to
// ~/.config/tstui/config.yaml. getenv is os.Getenv in production.
func DefaultPath(getenv func(string) string) (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "tstui", "config.yaml"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("find config directory: %w", err)
		}
	}
	return filepath.Join(home, ".config", "tstui", "config.yaml"), nil
}

// Load reads and validates the config at path. A missing file is not an
// error: it yields the defaults. The returned warnings are problems that do
// not stop tstui, such as a config file readable by other users.
func Load(path string) (*Config, []string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	var warnings []string
	if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, fmt.Sprintf(
			"config file %s has mode %#o; it should be %#o (run: chmod 600 %s)",
			path, st.Mode().Perm(), FileMode, path))
	}

	cfg, err := Parse(f)
	if err != nil {
		return nil, warnings, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, warnings, nil
}

// Parse decodes and validates a config document. Unknown keys are errors,
// so typos do not pass silently.
func Parse(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	cfg := &Config{}
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var (
	shortcutRE = regexp.MustCompile(`^[a-z]$`)
	keyRE      = regexp.MustCompile(`^(Ctrl|Alt)-[A-Za-z]$`)
	nameRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Validate checks the config and returns every problem found, joined.
func (c *Config) Validate() error {
	var errs []error
	names := map[string]bool{}
	shortcuts := map[string]string{}
	for i, p := range c.Proxies {
		where := fmt.Sprintf("proxies[%d]", i)
		if p.Name != "" {
			where = fmt.Sprintf("proxy %q", p.Name)
		}
		switch {
		case p.Name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		case !nameRE.MatchString(p.Name):
			errs = append(errs, fmt.Errorf("%s: name may only contain letters, digits, '.', '_' and '-'", where))
		case names[p.Name]:
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		names[p.Name] = true

		if err := validateAddr(p.Addr); err != nil {
			errs = append(errs, fmt.Errorf("%s: addr: %w", where, err))
		}

		if p.Shortcut != "" {
			if !shortcutRE.MatchString(p.Shortcut) {
				errs = append(errs, fmt.Errorf("%s: shortcut %q must be a single lowercase letter", where, p.Shortcut))
			} else if other, dup := shortcuts[p.Shortcut]; dup {
				errs = append(errs, fmt.Errorf("%s: shortcut %q is already used by proxy %q", where, p.Shortcut, other))
			}
			shortcuts[p.Shortcut] = p.Name
		}
	}

	if c.DefaultProxy != "" && !names[c.DefaultProxy] {
		errs = append(errs, fmt.Errorf("default_proxy %q does not match any proxy name", c.DefaultProxy))
	}
	for proxy := range c.Favorites {
		if !names[proxy] {
			errs = append(errs, fmt.Errorf("favorites: %q does not match any proxy name", proxy))
		}
	}
	if !keyRE.MatchString(c.Keybindings.Focus) {
		errs = append(errs, fmt.Errorf("keybindings.focus %q must look like Ctrl-B or Alt-X", c.Keybindings.Focus))
	}
	if c.Terminal.Scrollback < 0 || c.Terminal.Scrollback > MaxScrollback {
		errs = append(errs, fmt.Errorf("terminal.scrollback %d must be between 0 and %d", c.Terminal.Scrollback, MaxScrollback))
	}
	if !nameRE.MatchString(c.Skin) {
		errs = append(errs, fmt.Errorf("skin %q is not a valid skin name", c.Skin))
	}
	if c.Exec.Concurrency < 1 || c.Exec.Concurrency > MaxExecConcurrency {
		errs = append(errs, fmt.Errorf("exec.concurrency %d must be between 1 and %d", c.Exec.Concurrency, MaxExecConcurrency))
	}
	if c.Exec.Timeout != "" {
		if d, err := time.ParseDuration(c.Exec.Timeout); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("exec.timeout %q must be a positive duration such as 60s or 5m", c.Exec.Timeout))
		}
	}
	pfNames := map[string]bool{}
	for i, pf := range c.PortForwards {
		where := fmt.Sprintf("port_forwards[%d]", i)
		if pf.Name != "" {
			where = fmt.Sprintf("port forward %q", pf.Name)
		}
		switch {
		case pf.Name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		case !nameRE.MatchString(pf.Name):
			errs = append(errs, fmt.Errorf("%s: name may only contain letters, digits, '.', '_' and '-'", where))
		case pfNames[pf.Name]:
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		pfNames[pf.Name] = true
		if pf.Host == "" || strings.HasPrefix(pf.Host, "-") || strings.ContainsAny(pf.Host, "@: \t") {
			errs = append(errs, fmt.Errorf("%s: host %q is not a valid server name", where, pf.Host))
		}
		if strings.HasPrefix(pf.Login, "-") || strings.ContainsAny(pf.Login, "@: \t") {
			errs = append(errs, fmt.Errorf("%s: login %q is not valid", where, pf.Login))
		}
		if _, _, _, _, err := portfwd.ParseForward(pf.Forward); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		}
	}
	for key, exprs := range c.TshPrompts {
		for _, e := range exprs {
			if _, err := regexp.Compile(e); err != nil {
				errs = append(errs, fmt.Errorf("tsh_prompts.%s: %w", key, err))
			}
		}
	}
	return errors.Join(errs...)
}

func validateAddr(addr string) error {
	if addr == "" {
		return errors.New("is required")
	}
	if strings.Contains(addr, "://") {
		return fmt.Errorf("%q must be host[:port], without a scheme", addr)
	}
	web, _, _ := strings.Cut(addr, ",")
	host := web
	if h, _, err := net.SplitHostPort(web); err == nil {
		host = h
	}
	if host == "" || strings.ContainsAny(host, " /") {
		return fmt.Errorf("%q is not a valid host[:port]", addr)
	}
	return nil
}

// StateDir returns $XDG_STATE_HOME/tstui, falling back to
// ~/.local/state/tstui: where logs and remembered state live.
func StateDir(getenv func(string) string) (string, error) {
	if x := getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "tstui"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("find state directory: %w", err)
		}
	}
	return filepath.Join(home, ".local", "state", "tstui"), nil
}

// ProxyByName returns the proxy with the given name.
func (c *Config) ProxyByName(name string) (Proxy, bool) {
	for _, p := range c.Proxies {
		if p.Name == name {
			return p, true
		}
	}
	return Proxy{}, false
}

// Save validates cfg and writes it to path with mode 0600, creating the
// directory (mode 0700) if needed. The write is atomic: a temporary file
// is renamed over the old one.
func Save(path string, cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(FileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save config: %w", err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}
