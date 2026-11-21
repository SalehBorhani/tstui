package ui

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/multiexec"
	"github.com/root-ali/tstui/internal/session"
	"github.com/root-ali/tstui/internal/tsh"
)

var extraServers = map[string][]tsh.Server{profileProxy: {
	{Hostname: "web-01", Addr: "10.0.0.1:3022", Labels: map[string]string{"env": "prod"}, Expires: time.Now().Add(time.Hour)},
	{Hostname: "web-02", Labels: map[string]string{"env": "stage"}},
	{Hostname: "db-03", Labels: map[string]string{"env": "prod"}, Expires: time.Now().Add(-time.Hour)},
}}

// execRecorder serves tsh.Fake.Exec and records each argv.
type execRecorder struct {
	mu   sync.Mutex
	runs [][]string
}

func (r *execRecorder) exec(_ context.Context, args []string, _ tsh.ExecOptions) (tsh.Result, error) {
	r.mu.Lock()
	r.runs = append(r.runs, append([]string(nil), args...))
	r.mu.Unlock()
	if args[0] == "ssh" {
		host := args[len(args)-2]
		return tsh.Result{Stdout: []byte(host + ": done\n")}, nil
	}
	return tsh.Result{}, nil
}

func (r *execRecorder) all() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.runs...)
}

type extrasHarness struct {
	*sessionHarness
	rec   *execRecorder
	state string
	cfg   string
}

// newExtrasHarness runs the app logged in, with three servers, a fake
// Exec, a config file that names the proxy, and a state directory.
func newExtrasHarness(t *testing.T, logins []string, setup func(*tsh.Fake, *Options)) *extrasHarness {
	t.Helper()
	dir := t.TempDir()
	h := &extrasHarness{rec: &execRecorder{}, state: filepath.Join(dir, "state"), cfg: filepath.Join(dir, "config.yaml")}
	cfg := config.Default()
	cfg.Proxies = []config.Proxy{{Name: "ast", Addr: profileProxy}}
	if err := config.Save(h.cfg, cfg); err != nil {
		t.Fatal(err)
	}
	fake := &tsh.Fake{ProfileList: profiles(), Servers: extraServers, ExecFunc: h.rec.exec}
	st := validStatus(profileUser)
	st.Logins = logins
	fake.SetStatus(profileProxy, st)
	sp := &session.Fake{}
	fake.PTY = func(ctx context.Context, args []string, o tsh.PTYOptions) (tsh.Process, error) {
		return sp.Spawn(ctx, session.Spec{}, o)
	}
	o := Options{Config: cfg, ConfigPath: h.cfg, StateDir: h.state, Spawner: sp, Home: dir}
	if setup != nil {
		setup(fake, &o)
	}
	h.sessionHarness = &sessionHarness{appHarness: newAppHarness(t, fake, o), sp: sp}
	h.eventually("server list", func() bool { return h.app.serversLoaded })
	h.screen.SetSize(140, 40)
	h.app.tv.QueueUpdateDraw(func() {})
	return h
}

func (h *extrasHarness) command(line string) {
	h.typeText(":")
	h.eventually("palette open", func() bool { return h.app.paletteOpen })
	h.typeText(line)
	h.key(tcell.KeyEnter)
}

// visible lists the shown servers; call it on the UI goroutine (inside
// eventually or ui).
func (h *extrasHarness) visible() []string {
	var out []string
	for _, s := range h.app.list.servers {
		out = append(out, s.Hostname)
	}
	return out
}

