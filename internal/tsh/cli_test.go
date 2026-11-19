package tsh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCLI returns a CLI that runs testdata/fake-tsh/tsh with the given
// extra environment, plus the path of the file the script logs argv to.
func fakeCLI(t *testing.T, env ...string) (*CLI, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tsh is a bash script")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake-tsh", "tsh"))
	if err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	environ := append([]string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_TSH_ARGV_LOG=" + argvLog,
		"TSTUI_PASSWORD=hunter2",
		"TSTUI_MFA=123456",
		"TSTUI_USER=ali",
	}, env...)
	return NewCLI(Options{Bin: bin, Environ: environ, Home: t.TempDir()}), argvLog
}

func TestCLIVersion(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("old=%v", old), func(t *testing.T) {
			var env []string
			if old {
				env = append(env, "FAKE_TSH_OLD=1")
			}
			c, _ := fakeCLI(t, env...)
			v, err := c.Version(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if v.Version != "18.11.3" {
				t.Fatalf("version = %q", v.Version)
			}
		})
	}
}

func TestCLIStatus(t *testing.T) {
	tests := []struct {
		state        string
		wantLoggedIn bool
		wantExpired  bool
	}{
		{"valid", true, false},
		{"expired", true, true},
		{"logged-out", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			c, argvLog := fakeCLI(t, "FAKE_TSH_STATE="+tt.state)
			s, err := c.Status(context.Background(), "tp.afr.example:443")
			if err != nil {
				t.Fatal(err)
			}
			if s.LoggedIn != tt.wantLoggedIn || s.Expired(time.Now()) != tt.wantExpired {
				t.Fatalf("status = %+v; want LoggedIn=%v Expired=%v", s, tt.wantLoggedIn, tt.wantExpired)
			}
			if tt.wantLoggedIn && (s.User != "ali" || len(s.Logins) != 2) {
				t.Fatalf("status = %+v", s)
			}
			log := readFile(t, argvLog)
			if !strings.Contains(log, "--proxy=tp.afr.example:443") || !strings.Contains(log, "--client") {
				t.Fatalf("argv log %q lacks --proxy or --client", log)
			}
		})
	}
}

func TestCLIListServers(t *testing.T) {
	tests := []struct {
		state   string
		wantN   int
		wantErr error
	}{
		{"valid", 3, nil},
		{"expired", 0, ErrNotLoggedIn},
		{"logged-out", 0, ErrNotLoggedIn},
		{"denied", 0, ErrAccessDenied},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			c, _ := fakeCLI(t, "FAKE_TSH_STATE="+tt.state)
			got, err := c.ListServers(context.Background(), "tp.afr.example:443", "")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if len(got) != tt.wantN {
				t.Fatalf("got %d servers, want %d", len(got), tt.wantN)
			}
		})
	}
}

func TestCLITimeout(t *testing.T) {
	c, _ := fakeCLI(t, "FAKE_TSH_SLEEP=5")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Version(ctx)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not stop the child process")
	}
}

