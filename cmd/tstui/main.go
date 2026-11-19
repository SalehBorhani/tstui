// Command tstui is a k9s-style terminal UI for Teleport SSH.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/health"
	"github.com/root-ali/tstui/internal/tsh"
	"github.com/root-ali/tstui/internal/ui"
)

// Build information, set with -ldflags "-X main.version=... -X
// main.commit=... -X main.date=..." (see the Makefile).
var (
	version = "0.1.0-dev"
	commit  = ""
	date    = ""
)

// versionLine is what --version prints.
func versionLine() string {
	s := "tstui " + version
	var extra []string
	if commit != "" {
		extra = append(extra, "commit "+commit)
	}
	if date != "" {
		extra = append(extra, "built "+date)
	}
	extra = append(extra, runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH)
	return s + " (" + strings.Join(extra, ", ") + ")"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv, os.Environ()))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, environ []string) int {
	return runWithScreen(tcell.NewScreen, args, stdin, stdout, stderr, getenv, environ)
}

// Exit codes. A signal gives 128 + its number, as in a shell.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
	exitCrash = 3 // an internal error (panic); the report is on stderr
)

func runWithScreen(newScreen screenFactory, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, environ []string) int {
	f, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "tstui: %v\n", err)
		return exitUsage
	}
	if f.version {
		fmt.Fprintln(stdout, versionLine())
		return exitOK
	}
	// Without tsh there is nothing to show; say how to get it before the
	// UI takes the screen. --demo-shell works without it.
	if _, err := exec.LookPath("tsh"); err != nil && !f.demoShell {
		fmt.Fprint(stderr, tshMissing)
		return exitError
	}

	cfgPath := f.configPath
	if cfgPath == "" {
		if cfgPath, err = config.DefaultPath(getenv); err != nil {
			fmt.Fprintf(stderr, "tstui: %v\n", err)
			return exitError
		}
	}
	cfg, warnings, err := config.Load(cfgPath)
	printWarnings(stderr, warnings)
	if err != nil {
		fmt.Fprintf(stderr, "tstui: %v\n", err)
		return exitError
	}
	skins, warnings := ui.LoadSkins(filepath.Join(filepath.Dir(cfgPath), "skins"))
	printWarnings(stderr, warnings)
	if f.skin != "" {
		if _, ok := skins.Theme(f.skin); !ok {
			fmt.Fprintf(stderr, "tstui: --skin: unknown skin %q (skins: %s)\n", f.skin, strings.Join(skins.Names(), ", "))
			return exitUsage
		}
		cfg.Skin = f.skin
	} else if _, ok := skins.Theme(cfg.Skin); !ok {
		printWarnings(stderr, []string{fmt.Sprintf("unknown skin %q in the config; using the default (skins: %s)",
			cfg.Skin, strings.Join(skins.Names(), ", "))})
	}

	creds, warnings, err := resolveCredentials(f, cfg, getenv, stdin)
	defer creds.Wipe()
	printWarnings(stderr, warnings)
	if err != nil {
		fmt.Fprintf(stderr, "tstui: %v\n", err)
		return exitError
	}

	// Core dumps would hold whatever secrets were in memory at the time.
	disableCoreDumps()

	ctx, caught, stop := signalContext(context.Background())
	defer stop()

	logger, closeLog, err := newLogger(f.logFile)
	if err != nil {
		fmt.Fprintf(stderr, "tstui: %v\n", err)
		return exitError
	}
	defer closeLog()
	logger.Info("tstui start", "version", version, "args", tsh.RedactArgs(args))

	client := tsh.NewCLI(tsh.Options{Insecure: f.insecure, Environ: environ, Logger: logger})
	if f.report {
		err = debugReport(ctx, stdout, client, cfgPath, cfg, &creds)
	} else {
		err = runTUI(ctx, stdout, newScreen, client, cfg, skins, &creds, f, logger, getenv, environ)
	}
	var crash *ui.PanicError
	switch {
	case errors.As(err, &crash):
		logger.Error("panic", "value", crash.Value)
		fmt.Fprintf(stderr, "tstui: internal error: %v\n\n%s\n", crash.Value, crash.Stack)
		fmt.Fprintln(stderr, "tstui closed its sessions and restored the terminal. Please report this, with the trace above"+
			" (it holds code locations, not passwords). If the terminal still looks wrong, run: reset")
		return exitCrash
	case caught() != nil:
		logger.Info("tstui stopped by signal", "signal", caught().String())
		if sig, ok := caught().(syscall.Signal); ok {
			return 128 + int(sig)
		}
		return exitError
	case err != nil:
		fmt.Fprintf(stderr, "tstui: %v\n", err)
		return exitError
	}
	return exitOK
}

// signalContext returns a context that is cancelled by the first SIGINT,
// SIGTERM or SIGHUP (the terminal went away), and a function reporting
// which signal that was. The app then shuts down: sessions, port forwards
// and running transfers are killed, the screen restored. A second signal
// gets the default action and kills tstui at once, in case the cleanup
// hangs. stop releases the signals.
func signalContext(parent context.Context) (ctx context.Context, caught func() os.Signal, stop func()) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	signal.Notify(ch, signals...)
	var (
		mu  sync.Mutex
		got os.Signal
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-ch:
			mu.Lock()
			got = sig
			mu.Unlock()
			signal.Reset(signals...) // the next one kills
			cancel()
		case <-ctx.Done():
		}
	}()
	caught = func() os.Signal {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	stop = func() {
		signal.Stop(ch)
		cancel()
		<-done
	}
	return ctx, caught, stop
}