func TestPaletteFilterCompletionHistory(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.command("filter env=prod")
	h.eventually("filtered", func() bool { return strings.Join(h.visible(), ",") == "web-01,db-03" })
	h.waitScreen("[2/3]")
	h.waitScreen("/env=prod")

	// Tab completes a command name, then a host.
	h.typeText(":")
	h.typeText("ss")
	h.key(tcell.KeyTab)
	h.eventually("command completed", func() bool { return h.app.cmdLine.GetText() == "ssh " })
	h.typeText("db")
	h.key(tcell.KeyTab)
	h.eventually("host completed", func() bool { return h.app.cmdLine.GetText() == "ssh db-03" })
	h.key(tcell.KeyEscape)
	h.eventually("palette closed", func() bool { return !h.app.paletteOpen })

	// Up recalls the last command run; Enter on it clears the filter here.
	h.command("filter")
	h.eventually("filter cleared", func() bool { return len(h.visible()) == 3 })
	h.typeText(":")
	h.key(tcell.KeyUp)
	h.eventually("history", func() bool { return h.app.cmdLine.GetText() == "filter" })
	h.key(tcell.KeyUp)
	h.eventually("older history", func() bool { return h.app.cmdLine.GetText() == "filter env=prod" })
	h.key(tcell.KeyEscape)

	// An unknown command is reported in the footer.
	h.command("sssh web-01")
	h.waitScreen("did you mean ssh?")
}

func TestInteractiveSearch(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.eventually("listed", func() bool { return len(h.visible()) == 3 })

	// Every key narrows the list; no Enter needed.
	h.typeText("/")
	h.eventually("search open", func() bool { return h.app.searchOpen })
	h.waitScreen("Search")
	h.typeText("w")
	h.eventually("narrowed on w", func() bool { return strings.Join(h.visible(), ",") == "web-01,web-02" })
	h.typeText("eb-02")
	h.eventually("narrowed on web-02", func() bool { return strings.Join(h.visible(), ",") == "web-02" })
	h.key(tcell.KeyCtrlU)
	h.eventually("widened again", func() bool { return len(h.visible()) == 3 })
	h.typeText("env=prod")
	h.eventually("label filter live", func() bool { return strings.Join(h.visible(), ",") == "web-01,db-03" })
	h.waitScreen("2/3")

	// Esc puts back the filter from before the search.
	h.key(tcell.KeyEscape)
	h.eventually("restored", func() bool { return !h.app.searchOpen && len(h.visible()) == 3 })

	// Tab keeps the filter; / reopens with it typed in.
	h.typeText("/")
	h.eventually("search open", func() bool { return h.app.searchOpen })
	h.typeText("db")
	h.key(tcell.KeyTab)
	h.eventually("kept", func() bool { return !h.app.searchOpen && strings.Join(h.visible(), ",") == "db-03" })
	h.typeText("/")
	h.eventually("prefilled", func() bool { return h.app.cmdLine.GetText() == "db" })
	h.key(tcell.KeyEscape)
	h.eventually("still kept", func() bool { return strings.Join(h.visible(), ",") == "db-03" })

	// The arrows move the list while typing; Enter connects to the highlighted server.
	h.typeText("/")
	h.eventually("search open", func() bool { return h.app.searchOpen })
	h.key(tcell.KeyCtrlU)
	h.typeText("env=prod")
	h.eventually("two left", func() bool { return len(h.visible()) == 2 })
	h.key(tcell.KeyDown)
	h.eventually("cursor moved", func() bool { return h.app.list.selectedHost() == "db-03" })
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	if spec := h.sp.Spec(0); spec.Host != "db-03" {
		t.Fatalf("connected to %q, want db-03", spec.Host)
	}
}

func TestPaletteSSH(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.command("ssh db-03")
	h.waitSpawned(1)
	if spec := h.sp.Spec(0); spec.Host != "db-03" || spec.Login != "ali" {
		t.Fatalf("spec %+v", spec)
	}
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.command("ssh -oProxyCommand=x")
	h.waitScreen(`Invalid server name "-oProxyCommand=x"`)
	if h.sp.Count() != 1 {
		t.Fatal("an invalid host must not spawn")
	}
}

