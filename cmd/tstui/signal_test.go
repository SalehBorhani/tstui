//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// children lists the pids whose parent is this test process (Linux /proc).
func children(t *testing.T) []int {
	t.Helper()
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	var out []int
	for _, p := range stats {
		b, err := os.ReadFile(p)
		if err != nil {
			continue // gone meanwhile
		}
		// pid (comm) state ppid ...; comm may hold spaces, so split after ')'.
		rest := string(b[bytes.LastIndexByte(b, ')')+1:])
		fields := strings.Fields(rest)
		if len(fields) < 2 || fields[1] != strconv.Itoa(os.Getpid()) || fields[0] == "Z" {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Dir(p), "/proc/"), "/"))
		out = append(out, pid)
	}
	return out
}

// A signal stops tstui the clean way: the session's shell is killed, and
// the exit code is 128 + the signal number.
func TestSignalShutsDownCleanly(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			e := newLoginOnlyEnv(t, "otp")
			e.environ = append(e.environ, "FAKE_TSH_STATE=valid")
			term := newSimTerminal()
			var out, errOut bytes.Buffer
			codeCh := make(chan int, 1)
			go func() {
				codeCh <- runWithScreen(term.factory,
					[]string{"--proxy=tp.afr.example:443", "--demo-shell", "--log-file=" + e.logFile},
					nil, &out, &errOut, e.getenv, e.environ)
			}()
			term.wait(t)
			var shells []int
			deadline := time.Now().Add(5 * time.Second)
			for len(shells) == 0 && time.Now().Before(deadline) {
				shells = children(t)
				time.Sleep(20 * time.Millisecond)
			}
			if len(shells) == 0 {
				t.Fatal("the demo shell never started")
			}

			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-codeCh:
				if want := 128 + int(sig); code != want {
					t.Fatalf("exit %d, want %d; stderr %q", code, want, errOut.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("tstui did not stop on the signal")
			}
			for _, pid := range shells {
				if err := syscall.Kill(pid, 0); err == nil {
					t.Errorf("child %d still runs after tstui stopped", pid)
				}
			}
		})
	}
}
