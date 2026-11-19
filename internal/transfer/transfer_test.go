package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestArgs(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home := "/home/me"
	base := Spec{Proxy: "tp.example:443", Login: "ali", Host: "web-01"}
	with := func(f func(*Spec)) Spec { s := base; f(&s); return s }
	tests := []struct {
		name string
		spec Spec
		want []string
		err  string
	}{
		{"upload", with(func(s *Spec) { s.Local, s.Remote = "/tmp/a.txt", "/srv/a.txt" }),
			[]string{"scp", "--proxy=tp.example:443", "/tmp/a.txt", "ali@web-01:/srv/a.txt"}, ""},
		{"download recursive insecure", with(func(s *Spec) {
			s.Direction, s.Local, s.Remote, s.Recursive, s.Insecure = Download, "/tmp/logs", "/var/log/app", true, true
		}), []string{"scp", "--proxy=tp.example:443", "--insecure", "--recursive", "ali@web-01:/var/log/app", "/tmp/logs"}, ""},
		{"leaf cluster", with(func(s *Spec) { s.Local, s.Cluster = "/tmp/a", "hwb.example" }),
			[]string{"scp", "--proxy=tp.example:443", "--cluster=hwb.example", "/tmp/a", "ali@web-01:"}, ""},
		{"empty remote is home", with(func(s *Spec) { s.Local = "/tmp/a" }),
			[]string{"scp", "--proxy=tp.example:443", "/tmp/a", "ali@web-01:"}, ""},
		{"tilde", with(func(s *Spec) { s.Local = "~/x y/z" }),
			[]string{"scp", "--proxy=tp.example:443", "/home/me/x y/z", "ali@web-01:"}, ""},
		// A local name that looks like a flag becomes an absolute path.
		{"dash local", with(func(s *Spec) { s.Local = "-rf" }),
			[]string{"scp", "--proxy=tp.example:443", filepath.Join(cwd, "-rf"), "ali@web-01:"}, ""},
		// Shell metacharacters are just bytes in one argv element.
		{"nasty remote", with(func(s *Spec) { s.Local, s.Remote = "/tmp/a", "$(reboot); `id` 'x' \"y\" *.log -rf" }),
			[]string{"scp", "--proxy=tp.example:443", "/tmp/a", "ali@web-01:$(reboot); `id` 'x' \"y\" *.log -rf"}, ""},
		{"nasty local", with(func(s *Spec) { s.Local = "/tmp/$(rm -rf ~); x|y&z" }),
			[]string{"scp", "--proxy=tp.example:443", "/tmp/$(rm -rf ~); x|y&z", "ali@web-01:"}, ""},
		{"dots cleaned", with(func(s *Spec) { s.Local = "/tmp/../tmp/./a" }),
			[]string{"scp", "--proxy=tp.example:443", "/tmp/a", "ali@web-01:"}, ""},
		{"no proxy", Spec{Login: "ali", Host: "h", Local: "/a"}, []string{"scp", "/a", "ali@h:"}, ""},

		{"colon in local", with(func(s *Spec) { s.Local = "/tmp/a:b" }), nil, "contains ':'"},
		{"empty local", with(func(s *Spec) { s.Local = " " }), nil, "no local path"},
		{"newline local", with(func(s *Spec) { s.Local = "/tmp/a\nb" }), nil, "control characters"},
		{"newline remote", with(func(s *Spec) { s.Local, s.Remote = "/a", "x\ny" }), nil, "control characters"},
		{"dash host", with(func(s *Spec) { s.Host, s.Local = "-oProxyCommand=x", "/a" }), nil, "invalid server"},
		{"host with colon", with(func(s *Spec) { s.Host, s.Local = "h:/etc", "/a" }), nil, "invalid server"},
		{"host with at", with(func(s *Spec) { s.Host, s.Local = "root@h", "/a" }), nil, "invalid server"},
		{"dash login", with(func(s *Spec) { s.Login, s.Local = "-l", "/a" }), nil, "invalid login"},
		{"login with space", with(func(s *Spec) { s.Login, s.Local = "a b", "/a" }), nil, "invalid login"},
		{"no host", with(func(s *Spec) { s.Host, s.Local = "", "/a" }), nil, "no server"},
		{"no login", with(func(s *Spec) { s.Login, s.Local = "", "/a" }), nil, "no login"},
		{"tilde without home", with(func(s *Spec) { s.Local = "~/a" }), nil, "no home"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := home
			if tt.name == "tilde without home" {
				h = ""
			}
			got, err := Args(tt.spec, h)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("Args error = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Args =\n %q\nwant\n %q", got, tt.want)
			}
			// No element after the subcommand's flags may start with "-".
			for _, a := range got[len(got)-2:] {
				if strings.HasPrefix(a, "-") {
					t.Fatalf("positional argument %q looks like a flag", a)
				}
			}
		})
	}
}

