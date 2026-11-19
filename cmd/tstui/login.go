package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/session"
	"github.com/root-ali/tstui/internal/tsh"
	"github.com/root-ali/tstui/internal/ui"
)

// screenFactory creates the terminal screen; tests use a simulation screen.
type screenFactory func() (tcell.Screen, error)

// runTUI runs the terminal UI. With --login-only it stops as soon as the
// proxy is logged in and prints the result.
func runTUI(ctx context.Context, stdout io.Writer, newScreen screenFactory, client tsh.Client,
	cfg *config.Config, skins *ui.Skins, creds *auth.Credentials, f *flags, logger *slog.Logger,
	getenv func(string) string, environ []string) error {
	patterns, err := auth.NewPatterns(cfg.TshPrompts)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	svc := auth.NewService(client, auth.Options{Patterns: patterns, Logger: logger})

	var spawner session.Spawner = session.TshSpawner{Client: client, Insecure: f.insecure}
	if f.demoShell {
		spawner = session.ShellSpawner{Environ: environ}
	}
	logins := loadLoginMemory(getenv, logger)
	cfgPath := f.configPath
	if cfgPath == "" {
		cfgPath, _ = config.DefaultPath(getenv) // already resolved once by the caller
	}
	stateDir, err := config.StateDir(getenv)
	if err != nil {
		logger.Warn("state directory unavailable: no audit or transfer log", "err", err)
		stateDir = ""
	}

	screen, err := newScreen()
	if err != nil {
		return fmt.Errorf("tstui needs a terminal: %w", err)
	}
	app := ui.NewApp(screen, ui.Options{
		Version:        version,
		Client:         client,
		Auth:           svc,
		Config:         cfg,
		Skins:          skins,
		Creds:          *creds,
		Insecure:       f.insecure,
		Logger:         logger,
		ExitAfterLogin: f.loginOnly,
		Spawner:        spawner,
		DemoShell:      f.demoShell,
		Logins:         logins,
		ConfigPath:     cfgPath,
		StateDir:       stateDir,
		Home:           getenv("HOME"),
	})
	*creds = auth.Credentials{} // the app owns the secrets now
	if err := app.Run(ctx); err != nil {
		return err
	}
	if !f.loginOnly {
		return nil
	}

	proxy, st, err := app.LoginResult()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "logged in to %s as %s; certificate valid until %s (%s left)\n",
		proxy, st.User, st.ValidUntil.Local().Format(time.DateTime), st.Remaining(time.Now()).Round(time.Minute))
	return nil
}

// loadLoginMemory reads the logins remembered per server. Problems are
// logged, not fatal: the memory is a convenience.
func loadLoginMemory(getenv func(string) string, logger *slog.Logger) *session.LoginMemory {
	path, err := session.DefaultLoginsPath(getenv)
	if err != nil {
		logger.Warn("login memory disabled", "err", err)
		path = ""
	}
	mem, err := session.LoadLogins(path)
	if err != nil {
		logger.Warn("login memory", "err", err)
	}
	return mem
}

// newLogger returns a debug logger writing to path (created mode 0600),
// or a logger that discards everything when path is empty.
func newLogger(path string) (*slog.Logger, func(), error) {
	if path == "" {
		return slog.New(slog.NewTextHandler(io.Discard, nil)), func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})), func() { _ = f.Close() }, nil
}
