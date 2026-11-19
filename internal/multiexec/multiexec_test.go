package multiexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestForEachLimit(t *testing.T) {
	for _, limit := range []int{1, 3, 5, 20} {
		var cur, peak atomic.Int32
		var calls atomic.Int32
		ForEach(context.Background(), 12, limit, func(context.Context, int) {
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			cur.Add(-1)
			calls.Add(1)
		}, func(int) { t.Error("nothing should be skipped") })
		want := int32(min(limit, 12))
		if calls.Load() != 12 || peak.Load() != want {
			t.Errorf("limit %d: %d calls, peak concurrency %d (want %d)", limit, calls.Load(), peak.Load(), want)
		}
	}
}

func TestForEachCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran, skipped atomic.Int32
	ForEach(ctx, 10, 2, func(ctx context.Context, i int) {
		ran.Add(1)
		if i == 1 {
			cancel()
		}
		<-ctx.Done()
	}, func(int) { skipped.Add(1) })
	if ran.Load() != 2 || skipped.Load() != 8 {
		t.Fatalf("ran %d, skipped %d; want 2 and 8", ran.Load(), skipped.Load())
	}
}

func TestArgs(t *testing.T) {
	got, err := Args(Target{Proxy: "tp:443", Login: "ali", Host: "web-01"}, "  uptime; rm -f '/tmp/$(x)' && echo `id`  ", true)
	want := []string{"ssh", "--proxy=tp:443", "--insecure", "ali@web-01", "uptime; rm -f '/tmp/$(x)' && echo `id`"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Args = %q, %v", got, err)
	}
	got, err = Args(Target{Proxy: "tp:443", Cluster: "hwb.example", Login: "ali", Host: "web-01"}, "uptime", false)
	want = []string{"ssh", "--proxy=tp:443", "--cluster=hwb.example", "ali@web-01", "uptime"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Args with cluster = %q, %v", got, err)
	}
	if n := (Target{Cluster: "hwb.example", Host: "web-01"}).Name(); n != "hwb.example/web-01" {
		t.Fatalf("Name = %q", n)
	}
	if n := (Target{Host: "web-01"}).Name(); n != "web-01" {
		t.Fatalf("Name = %q", n)
	}
	bad := []struct {
		t   Target
		cmd string
	}{
		{Target{Login: "a", Host: "-oProxyCommand=x"}, "ls"},
		{Target{Login: "a", Host: "x y"}, "ls"},
		{Target{Login: "-l", Host: "h"}, "ls"},
		{Target{Login: "", Host: "h"}, "ls"},
		{Target{Login: "a", Host: "h"}, "   "},
		{Target{Login: "a", Host: "h"}, "--help"},
		{Target{Login: "a", Host: "h"}, "ls\x00"},
	}
	for _, b := range bad {
		if _, err := Args(b.t, b.cmd, false); err == nil {
			t.Errorf("Args(%+v, %q) succeeded", b.t, b.cmd)
		}
	}
}

type fakeExec struct {
	mu    sync.Mutex
	args  [][]string
	opts  []tsh.ExecOptions
	reply func(ctx context.Context, host string, o tsh.ExecOptions) (tsh.Result, error)
}

func (f *fakeExec) Exec(ctx context.Context, args []string, o tsh.ExecOptions) (tsh.Result, error) {
	f.mu.Lock()
	f.args = append(f.args, args)
	f.opts = append(f.opts, o)
	f.mu.Unlock()
	target := args[len(args)-2]
	return f.reply(ctx, target[strings.Index(target, "@")+1:], o)
}

