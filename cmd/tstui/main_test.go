package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/root-ali/tstui/internal/config"
)

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    flags
		wantErr string
	}{
		{"none", nil, flags{}, ""},
		{"all", []string{
			"--proxy=tp.afr.example:443", "--user", "ali", "--password-stdin", "--mfa-token=123456",
			"--auth=okta", "--insecure", "--config=/c.yaml", "--skin=light",
		}, flags{
			proxy: "tp.afr.example:443", user: "ali", passwordStdin: true, mfaToken: "123456",
			authConnector: "okta", insecure: true, configPath: "/c.yaml", skin: "light",
		}, ""},
		{"single dash works too", []string{"-version"}, flags{version: true}, ""},
		{"two password sources", []string{"--password-stdin", "--password=x"}, flags{}, "only one of"},
		{"file and flag", []string{"--password-file=/p", "--password=x"}, flags{}, "only one of"},
		{"unknown flag", []string{"--nope"}, flags{}, "not defined"},
		{"positional", []string{"web-01"}, flags{}, "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlags(tt.args, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if *got != tt.want {
				t.Fatalf("got %+v\nwant %+v", *got, tt.want)
			}
		})
	}
}

func TestResolveCredentials(t *testing.T) {
	cfg, err := config.Parse(strings.NewReader(`
default_proxy: a-teleport
proxies:
  - {name: a-teleport, addr: "tp.afr.example:443", user: cfguser, auth: okta}
  - {name: b-teleport, addr: "tp.ast.example:443", user: buser}
`))
	if err != nil {
		t.Fatal(err)
	}

	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("filepass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loosePwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(loosePwFile, []byte("filepass"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name                  string
		args                  []string
		env                   map[string]string
		stdin                 string
		wantProxy, wantUser   string
		wantAuth              string
		wantPassword, wantOTP string
		wantWarning, wantErr  string
	}{
		{name: "config defaults",
			wantProxy: "tp.afr.example:443", wantUser: "cfguser", wantAuth: "okta"},
		{name: "env beats config",
			env:       map[string]string{"TSTUI_PROXY": "tp.x.example", "TSTUI_USER": "envuser", "TSTUI_PASSWORD": "envpass", "TSTUI_MFA": "111111"},
			wantProxy: "tp.x.example", wantUser: "envuser", wantPassword: "envpass", wantOTP: "111111"},
		{name: "flags beat env",
			args:      []string{"--proxy=tp.y.example", "--user=flaguser", "--mfa-token=222222", "--auth=github"},
			env:       map[string]string{"TSTUI_PROXY": "tp.x.example", "TSTUI_USER": "envuser", "TSTUI_MFA": "111111"},
			wantProxy: "tp.y.example", wantUser: "flaguser", wantAuth: "github", wantOTP: "222222"},
		{name: "proxy by config name picks its settings",
			args:      []string{"--proxy=b-teleport"},
			wantProxy: "tp.ast.example:443", wantUser: "buser"},
		{name: "proxy by config addr picks its settings",
			args:      []string{"--proxy=tp.ast.example:443"},
			wantProxy: "tp.ast.example:443", wantUser: "buser"},
		{name: "password from stdin",
			args: []string{"--password-stdin"}, stdin: "stdinpass\r\n",
			wantProxy: "tp.afr.example:443", wantUser: "cfguser", wantAuth: "okta", wantPassword: "stdinpass"},
		{name: "stdin beats env",
			args: []string{"--password-stdin"}, stdin: "stdinpass", env: map[string]string{"TSTUI_PASSWORD": "envpass"},
			wantProxy: "tp.afr.example:443", wantUser: "cfguser", wantAuth: "okta", wantPassword: "stdinpass"},
		{name: "empty stdin",
			args: []string{"--password-stdin"}, stdin: "\n", wantErr: "empty"},
		{name: "huge stdin",
			args: []string{"--password-stdin"}, stdin: strings.Repeat("x", maxPasswordLen+1), wantErr: "longer than"},
		{name: "password file",
			args:      []string{"--password-file=" + pwFile},
			wantProxy: "tp.afr.example:443", wantUser: "cfguser", wantAuth: "okta", wantPassword: "filepass"},
		{name: "loose password file warns",
			args:      []string{"--password-file=" + loosePwFile},
			wantProxy: "tp.afr.example:443", wantUser: "cfguser", wantAuth: "okta", wantPassword: "filepass",
			wantWarning: "readable by other users"},
		{name: "missing password file",
			args: []string{"--password-file=/nonexistent/pw"}, wantErr: "open password file"},
		{name: "--password warns",
			args:      []string{"--password=flagpass"},
			wantProxy: "tp.afr.example:443", wantUser: "cfguser", wantAuth: "okta", wantPassword: "flagpass",
			wantWarning: "shell history"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseFlags(tt.args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			c, warnings, err := resolveCredentials(f, cfg, envFunc(tt.env), strings.NewReader(tt.stdin))
			defer c.Wipe()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Proxy != tt.wantProxy || c.User != tt.wantUser || c.AuthConnector != tt.wantAuth {
				t.Errorf("proxy/user/auth = %q/%q/%q, want %q/%q/%q",
					c.Proxy, c.User, c.AuthConnector, tt.wantProxy, tt.wantUser, tt.wantAuth)
			}
			if got := string(c.Password.Bytes()); got != tt.wantPassword {
				t.Errorf("password = %q, want %q", got, tt.wantPassword)
			}
			if got := string(c.OTP.Bytes()); got != tt.wantOTP {
				t.Errorf("otp = %q, want %q", got, tt.wantOTP)
			}
			joined := strings.Join(warnings, "\n")
			if tt.wantWarning == "" && joined != "" || !strings.Contains(joined, tt.wantWarning) {
				t.Errorf("warnings = %q, want %q", joined, tt.wantWarning)
			}
			if f.password != "" || f.mfaToken != "" {
				t.Error("secret flag values were not cleared after use")
			}
		})
	}
}

func TestRunVersionAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, nil, &out, &errOut, envFunc(nil), nil); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "tstui ") {
		t.Fatalf("stdout = %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"--password=x", "--password-stdin"}, nil, &out, &errOut, envFunc(nil), nil); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if code := run([]string{"--help"}, nil, &out, &errOut, envFunc(nil), nil); code != exitOK {
		t.Fatalf("--help exit %d", code)
	}
	if !strings.Contains(errOut.String(), "TSTUI_PASSWORD") {
		t.Fatalf("usage lacks env vars: %s", errOut.String())
	}
}

// TestRunWithFakeTsh runs the debug report (--report) end to end against the fake tsh.
func TestRunWithFakeTsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tsh is a bash script")
	}
	useFakeTsh(t)

	tests := []struct {
		state   string
		want    []string
		notWant []string
	}{
		{"valid", []string{"client    tsh 18.11.3", "certificate valid for", "servers (5)", "clusters (3)", "hwb-prod", "web-01", "⟵ tunnel", "env=prod,role=web,uptime=up 3 weeks", "password=set"}, nil},
		{"expired", []string{"EXPIRED", "servers   skipped"}, []string{"web-01"}},
		{"logged-out", []string{"not logged in", "servers   skipped"}, []string{"web-01"}},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			home := t.TempDir()
			env := map[string]string{"HOME": home, "TSTUI_PASSWORD": "hunter2", "TSTUI_USER": "ali"}
			environ := []string{"PATH=" + os.Getenv("PATH"), "TELEPORT_HOME=" + home, "FAKE_TSH_STATE=" + tt.state, "TSTUI_PASSWORD=hunter2"}
			var out, errOut bytes.Buffer
			// 127.0.0.1:1 refuses connections, so the ping fails fast.
			code := run([]string{"--report", "--proxy=127.0.0.1:1"}, nil, &out, &errOut, envFunc(env), environ)
			if code != exitOK {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
			}
			all := out.String() + errOut.String()
			for _, w := range tt.want {
				if !strings.Contains(all, w) {
					t.Errorf("output lacks %q:\n%s", w, all)
				}
			}
			for _, w := range append(tt.notWant, "hunter2") {
				if strings.Contains(all, w) {
					t.Errorf("output contains %q:\n%s", w, all)
				}
			}
		})
	}
}

// useFakeTsh puts the fake tsh script first on PATH for the test. A test
// that gets past run's "is tsh installed" check needs it: CI runners have
// no real tsh, and with one installed the test would use it instead. exec
// looks the binary up in this process's PATH, not in a child's environment.
func useFakeTsh(t *testing.T) {
	t.Helper()
	fakeDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake-tsh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunBadConfig(t *testing.T) {
	useFakeTsh(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bogus_key: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"--config=" + path}, nil, &out, &errOut, envFunc(nil), nil); code != exitError {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "bogus_key") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// Without tsh on PATH, tstui says how to install it instead of opening a
// UI that cannot do anything; --version and --demo-shell need no tsh.
func TestTshMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if code := run([]string{"--config=" + cfg}, nil, &out, &errOut, envFunc(nil), nil); code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	for _, want := range []string{"tsh, Teleport's command-line client, is not on your PATH", "https://goteleport.com/download/", "tsh version"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
	errOut.Reset()
	if code := run([]string{"--version"}, nil, &out, &errOut, envFunc(nil), nil); code != exitOK {
		t.Fatalf("--version without tsh: exit %d, %s", code, errOut.String())
	}
}