type fakeExec struct {
	args []string
	res  tsh.Result
	err  error
}

func (f *fakeExec) Exec(_ context.Context, args []string, _ tsh.ExecOptions) (tsh.Result, error) {
	f.args = args
	return f.res, f.err
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	ex := &fakeExec{}
	if err := Run(context.Background(), ex, Spec{Login: "a", Host: "h", Local: dir}, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ex.args, []string{"scp", "--recursive", dir, "a@h:"}) {
		t.Fatalf("directory upload args = %q (want --recursive added)", ex.args)
	}

	ex = &fakeExec{res: tsh.Result{ExitCode: 1, Stderr: []byte("\x1b[31mERROR: \x1b[0mopen /x: no such file or directory\n")}}
	err := Run(context.Background(), ex, Spec{Direction: Download, Login: "a", Host: "h", Local: dir, Remote: "/x"}, "")
	if err == nil || err.Error() != "open /x: no such file or directory" {
		t.Fatalf("error = %v", err)
	}

	ex = &fakeExec{res: tsh.Result{ExitCode: 2}}
	if err := Run(context.Background(), ex, Spec{Login: "a", Host: "h", Local: "/a"}, ""); err == nil || !strings.Contains(err.Error(), "status 2") {
		t.Fatalf("silent failure error = %v", err)
	}

	ex = &fakeExec{err: errors.New("boom")}
	if err := Run(context.Background(), ex, Spec{Login: "a", Host: "-h", Local: "/a"}, ""); err == nil || ex.args != nil {
		t.Fatalf("invalid spec must not run tsh: err %v, args %q", err, ex.args)
	}
}

func TestLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "transfers.log")
	l := NewLog(path)
	for i := range 60 {
		r := Record{Spec: Spec{Login: "a", Host: "h", Local: "/l", Remote: "/r"}}
		if i == 59 {
			r.Err = errors.New("denied")
		}
		if err := l.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(l.Records()); n != maxLogInMemory {
		t.Fatalf("kept %d records", n)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %o", st.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 60 || !strings.Contains(lines[0], "upload /l -> a@h:/r") || !strings.HasSuffix(lines[59], "error: denied") {
		t.Fatalf("log file:\n%s", b)
	}
}

func TestCompleteLocal(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"docs", "data", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"deploy.sh", "notes.txt", ".env"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "docs"), filepath.Join(dir, "dlink")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		input string
		want  []string
	}{
		{dir + "/d", []string{dir + "/data/", dir + "/deploy.sh", dir + "/dlink/", dir + "/docs/"}},
		{dir + "/do", []string{dir + "/docs/"}},
		{dir + "/", []string{dir + "/data/", dir + "/deploy.sh", dir + "/dlink/", dir + "/docs/", dir + "/notes.txt"}},
		{dir + "/.", []string{dir + "/.env", dir + "/.hidden/"}},
		{"~/d", []string{"~/data/", "~/deploy.sh", "~/dlink/", "~/docs/"}},
		{"~", []string{"~/"}},
		{dir + "/missing/x", nil},
	}
	for _, tt := range tests {
		if got := CompleteLocal(tt.input, dir); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CompleteLocal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
