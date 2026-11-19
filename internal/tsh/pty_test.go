//go:build linux || darwin

package tsh

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func runPTY(t *testing.T, script string, env []string, o PTYOptions) (string, Process) {
	t.Helper()
	p, err := StartCommandPTY(context.Background(), "/bin/sh", []string{"-c", script}, env, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, p) // ends with EIO once the child is gone
		close(done)
	}()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("output did not end")
	}
	return out.String(), p
}

func TestStartCommandPTYEnvAndExit(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "TERM=dumb", "KEEP=1"}
	out, p := runPTY(t, `echo "term=$TERM keep=$KEEP extra=$EXTRA"; stty size; exit 7`, env,
		PTYOptions{Rows: 30, Cols: 100, Env: []string{"TERM=xterm-256color", "EXTRA=x"}})
	if !strings.Contains(out, "term=xterm-256color keep=1 extra=x") {
		t.Errorf("env not merged: %q", out)
	}
	if !strings.Contains(out, "30 100") {
		t.Errorf("size not set: %q", out)
	}
	if p.ExitCode() != 7 {
		t.Errorf("exit code %d", p.ExitCode())
	}
}

func TestStartCommandPTYSeparateStderr(t *testing.T) {
	var stderr syncBuffer
	out, _ := runPTY(t, `echo out; echo err >&2`, []string{"PATH=/usr/bin:/bin"}, PTYOptions{Stderr: &stderr})
	if strings.Contains(out, "err") || !strings.Contains(out, "out") {
		t.Errorf("terminal got %q", out)
	}
	if stderr.String() != "err\n" {
		t.Errorf("stderr got %q", stderr.String())
	}
}

// Secrets in the environment never reach the child, whether they come in
// the base environment or in the extras.
func TestStartCommandPTYStripsSecrets(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "TSTUI_PASSWORD=hunter2-env", "TSTUI_MFA=654321"}
	out, _ := runPTY(t, `env`, env, PTYOptions{Env: []string{"TSTUI_PASSWORD=hunter2-extra"}})
	for _, secret := range []string{"hunter2", "654321", "TSTUI_PASSWORD", "TSTUI_MFA"} {
		if strings.Contains(out, secret) {
			t.Errorf("child environment contains %q", secret)
		}
	}
}

func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"A=1", "TERM=dumb", "B=2"}, []string{"TERM=xterm", "C=3"})
	if strings.Join(got, " ") != "A=1 B=2 TERM=xterm C=3" {
		t.Errorf("got %q", got)
	}
	if got := mergeEnv([]string{"A=1"}, nil); len(got) != 1 {
		t.Errorf("no extras: %q", got)
	}
}