func TestFavoritesAndMarks(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	// web-01 is selected first; move to db-03 and star it: it moves to the top.
	h.key(tcell.KeyDown)
	h.key(tcell.KeyDown)
	h.typeText("f")
	h.eventually("favorite pinned first", func() bool { return h.visible()[0] == "db-03" })
	h.waitScreen("Added favorite db-03.")
	b, err := os.ReadFile(h.cfg)
	if err != nil || !strings.Contains(string(b), "favorites:\n  ast:\n    - db-03") {
		t.Fatalf("favorite not saved to the config:\n%s (%v)", b, err)
	}
	if _, err := os.Stat(h.cfg + ".bak"); err != nil {
		t.Fatalf("no backup of the previous config: %v", err)
	}

	// Space marks and moves down; a marks all, a again unmarks all.
	h.typeText(" ")
	h.eventually("one mark", func() bool { return len(h.app.list.markedHosts()) == 1 })
	h.waitScreen("✓1")
	h.typeText("a")
	h.eventually("all marked", func() bool { return len(h.app.list.markedHosts()) == 3 })
	h.typeText("a")
	h.eventually("all unmarked", func() bool { return len(h.app.list.markedHosts()) == 0 })
	// a marks only what the filter shows.
	h.command("filter web")
	h.eventually("filtered", func() bool { return len(h.visible()) == 2 })
	h.typeText("a")
	h.eventually("filtered marked", func() bool { return len(h.app.list.markedHosts()) == 2 })
	h.key(tcell.KeyEscape)
	h.eventually("esc clears marks", func() bool { return len(h.app.list.markedHosts()) == 0 })
}

func TestSortKeys(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	order := func() string { return strings.Join(h.visible(), ",") }
	h.typeText("N")
	h.eventually("sorted by name", func() bool { return order() == "db-03,web-01,web-02" })
	h.waitScreen("Sorted by name, ascending.")
	h.waitScreen("NAME↑")
	h.typeText("N")
	h.eventually("name reversed", func() bool { return order() == "web-02,web-01,db-03" })
	h.waitScreen("NAME↓")
	// Only web-01 has an address; the tunnel nodes follow, by name.
	h.typeText("A")
	h.eventually("sorted by address", func() bool { return order() == "web-01,db-03,web-02" })
	h.command("sort off")
	h.eventually("tsh order", func() bool { return order() == "web-01,web-02,db-03" })
	h.command("sort name")
	h.eventually("palette sort", func() bool { return order() == "db-03,web-01,web-02" })
}

func TestReachabilityDots(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.ui(func() {
		got := map[string]string{}
		for row := 1; row <= 3; row++ {
			got[h.app.list.table.GetCell(row, 1).Text] = h.app.list.table.GetCell(row, 3).Text
		}
		if got["web-01"] != "●" || got["db-03"] != "○" || got["web-02"] != "·" {
			t.Errorf("dots = %v", got)
		}
	})
}

func readAudit(t *testing.T, path string) []multiexec.AuditEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []multiexec.AuditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e multiexec.AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestMultiExec(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.typeText("a") // mark all three
	h.command("exec uptime")
	h.waitScreen("<Confirm multi-exec>")
	h.waitScreen("• db-03")
	h.key(tcell.KeyEnter) // Run
	h.waitScreen("finished: 3 ok, 0 not ok")
	h.waitScreen("web-01: done") // output of the selected host

	var sshRuns []string
	for _, r := range h.rec.all() {
		sshRuns = append(sshRuns, strings.Join(r, " "))
	}
	if len(sshRuns) != 3 || !strings.Contains(strings.Join(sshRuns, "\n"), "ssh --proxy="+profileProxy+" ali@db-03 uptime") {
		t.Fatalf("tsh runs:\n%s", strings.Join(sshRuns, "\n"))
	}
	audit := readAudit(t, filepath.Join(h.state, "audit.log"))
	if len(audit) != 2 || audit[0].Event != "start" || audit[1].Event != "finish" || audit[0].Command != "uptime" ||
		len(audit[0].Hosts) != 3 || audit[1].Results["web-02"] != "ok exit=0" {
		t.Fatalf("audit = %+v", audit)
	}
	if st, _ := os.Stat(filepath.Join(h.state, "audit.log")); st.Mode().Perm() != 0o600 {
		t.Fatalf("audit log mode %o", st.Mode().Perm())
	}

	// Export to a file.
	h.typeText("e")
	h.waitScreen("<Export results>")
	out := filepath.Join(t.TempDir(), "out.txt")
	h.ui(func() {
		in, ok := h.app.tv.GetFocus().(*tview.InputField)
		if !ok {
			t.Errorf("focus is %T, want the path field", h.app.tv.GetFocus())
			return
		}
		in.SetText(out)
	})
	h.key(tcell.KeyEnter)
	h.waitScreen("Results exported to")
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), "=== ali@db-03  status=ok") {
		t.Fatalf("export: %v\n%s", err, b)
	}
	h.key(tcell.KeyEscape)
	h.eventually("view closed", func() bool { return h.app.dialog == nil })
}

