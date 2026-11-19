//go:build linux || darwin

package tsh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type ptyProcess struct {
	cmd  *exec.Cmd
	ptmx *os.File
	done chan struct{}

	exitCode  int
	closeOnce sync.Once
}

func startPTY(ctx context.Context, bin string, args, env []string, o PTYOptions) (*ptyProcess, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	// The child gets its own copy of tty; ours is not needed after Start.
	defer tty.Close()

	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: o.Rows, Cols: o.Cols}); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("set pty size: %w", err)
	}
	if o.NoEcho {
		if err := disableEcho(tty); err != nil {
			_ = ptmx.Close()
			return nil, err
		}
	}

	// Not CommandContext: cancellation must kill the whole session (below),
	// not only the direct child.
	cmd := exec.Command(bin, args...)
	cmd.Env = SafeEnv(mergeEnv(env, o.Env))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if o.Stderr != nil {
		// exec copies the pipe into o.Stderr; Wait returns once it is
		// drained, or WaitDelay after exit if a grandchild keeps it open.
		cmd.Stderr = o.Stderr
		cmd.WaitDelay = 2 * time.Second
	}
	// A new session with tty as the controlling terminal, like a login
	// shell. The session leader's pid is also its process group id, so
	// Kill can signal everything tsh started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w (%s): %w", ErrNotFound, bin, err)
		}
		return nil, err
	}

	p := &ptyProcess{cmd: cmd, ptmx: ptmx, done: make(chan struct{}), exitCode: -1}
	go func() {
		err := cmd.Wait()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			p.exitCode = 0
		case errors.As(err, &exitErr):
			p.exitCode = exitErr.ExitCode() // -1 when killed by a signal
		}
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Kill()
		case <-p.done:
		}
	}()
	return p, nil
}

func disableEcho(tty *os.File) error {
	t, err := unix.IoctlGetTermios(int(tty.Fd()), ioctlGetTermios)
	if err != nil {
		return fmt.Errorf("read pty modes: %w", err)
	}
	t.Lflag &^= unix.ECHO | unix.ECHONL
	if err := unix.IoctlSetTermios(int(tty.Fd()), ioctlSetTermios, t); err != nil {
		return fmt.Errorf("turn pty echo off: %w", err)
	}
	return nil
}

func (p *ptyProcess) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *ptyProcess) Write(b []byte) (int, error) { return p.ptmx.Write(b) }
func (p *ptyProcess) Done() <-chan struct{}       { return p.done }

func (p *ptyProcess) ExitCode() int {
	select {
	case <-p.done:
		return p.exitCode
	default:
		return -1
	}
}

func (p *ptyProcess) Resize(rows, cols uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *ptyProcess) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (p *ptyProcess) Close() error {
	var err error
	p.closeOnce.Do(func() {
		_ = p.Kill()
		err = p.ptmx.Close()
	})
	return err
}
