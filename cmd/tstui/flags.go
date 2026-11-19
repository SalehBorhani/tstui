package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/config"
)

// flags holds the raw command-line values.
type flags struct {
	proxy         string
	user          string
	passwordStdin bool
	passwordFile  string
	password      string
	mfaToken      string
	authConnector string
	insecure      bool
	configPath    string
	skin          string
	version       bool

	// Hidden debugging flags.
	loginOnly bool
	logFile   string
	report    bool
	demoShell bool
}

// hiddenFlags are left out of the usage text: temporary or debug-only.
var hiddenFlags = map[string]bool{"login-only": true, "log-file": true, "report": true, "demo-shell": true}

const usageHeader = `tstui - a terminal UI for Teleport SSH

Usage:
  tstui [flags]

Anything needed to log in that is not given here is asked for in a popup.

Flags:
`

const usageFooter = `
Environment:
  TSTUI_PROXY, TSTUI_USER, TSTUI_PASSWORD, TSTUI_MFA
  Flags take precedence over environment, environment over the config file.
`

func parseFlags(args []string, stderr io.Writer) (*flags, error) {
	f := &flags{}
	fs := flag.NewFlagSet("tstui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.proxy, "proxy", "", "Teleport proxy `HOST:PORT`, or a proxy name from the config")
	fs.StringVar(&f.user, "user", "", "Teleport user `NAME`")
	fs.BoolVar(&f.passwordStdin, "password-stdin", false, "read the password from stdin")
	fs.StringVar(&f.passwordFile, "password-file", "", "read the password from `PATH` (should be mode 0600)")
	fs.StringVar(&f.password, "password", "", "password `PASS` (insecure: visible in shell history and ps)")
	fs.StringVar(&f.mfaToken, "mfa-token", "", "OTP `CODE` for MFA")
	fs.StringVar(&f.authConnector, "auth", "", "auth `CONNECTOR` name (tsh --auth)")
	fs.BoolVar(&f.insecure, "insecure", false, "skip TLS certificate verification (test clusters only)")
	fs.StringVar(&f.configPath, "config", "", "config file `PATH` (default $XDG_CONFIG_HOME/tstui/config.yaml)")
	fs.StringVar(&f.skin, "skin", "", "skin `NAME`")
	fs.BoolVar(&f.version, "version", false, "print the version and exit")
	fs.BoolVar(&f.loginOnly, "login-only", false, "open the UI, log in if needed, print the result and exit (temporary, for testing)")
	fs.BoolVar(&f.report, "report", false, "print what tstui finds out about tsh and the proxy as plain text, no UI")
	fs.StringVar(&f.logFile, "log-file", "", "write debug logs to `PATH` (never contains secrets)")
	fs.BoolVar(&f.demoShell, "demo-shell", false, "open a local bash in a tab, and run bash instead of tsh ssh on Enter (try the terminal without a cluster)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usageHeader)
		printVisibleDefaults(fs)
		fmt.Fprint(fs.Output(), usageFooter)
	}

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	sources := 0
	for _, set := range []bool{f.passwordStdin, f.passwordFile != "", f.password != ""} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return nil, errors.New("use only one of --password-stdin, --password-file and --password")
	}
	return f, nil
}

// printVisibleDefaults is fs.PrintDefaults without the hidden flags.
func printVisibleDefaults(fs *flag.FlagSet) {
	visible := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
	visible.SetOutput(fs.Output())
	fs.VisitAll(func(fl *flag.Flag) {
		if !hiddenFlags[fl.Name] {
			visible.Var(fl.Value, fl.Name, fl.Usage)
		}
	})
	visible.PrintDefaults()
}

// maxPasswordLen bounds what is read from stdin or a password file.
const maxPasswordLen = 4096

// resolveCredentials merges flags, environment and config into the login
// inputs, in that order of precedence. It returns warnings for insecure
// choices. The caller must Wipe the result.
func resolveCredentials(f *flags, cfg *config.Config, getenv func(string) string, stdin io.Reader) (auth.Credentials, []string, error) {
	var c auth.Credentials
	var warnings []string

	c.Proxy = first(f.proxy, getenv("TSTUI_PROXY"))
	var proxyCfg config.Proxy
	if c.Proxy == "" && cfg.DefaultProxy != "" {
		proxyCfg, _ = cfg.ProxyByName(cfg.DefaultProxy)
	} else if p, ok := cfg.ProxyByName(c.Proxy); ok {
		proxyCfg = p // a config proxy name was given instead of an address
	} else {
		for _, p := range cfg.Proxies {
			if p.Addr == c.Proxy {
				proxyCfg = p
				break
			}
		}
	}
	if proxyCfg.Addr != "" {
		c.Proxy = proxyCfg.Addr
	}

	c.User = first(f.user, getenv("TSTUI_USER"), proxyCfg.User)
	c.AuthConnector = first(f.authConnector, proxyCfg.Auth)

	switch {
	case f.passwordStdin:
		b, err := readSecret(stdin)
		if err != nil {
			return c, warnings, fmt.Errorf("read password from stdin: %w", err)
		}
		c.Password = auth.NewSecret(b)
	case f.passwordFile != "":
		b, warn, err := readPasswordFile(f.passwordFile)
		if err != nil {
			return c, warnings, err
		}
		if warn != "" {
			warnings = append(warnings, warn)
		}
		c.Password = auth.NewSecret(b)
	case f.password != "":
		warnings = append(warnings, "--password exposes the password in shell history and the process list; prefer --password-stdin or --password-file")
		c.Password = auth.NewSecret([]byte(f.password))
		f.password = ""
	case getenv("TSTUI_PASSWORD") != "":
		c.Password = auth.NewSecret([]byte(getenv("TSTUI_PASSWORD")))
	}

	if otp := first(f.mfaToken, getenv("TSTUI_MFA")); otp != "" {
		c.OTP = auth.NewSecret([]byte(otp))
		f.mfaToken = ""
	}
	return c, warnings, nil
}

func readPasswordFile(path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open password file: %w", err)
	}
	defer file.Close()
	var warn string
	if st, err := file.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		warn = fmt.Sprintf("password file %s is readable by other users (mode %#o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	b, err := readSecret(file)
	if err != nil {
		return nil, warn, fmt.Errorf("read password file %s: %w", path, err)
	}
	return b, warn, nil
}

// readSecret reads one secret value and strips a single trailing newline.
func readSecret(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxPasswordLen+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPasswordLen {
		wipe(b)
		return nil, fmt.Errorf("longer than %d bytes", maxPasswordLen)
	}
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	if len(b) == 0 {
		return nil, errors.New("empty")
	}
	return b, nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