// tshMissing is printed when tsh is not on PATH.
const tshMissing = `tstui: tsh, Teleport's command-line client, is not on your PATH.
tstui runs tsh for everything (login, listing, ssh), so it must be installed.

  Download: https://goteleport.com/download/ (choose the major version of
            your Teleport cluster, or one major version older)
  macOS:    the tsh .pkg from that page, or: brew install teleport
  Linux:    Teleport's apt or yum repository (package "teleport"), or the
            .tar.gz from that page with tsh copied to a directory on PATH

Then check with: tsh version
`

func printWarnings(w io.Writer, warnings []string) {
	for _, msg := range warnings {
		fmt.Fprintf(w, "tstui: warning: %s\n", msg)
	}
}

// debugReport prints what tstui knows about tsh, the proxy and the user
// as plain text (hidden --report flag, for debugging).
func debugReport(ctx context.Context, w io.Writer, c tsh.Client, cfgPath string, cfg *config.Config, creds *auth.Credentials) error {
	now := time.Now()
	fmt.Fprintf(w, "tstui %s (debug report)\n\n", version)
	fmt.Fprintf(w, "config    %s (%d proxies, skin %s, focus key %s)\n", cfgPath, len(cfg.Proxies), cfg.Skin, cfg.Keybindings.Focus)

	v, err := c.Version(ctx)
	if err != nil {
		return fmt.Errorf("tsh is required: %w", err)
	}
	fmt.Fprintf(w, "client    tsh %s\n", v.Version)
	if c := health.CheckClient(v.Version); c.Level == health.CompatWarn {
		fmt.Fprintf(w, "          warning: %s\n", c.Message)
	}

	profiles, err := c.Profiles()
	if err != nil {
		fmt.Fprintf(w, "profiles  error: %v\n", err)
	}
	proxy := creds.Proxy
	if proxy == "" {
		for _, p := range profiles {
			if p.Current {
				proxy = p.WebProxyAddr
			}
		}
	}
	if proxy == "" {
		fmt.Fprintln(w, "proxy     none: give --proxy, set default_proxy in the config, or log in with tsh once")
		return nil
	}
	fmt.Fprintf(w, "proxy     %s\n", proxy)

	if ping, err := c.ServerPing(ctx, proxy); err != nil {
		fmt.Fprintf(w, "server    unreachable: %v\n", err)
	} else {
		mfa := ping.SecondFactor
		if ping.PreferredLocalMFA != "" && ping.PreferredLocalMFA != mfa {
			mfa += "/" + ping.PreferredLocalMFA
		}
		fmt.Fprintf(w, "server    v%s  cluster %s  auth %s  mfa %s  latency %s\n",
			ping.ServerVersion, ping.ClusterName, ping.AuthType, mfa, ping.Latency.Round(time.Millisecond))
	}

	st, err := c.Status(ctx, proxy)
	switch {
	case err != nil:
		fmt.Fprintf(w, "login     error: %v\n", err)
	case !st.LoggedIn:
		fmt.Fprintln(w, "login     not logged in")
	case st.Expired(now):
		fmt.Fprintf(w, "login     %s, certificate EXPIRED at %s\n", st.User, st.ValidUntil.Local().Format(time.DateTime))
	default:
		fmt.Fprintf(w, "login     %s, certificate valid for %s (until %s)\n",
			st.User, st.Remaining(now).Round(time.Minute), st.ValidUntil.Local().Format(time.DateTime))
	}
	if st.LoggedIn {
		fmt.Fprintf(w, "roles     %s\nlogins    %s\n", strings.Join(st.Roles, ", "), strings.Join(st.Logins, ", "))
	}
	fmt.Fprintf(w, "creds     user=%q password=%s otp=%s auth=%q (missing values will be asked for in a popup)\n",
		creds.User, presence(creds.Password), presence(creds.OTP), creds.AuthConnector)

	if len(profiles) > 0 {
		fmt.Fprintln(w, "\nprofiles")
		for _, p := range profiles {
			mark := " "
			if p.Current {
				mark = "*"
			}
			fmt.Fprintf(w, "  %s %-30s user %-12s cluster %s\n", mark, p.WebProxyAddr, p.User, p.Cluster)
		}
	}

	if !st.LoggedIn || st.Expired(now) {
		fmt.Fprintln(w, "\nservers   skipped: log in first (tstui --login-only, or tsh login)")
		return nil
	}
	listing, err := tsh.ListAll(ctx, c, proxy)
	if err != nil {
		fmt.Fprintf(w, "\nservers   error: %v\n", err)
		return nil
	}
	if len(listing.Clusters) > 0 {
		fmt.Fprintf(w, "\nclusters (%d)\n", len(listing.Clusters))
		for _, cl := range listing.Clusters {
			state := cl.Status
			if e := listing.Errs[cl.Name]; e != nil {
				state += ", list error: " + e.Error()
			}
			fmt.Fprintf(w, "  %-30s %-5s %s\n", cl.Name, cl.Type, state)
		}
	}
	servers := listing.Servers
	fmt.Fprintf(w, "\nservers (%d)\n", len(servers))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  HOSTNAME\tCLUSTER\tADDR\tVERSION\tLABELS")
	for _, s := range servers {
		addr := s.Addr
		if addr == "" {
			addr = "⟵ tunnel"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", s.Hostname, s.Cluster, addr, s.Version, formatLabels(s.Labels))
	}
	return tw.Flush()
}

func presence(s *auth.Secret) string {
	if s.Empty() {
		return "missing"
	}
	return "set"
}

func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ",")
}
