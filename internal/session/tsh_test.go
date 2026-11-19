package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/root-ali/tstui/internal/tsh"
)

// fakeTshCLI is the real tsh.CLI running testdata/fake-tsh/tsh by its
// absolute path (no PATH lookup, so the real tsh can never run).
func fakeTshCLI(t *testing.T, env ...string) (*tsh.CLI, string) {
	t.Helper()
	bin, err := filepath.Abs("../../testdata/fake-tsh/tsh")
	if err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	environ := append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TELEPORT_HOME=" + t.TempDir(),
		"FAKE_TSH_ARGV_LOG=" + argvLog,
	}, env...)
	return tsh.NewCLI(tsh.Options{Bin: bin, Environ: environ}), argvLog
}

func TestTshSessionThroughFakeTsh(t *testing.T) {
	cli, argvLog := fakeTshCLI(t, "TSTUI_PASSWORD=hunter2", "TSTUI_MFA=123456")
	s := New(1, Spec{Proxy: "tp.afr.example:443", Host: "web-01", Login: "root"}, TshSpawner{Client: cli}, Options{Cols: 80, Rows: 24})
	t.Cleanup(s.Close)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitText(t, s, "root@web-01:")
	s.Send([]byte("echo \"remote $TERM\"\r"))
	waitText(t, s, "remote xterm-256color")
	s.Send([]byte("exit 5\r"))
	code, err := waitState(t, s, Disconnected)
	if code != 5 || err != nil {
		t.Fatalf("exit %d err %v", code, err)
	}

	b, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	if !strings.Contains(log, "argv: ssh --proxy=tp.afr.example:443 root@web-01 |") {
		t.Errorf("argv log %q", log)
	}
	// The secrets tstui got through its environment never reach tsh.
	for _, secret := range []string{"TSTUI_PASSWORD", "TSTUI_MFA", "hunter2", "123456"} {
		if strings.Contains(log, secret) {
			t.Errorf("child saw %q: %q", secret, log)
		}
	}
}

func TestTshSessionErrors(t *testing.T) {
	tests := []struct {
		env  []string
		kind error
	}{
		{[]string{"FAKE_TSH_SSH=denied"}, ErrAccessDenied},
		{[]string{"FAKE_TSH_SSH=unreachable"}, ErrUnreachable},
		{[]string{"FAKE_TSH_SSH=mfa"}, ErrMFARequired},
		{[]string{"FAKE_TSH_SSH=notfound"}, ErrNotFound},
		{[]string{"FAKE_TSH_STATE=expired"}, ErrCertExpired},
	}
	for _, tt := range tests {
		t.Run(tt.env[0], func(t *testing.T) {
			cli, _ := fakeTshCLI(t, tt.env...)
			s := New(1, Spec{Proxy: "p:443", Host: "web-01", Login: "ali"}, TshSpawner{Client: cli}, Options{Cols: 80, Rows: 24})
			t.Cleanup(s.Close)
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			code, err := waitState(t, s, Failed)
			if code != 1 || !errors.Is(err, tt.kind) {
				t.Fatalf("exit %d err %v, want %v", code, err, tt.kind)
			}
			// tsh's message is on the screen as well.
			if !strings.Contains(s.Terminal().Text(), "ERROR:") {
				t.Errorf("screen %q", s.Terminal().Text())
			}
		})
	}
}