func TestMultiExecRiskyNeedsYes(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.command("exec rm -rf /tmp/cache")
	h.waitScreen("DANGEROUS COMMAND")
	h.waitScreen("recursive delete")
	// Enter in the yes field without typing yes does not run it.
	h.key(tcell.KeyEnter)
	h.waitScreen("Type yes in the field to confirm.")
	if len(h.rec.all()) != 0 {
		t.Fatal("risky command ran without yes")
	}
	if len(readAudit(t, filepath.Join(h.state, "audit.log"))) != 0 {
		t.Fatal("nothing should be audited before confirmation")
	}
	h.typeText("yes")
	h.key(tcell.KeyEnter)
	h.waitScreen("finished: 1 ok")
	runs := h.rec.all()
	if len(runs) != 1 || runs[0][len(runs[0])-1] != "rm -rf /tmp/cache" || runs[0][len(runs[0])-2] != "ali@web-01" {
		t.Fatalf("runs = %q", runs)
	}
	if a := readAudit(t, filepath.Join(h.state, "audit.log")); len(a) != 2 || len(a[0].Risky) == 0 {
		t.Fatalf("audit = %+v", a)
	}
}

func TestMultiExecPicksLogin(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali", "root"}, nil)
	h.command("exec id")
	h.waitScreen("<Login for 1 server(s)>")
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitScreen("Run as root on 1 server(s)")
	h.key(tcell.KeyEscape)
	h.eventually("cancelled", func() bool { return h.app.dialog == nil })
	if len(h.rec.all()) != 0 {
		t.Fatal("cancelled exec ran")
	}
}

func TestSCPDialog(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	local := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.typeText("u")
	h.waitScreen("<Copy files> web-01")
	h.eventually("focus on local path", func() bool { return h.app.tv.GetFocus() != nil })
	h.typeText(local)
	h.key(tcell.KeyEscape) // close the completion list, if any
	h.key(tcell.KeyTab)
	h.typeText("/srv/notes.txt")
	h.key(tcell.KeyTab) // recursive
	h.key(tcell.KeyTab) // Start
	h.key(tcell.KeyEnter)
	h.waitScreen("upload finished")
	runs := h.rec.all()
	want := "scp --proxy=" + profileProxy + " " + local + " ali@web-01:/srv/notes.txt"
	if len(runs) != 1 || strings.Join(runs[0], " ") != want {
		t.Fatalf("runs = %q, want %q", runs, want)
	}
	b, _ := os.ReadFile(filepath.Join(h.state, "transfers.log"))
	if !strings.Contains(string(b), "upload "+local+" -> ali@web-01:/srv/notes.txt") {
		t.Fatalf("transfer log:\n%s", b)
	}
}