func TestRun(t *testing.T) {
	ex := &fakeExec{reply: func(ctx context.Context, host string, o tsh.ExecOptions) (tsh.Result, error) {
		switch host {
		case "fail":
			return tsh.Result{ExitCode: 2, Stderr: []byte("oops\n")}, nil
		case "denied":
			return tsh.Result{ExitCode: 1, Stderr: []byte("\x1b[31mERROR: \x1b[0maccess denied to ali\n")}, nil
		case "slow":
			select {
			case <-time.After(o.Timeout):
				return tsh.Result{ExitCode: -1}, fmt.Errorf("%w after 1s", tsh.ErrTimeout)
			case <-ctx.Done():
				return tsh.Result{ExitCode: -1}, ctx.Err()
			}
		case "nobin":
			return tsh.Result{ExitCode: -1}, fmt.Errorf("%w: exec: tsh", tsh.ErrNotFound)
		}
		return tsh.Result{Stdout: []byte(host + " up\n")}, nil
	}}
	targets := []Target{{Login: "ali", Host: "ok"}, {Login: "ali", Host: "fail"}, {Login: "ali", Host: "denied"},
		{Login: "ali", Host: "slow"}, {Login: "ali", Host: "nobin"}, {Login: "ali", Host: "-bad"}}
	var mu sync.Mutex
	updates := map[int][]Status{}
	res := Run(context.Background(), ex, targets, "uptime", Options{Concurrency: 2, Timeout: 50 * time.Millisecond,
		OnUpdate: func(i int, r Result) { mu.Lock(); updates[i] = append(updates[i], r.Status); mu.Unlock() }})

	want := []Status{OK, Failed, Failed, TimedOut, Error, Error}
	for i, r := range res {
		if r.Status != want[i] {
			t.Errorf("%s: status %s, want %s (err %v)", r.Target.Host, r.Status, want[i], r.Err)
		}
		if got := updates[i]; len(got) != 2 || got[0] != Running || got[1] != r.Status {
			t.Errorf("%s: updates %v", r.Target.Host, got)
		}
	}
	if string(res[0].Stdout) != "ok up\n" || res[0].ExitCode != 0 {
		t.Errorf("ok result = %+v", res[0])
	}
	if res[1].ExitCode != 2 || string(res[1].Stderr) != "oops\n" {
		t.Errorf("fail result = %+v", res[1])
	}
	if res[2].Err == nil || res[2].Err.Error() != "access denied to ali" {
		t.Errorf("denied error = %v", res[2].Err)
	}
	// Every call had the timeout, the output cap, and the command kept out of the log.
	for _, o := range ex.opts {
		if o.Timeout != 50*time.Millisecond || o.MaxOutput != MaxOutput || o.LogArgs[len(o.LogArgs)-1] != "[command]" {
			t.Errorf("exec options %+v", o)
		}
	}
	if len(ex.args) != 5 {
		t.Errorf("%d tsh runs; the invalid host must not run", len(ex.args))
	}
}

func TestRunCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 10)
	ex := &fakeExec{reply: func(ctx context.Context, host string, _ tsh.ExecOptions) (tsh.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		return tsh.Result{ExitCode: -1}, ctx.Err()
	}}
	targets := make([]Target, 6)
	for i := range targets {
		targets[i] = Target{Login: "a", Host: fmt.Sprintf("h%d", i)}
	}
	done := make(chan []Result)
	go func() { done <- Run(ctx, ex, targets, "sleep 100", Options{Concurrency: 2}) }()
	<-started
	<-started
	cancel()
	select {
	case res := <-done:
		for _, r := range res {
			if r.Status != Cancelled {
				t.Errorf("%s: %s, want cancelled", r.Target.Host, r.Status)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRisky(t *testing.T) {
	tests := []struct {
		cmd  string
		want string // substring of a reason; "" means not risky
	}{
		{"uptime", ""},
		{"df -h; free -m", ""},
		{"rm /tmp/file", ""},
		{"rm -f /tmp/file", ""},
		{"ls -rf", ""},
		{"rm -rf /", "rm -r"},
		{"rm -fr /var/tmp/x", "rm -r"},
		{"rm -r -f x", "rm -r"},
		{"rm -R x", "rm -r"},
		{"rm --recursive --force x", "rm -r"},
		{"sudo rm -rf /opt/app", "rm -r"},
		{"/bin/rm -rf x", "rm -r"},
		{"find /tmp -name x | xargs rm -rf", "rm -r"},
		{`bash -c "rm -rf /"`, "rm -r"},
		{"cd /x && rm -rf *", "rm -r"},
		{"rm -- -r", ""}, // a file named -r
		{"shutdown -h now", "shutdown"},
		{"sudo reboot", "reboot"},
		{"echo hi; /sbin/poweroff", "poweroff"},
		{"halt", "halt"},
		{"systemctl reboot", "systemctl reboot"},
		{"systemctl restart nginx", ""},
		{"init 0", "init 0"},
		{"init 6", "init 6"},
		{"mkfs.ext4 /dev/sdb1", "mkfs"},
		{"mkfs -t xfs /dev/sdc", "mkfs"},
		{"dd if=/dev/zero of=/dev/sda bs=1M", "dd"},
		{"cat x > /dev/sda", "disk device"},
		{"echo > /dev/null", ""},
		{"wipefs -a /dev/sdb", "wipefs"},
		{"fdisk /dev/sda", "fdisk"},
		{":(){ :|:& };:", "fork bomb"},
		{"chmod -R 777 /", "chmod on /"},
		{"chmod -R 755 /srv/app", ""},
		{"chown -R nobody /", "chown on /"},
		{"kill -9 -1", "kill -1"},
		{"$(reboot)", "reboot"},
		{"`shutdown now`", "shutdown"},
	}
	for _, tt := range tests {
		got := Risky(tt.cmd)
		if tt.want == "" {
			if got != nil {
				t.Errorf("Risky(%q) = %q, want none", tt.cmd, got)
			}
			continue
		}
		if !strings.Contains(strings.Join(got, "|"), tt.want) {
			t.Errorf("Risky(%q) = %q, want a reason with %q", tt.cmd, got, tt.want)
		}
	}
	if got := Risky("reboot; rm -rf /"); len(got) != 2 {
		t.Errorf("two patterns should give two reasons, got %q", got)
	}
}

func TestAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "audit.log")
	a := NewAudit(path)
	start := AuditEntry{Time: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), Event: "start", Proxy: "tp:443", Login: "ali",
		Hosts: []string{"web-01", "web-02"}, Command: "rm -rf /tmp/x", Risky: Risky("rm -rf /tmp/x")}
	if err := a.Write(start); err != nil {
		t.Fatal(err)
	}
	res := []Result{{Target: Target{Host: "web-01"}, Status: OK}, {Target: Target{Host: "web-02"}, Status: Failed, ExitCode: 1,
		Stdout: []byte("SECRET-OUTPUT")}}
	if err := a.Write(FinishEntry(start, res, start.Time.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("audit file: %v %v", st, err)
	}
	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte("SECRET-OUTPUT")) {
		t.Fatal("output must not be audited")
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("audit log:\n%s", b)
	}
	var fin AuditEntry
	if err := json.Unmarshal(lines[1], &fin); err != nil {
		t.Fatal(err)
	}
	if fin.Event != "finish" || fin.Command != "rm -rf /tmp/x" || fin.Results["web-02"] != "failed exit=1" ||
		!reflect.DeepEqual(fin.Hosts, []string{"web-01", "web-02"}) || len(fin.Risky) == 0 {
		t.Fatalf("finish entry = %+v", fin)
	}
	if err := NewAudit("").Write(start); err != nil {
		t.Fatal(err)
	}
}

func TestExport(t *testing.T) {
	var b bytes.Buffer
	res := []Result{
		{Target: Target{Login: "ali", Host: "web-01"}, Status: OK, Stdout: []byte("up 3 days")},
		{Target: Target{Login: "ali", Host: "db-03"}, Status: Failed, ExitCode: 2, Stderr: []byte("no\n"), Truncated: true,
			Err: fmt.Errorf("boom")},
	}
	if err := Export(&b, "uptime", time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), res); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"# command: uptime", "=== ali@web-01  status=ok exit=0", "--- stdout\nup 3 days\n",
		"=== ali@db-03  status=failed exit=2", "error=boom", "--- stderr\nno\n", "(output truncated)"} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q:\n%s", want, out)
		}
	}
}