func TestCLIMissingBinary(t *testing.T) {
	c := NewCLI(Options{Bin: filepath.Join(t.TempDir(), "no-such-tsh"), Environ: []string{}})
	if _, err := c.Version(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestCLINoSecretsReachChild checks that tstui's secret env vars and any
// secret values never reach tsh's argv or environment.
func TestCLINoSecretsReachChild(t *testing.T) {
	c, argvLog := fakeCLI(t)
	ctx := context.Background()
	_, _ = c.Version(ctx)
	_, _ = c.Status(ctx, "tp.afr.example")
	_, _ = c.ListServers(ctx, "tp.afr.example", "")
	log := readFile(t, argvLog)
	for _, secret := range []string{"hunter2", "123456", "TSTUI_PASSWORD", "TSTUI_MFA"} {
		if strings.Contains(log, secret) {
			t.Errorf("child saw %q:\n%s", secret, log)
		}
	}
	if !strings.Contains(log, "TSTUI_USER") {
		t.Errorf("non-secret env var TSTUI_USER should pass through:\n%s", log)
	}
}

func TestExecRunnerLogsNoSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	var buf bytes.Buffer
	r := ExecRunner{Bin: "/bin/sh", Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	// sh -c SCRIPT $0 $1 $2: the script echoes the secret args to stdout
	// and stderr, so they must be kept out of the log both as argv and as output.
	res, err := r.Run(context.Background(), Command{
		Args: []string{"-c", `echo "$0 $2"; echo "$0" >&2; exit 3`, "--password=hunter2", "--mfa-token", "654321"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || !strings.Contains(string(res.Stdout), "hunter2") {
		t.Fatalf("unexpected result %+v", res)
	}
	log := buf.String()
	if !strings.Contains(log, "exit=3") {
		t.Fatalf("log lacks exit code: %s", log)
	}
	if strings.Contains(log, "654321") || strings.Contains(log, "--password=hunter2") {
		t.Fatalf("secret flag value leaked into log: %s", log)
	}
}

func TestRedactArgs(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{[]string{"ls", "--proxy=x"}, []string{"ls", "--proxy=x"}},
		{[]string{"login", "--password=s3cret"}, []string{"login", "--password=[REDACTED]"}},
		{[]string{"login", "--mfa-token", "123456", "--user", "ali"}, []string{"login", "--mfa-token", "[REDACTED]", "--user", "ali"}},
		{[]string{"-otp=1"}, []string{"-otp=[REDACTED]"}},
		{[]string{"--piv-pin", "1234"}, []string{"--piv-pin", "[REDACTED]"}},
		// A flag after a valueless secret flag is still checked itself.
		{[]string{"--password-stdin", "--mfa-token", "123456"}, []string{"--password-stdin", "--mfa-token", "[REDACTED]"}},
		{[]string{"--password", "--mfa-token", "123456"}, []string{"--password", "[REDACTED]", "[REDACTED]"}},
		{[]string{"--password", "-starts-with-dash", "--user", "ali"}, []string{"--password", "[REDACTED]", "--user", "ali"}},
		{[]string{"--password-file", "/p", "--user", "ali"}, []string{"--password-file", "[REDACTED]", "--user", "ali"}},
		{nil, []string{}},
	}
	for _, tt := range tests {
		got := RedactArgs(tt.in)
		if strings.Join(got, " ") != strings.Join(tt.want, " ") || len(got) != len(tt.want) {
			t.Errorf("RedactArgs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSafeEnv(t *testing.T) {
	got := SafeEnv([]string{"HOME=/h", "TSTUI_PASSWORD=x", "TSTUI_MFA=1", "TSTUI_USER=ali", "TSTUI_PASSWORD_X=y"})
	want := "HOME=/h TSTUI_USER=ali TSTUI_PASSWORD_X=y"
	if strings.Join(got, " ") != want {
		t.Fatalf("SafeEnv = %q, want %q", got, want)
	}
}

func TestServerPing(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/webapi/ping" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"auth":{"type":"local","second_factor":"otp"},"server_version":"18.10.0","cluster_name":"afr-prod"}`))
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "https://")

	c := NewCLI(Options{HTTPClient: ts.Client(), Environ: []string{}})
	info, err := c.ServerPing(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.ServerVersion != "18.10.0" || info.ClusterName != "afr-prod" || info.SecondFactor != "otp" || info.Latency <= 0 {
		t.Fatalf("info = %+v", info)
	}

	// Without the test CA the self-signed cert must be rejected...
	if _, err := NewCLI(Options{Environ: []string{}}).ServerPing(context.Background(), addr); err == nil {
		t.Fatal("ping with an untrusted cert succeeded")
	}
	// ...unless --insecure is set.
	if _, err := NewCLI(Options{Insecure: true, Environ: []string{}}).ServerPing(context.Background(), addr); err != nil {
		t.Fatalf("insecure ping: %v", err)
	}
}

func TestServerPingHTTPError(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer ts.Close()
	c := NewCLI(Options{HTTPClient: ts.Client(), Environ: []string{}})
	_, err := c.ServerPing(context.Background(), strings.TrimPrefix(ts.URL, "https://"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want HTTP 502", err)
	}
}

func TestReadProfiles(t *testing.T) {
	home := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tp.afr.example.yaml", "web_proxy_addr: tp.afr.example:443\nuser: ali\ncluster: afr-prod\nmfa_mode: auto\n")
	write("tp.ast.example.yaml", "web_proxy_addr: tp.ast.example:3080\nuser: ali\ncluster: ast-prod\n")
	write("current-profile", "tp.ast.example\n")
	write("notes.txt", "ignored")

	got, err := ReadProfiles(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []Profile{
		{Name: "tp.afr.example", WebProxyAddr: "tp.afr.example:443", User: "ali", Cluster: "afr-prod", MFAMode: "auto"},
		{Name: "tp.ast.example", WebProxyAddr: "tp.ast.example:3080", User: "ali", Cluster: "ast-prod", Current: true},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	if got, err := ReadProfiles(filepath.Join(home, "missing")); err != nil || len(got) != 0 {
		t.Fatalf("missing home: %v, %v", got, err)
	}

	write("broken.yaml", "web_proxy_addr: [")
	if _, err := ReadProfiles(home); err == nil {
		t.Fatal("broken profile parsed without error")
	}
}

func TestDefaultHome(t *testing.T) {
	if got := DefaultHome([]string{"TELEPORT_HOME=/x/tsh"}); got != "/x/tsh" {
		t.Fatalf("got %q", got)
	}
	if got := DefaultHome(nil); !strings.HasSuffix(got, ".tsh") {
		t.Fatalf("got %q", got)
	}
}

func TestFake(t *testing.T) {
	f := &Fake{
		VersionInfo: VersionInfo{Version: "18.0.0"},
		Statuses:    map[string]Status{"p": {LoggedIn: true, User: "ali"}},
		Servers:     map[string][]Server{"p": {{Hostname: "web-01"}}},
	}
	var c Client = f
	ctx := context.Background()
	if s, _ := c.Status(ctx, "p"); s.User != "ali" {
		t.Fatalf("status = %+v", s)
	}
	if s, _ := c.ListServers(ctx, "p", ""); len(s) != 1 {
		t.Fatalf("servers = %+v", s)
	}
	f.Err = ErrNotLoggedIn
	if _, err := c.Version(ctx); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(f.Calls(), ","); got != "Status p,ListServers p,Version" {
		t.Fatalf("calls = %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCLIExec(t *testing.T) {
	c, argvLog := fakeCLI(t, "FAKE_TSH_EXEC_FAIL=db-03")
	ctx := context.Background()
	res, err := c.Exec(ctx, []string{"ssh", "--proxy=p", "ali@web-01", "uptime; rm -rf /tmp/x"}, ExecOptions{})
	if err != nil || res.ExitCode != 0 || CleanOutput(res.Stdout) != "web-01: uptime; rm -rf /tmp/x" {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	res, err = c.Exec(ctx, []string{"ssh", "ali@db-03", "ls"}, ExecOptions{})
	if err != nil || res.ExitCode != 3 || !strings.Contains(string(res.Stderr), "command failed") {
		t.Fatalf("failing host: %+v, %v", res, err)
	}
	// The child gets the command as one argv element, no shell in between.
	if log := readFile(t, argvLog); !strings.Contains(log, "argv: ssh --proxy=p ali@web-01 uptime; rm -rf /tmp/x |") {
		t.Fatalf("argv log:\n%s", log)
	}
	for _, secret := range []string{"hunter2", "123456", "TSTUI_PASSWORD", "TSTUI_MFA"} {
		if strings.Contains(readFile(t, argvLog), secret) {
			t.Errorf("child saw %q", secret)
		}
	}
}

func TestCLIExecTimeout(t *testing.T) {
	c, _ := fakeCLI(t, "FAKE_TSH_EXEC_SLEEP=5")
	start := time.Now()
	res, err := c.Exec(context.Background(), []string{"ssh", "ali@web-01", "ls"}, ExecOptions{Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) || res.ExitCode != -1 {
		t.Fatalf("Exec = %+v, %v; want timeout", res, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
}

func TestExecRunnerCapsOutputAndLogArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	var buf bytes.Buffer
	r := ExecRunner{Bin: "/bin/sh", Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	res, err := r.Run(context.Background(), Command{
		Args:      []string{"-c", `i=0; while [ $i -lt 100 ]; do echo 0123456789; i=$((i+1)); done; echo done >&2`, "remote-cmd-secret"},
		MaxOutput: 50,
		LogArgs:   []string{"-c", "[command]"},
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if len(res.Stdout) != 50 || !res.Truncated || string(res.Stderr) != "done\n" {
		t.Fatalf("stdout %d bytes, truncated %v, stderr %q", len(res.Stdout), res.Truncated, res.Stderr)
	}
	if strings.Contains(buf.String(), "remote-cmd-secret") || !strings.Contains(buf.String(), "[command]") {
		t.Fatalf("log should show LogArgs only: %s", buf.String())
	}
}