func TestPortForward(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback")
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.command("pf")
	h.waitScreen("No port forwards. Press n to create one.")
	h.typeText("n")
	h.waitScreen("<New port forward>")
	h.typeText(strconv.Itoa(port) + ":localhost:5432")
	h.key(tcell.KeyTab)
	h.typeText("pg")
	h.key(tcell.KeyTab)
	h.typeText(" ") // save in config
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter) // Start
	h.waitScreen("127.0.0.1:" + strconv.Itoa(port))
	h.waitScreen("starting")
	h.waitSpawned(1)
	b, _ := os.ReadFile(h.cfg)
	if !strings.Contains(string(b), "forward: 127.0.0.1:"+strconv.Itoa(port)+":localhost:5432") {
		t.Fatalf("saved forward missing from the config:\n%s", b)
	}
	if !strings.Contains(strings.Join(h.fake.Calls(), "\n"), "StartPTY ssh --proxy="+profileProxy+" -N --forward=127.0.0.1:"+strconv.Itoa(port)+":localhost:5432 ali@web-01") {
		t.Fatalf("calls:\n%s", strings.Join(h.fake.Calls(), "\n"))
	}
	// A second forward on the same port is refused.
	h.typeText("n")
	h.waitScreen("<New port forward>")
	h.typeText(strconv.Itoa(port) + ":localhost:80")
	h.key(tcell.KeyTab)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.waitScreen("local port in use")
	h.key(tcell.KeyEscape)
	h.waitScreen("<Port forwards>")
	h.key(tcell.KeyEscape)

	// Quitting asks first, then stops the forward (goleak checks the rest).
	h.typeText("q")
	h.waitScreen("1 port forward is still open.")
	h.ctrl(tcell.KeyCtrlC)
	select {
	case err := <-h.runErr:
		h.runErr <- err
	case <-time.After(5 * time.Second):
		t.Fatal("app did not quit")
	}
	if !h.sp.Proc(0).Exited() {
		t.Fatal("forward still running after quit")
	}
}

func TestExpiryToastAndRelogin(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, func(f *tsh.Fake, _ *Options) {
		st := validStatus(profileUser)
		st.ValidUntil = time.Now().Add(20 * time.Minute)
		st.Logins = []string{"ali"}
		f.SetStatus(profileProxy, st)
	})
	h.waitScreen("Certificate expires in")
	h.waitScreen("press L to log in again")
	h.typeText("L")
	h.eventually("login dialog", h.modalOpen)
	h.typeText("pw")
	h.key(tcell.KeyEnter)
	h.eventually("relogged and toast gone", func() bool {
		ok := !h.modalOpen()
		_, _, shown := h.app.currentToast()
		return ok && !shown && time.Until(h.app.info.ValidUntil) > time.Hour
	})
}

func TestHealthVersionWarning(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, func(f *tsh.Fake, _ *Options) {
		f.SetPing(profileProxy, tsh.PingInfo{ServerVersion: "20.1.0", MinClientVersion: "19.0.0", Latency: 23 * time.Millisecond})
	})
	h.waitScreen("⚠ tsh v18 too old for server v20")                 // header
	h.waitScreen("tsh v18.11.3 is too old for the server (v20.1.0)") // notice, once
	h.waitScreen("23ms")
}

func TestHelpAndSkin(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.typeText("?")
	h.waitScreen("Server list")
	h.waitScreen("mark all shown servers")
	h.key(tcell.KeyEscape)
	h.eventually("help closed", func() bool { return h.app.dialog == nil })
	h.command("skin light")
	h.waitScreen("Skin light.")
	h.ui(func() {
		if h.app.theme.Title != LightTheme().Title {
			t.Error("skin not applied")
		}
	})
	b, _ := os.ReadFile(h.cfg)
	if !strings.Contains(string(b), "skin: light") {
		t.Fatalf("skin not saved:\n%s", b)
	}
	h.command("skin nope")
	h.waitScreen(`Unknown skin "nope"`)
}

func TestProxyPicker(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.typeText("p")
	h.waitScreen("<Proxies>")
	h.waitScreen("[x] ast (" + profileProxy + ")")
	h.key(tcell.KeyEscape)
	h.eventually("closed", func() bool { return h.app.dialog == nil })
	h.command("proxy other.example:443")
	h.eventually("switched", func() bool { return h.app.info.Proxy == "other.example:443" })
}
