package health

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestCheckVersions(t *testing.T) {
	tests := []struct {
		client, server, min string
		want                CompatLevel
		msg                 string
	}{
		{"18.11.3", "18.10.0", "17.0.0-aa", CompatOK, ""}, // verified pair (tsh-notes)
		{"17.4.0", "18.10.0", "17.0.0-aa", CompatOK, ""},  // N-1 is supported
		{"17.0.0", "18.0.0", "17.0.0-aa", CompatOK, ""},   // release > its pre-release
		{"16.4.2", "18.10.0", "", CompatWarn, "too old"},  // N-2
		{"19.0.0", "18.10.0", "", CompatWarn, "newer"},    // newer major
		{"v18.0.0", "v18.9.9", "", CompatOK, ""},          // minor/patch do not matter
		{"17.1.0", "18.1.0", "17.2.0", CompatWarn, "minimum"},
		{"", "18.1.0", "", CompatUnknown, "unknown"},
		{"18.1.0", "", "", CompatUnknown, "unknown"},
		{"not-a-version", "18.1.0", "", CompatUnknown, "unknown"},
		{"18.1.0", "18.1.0", "garbage", CompatOK, ""}, // unreadable minimum is ignored
	}
	for _, tt := range tests {
		got := CheckVersions(tt.client, tt.server, tt.min)
		if got.Level != tt.want || !strings.Contains(got.Message, tt.msg) {
			t.Errorf("CheckVersions(%q, %q, %q) = %+v; want level %d with %q", tt.client, tt.server, tt.min, got, tt.want, tt.msg)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		err  bool
	}{
		{"18.10.0", Version{18, 10, 0, ""}, false},
		{"v17.0.0-aa", Version{17, 0, 0, "aa"}, false},
		{"18", Version{18, 0, 0, ""}, false},
		{"18.1.2+build.5", Version{18, 1, 2, ""}, false},
		{"", Version{}, true},
		{"1.2.3.4", Version{}, true},
		{"x.1", Version{}, true},
		{"-1.0.0", Version{}, true},
	}
	for _, tt := range tests {
		got, err := ParseVersion(tt.in)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("ParseVersion(%q) = %+v, %v", tt.in, got, err)
		}
	}
	ordered := []string{"16.9.9", "17.0.0-aa", "17.0.0-ab", "17.0.0", "17.0.1", "17.1.0", "18.0.0"}
	for i := 1; i < len(ordered); i++ {
		a, _ := ParseVersion(ordered[i-1])
		b, _ := ParseVersion(ordered[i])
		if !a.Less(b) || b.Less(a) {
			t.Errorf("want %s < %s", ordered[i-1], ordered[i])
		}
	}
}

func TestLevelFor(t *testing.T) {
	for left, want := range map[time.Duration]ExpiryLevel{
		8 * time.Hour:    ExpiryOK,
		30 * time.Minute: ExpiryOK,
		29 * time.Minute: ExpirySoon,
		5 * time.Minute:  ExpirySoon,
		4 * time.Minute:  ExpiryImminent,
		0:                ExpiryPast,
		-time.Hour:       ExpiryPast,
	} {
		if got := LevelFor(left); got != want {
			t.Errorf("LevelFor(%s) = %d, want %d", left, got, want)
		}
	}
}

func TestExpiryAlerts(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	steps := []struct {
		at    time.Duration // after now
		until time.Time
		want  ExpiryLevel
	}{
		{0, until, ExpiryOK},
		{29*time.Minute + 59*time.Second, until, ExpiryOK},
		{31 * time.Minute, until, ExpirySoon}, // 29m left: fires once
		{40 * time.Minute, until, ExpiryOK},
		{56 * time.Minute, until, ExpiryImminent},
		{58 * time.Minute, until, ExpiryOK},
		{60 * time.Minute, until, ExpiryPast},
		{61 * time.Minute, until, ExpiryOK}, // already said
		// Re-login: a new 8h certificate resets; later thresholds fire again.
		{62 * time.Minute, now.Add(9 * time.Hour), ExpiryOK},
		{9*time.Hour - 20*time.Minute, now.Add(9 * time.Hour), ExpirySoon},
		// Logged out resets too.
		{9 * time.Hour, time.Time{}, ExpiryOK},
		// Starting at 4 minutes left only fires the 5-minute alert.
		{10 * time.Hour, now.Add(10*time.Hour + 4*time.Minute), ExpiryImminent},
	}
	var a ExpiryAlerts
	for i, s := range steps {
		if got := a.Update(s.until, now.Add(s.at)); got != s.want {
			t.Errorf("step %d (at +%s): got %d, want %d", i, s.at, got, s.want)
		}
	}
}

func TestNodeStatus(t *testing.T) {
	listed := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		expires time.Time
		want    NodeState
	}{
		{time.Time{}, NodeUnknown},
		{listed.Add(5 * time.Minute), NodeOnline},
		{listed.Add(-time.Second), NodeStale},
	}
	for _, tt := range tests {
		if got := NodeStatus(tsh.Server{Expires: tt.expires}, listed); got != tt.want {
			t.Errorf("NodeStatus(expires %v) = %d, want %d", tt.expires, got, tt.want)
		}
	}
}

func TestMonitorCheck(t *testing.T) {
	until := time.Now().Add(time.Hour)
	f := &tsh.Fake{
		Pings:    map[string]tsh.PingInfo{"a:443": {ServerVersion: "18.10.0", MinClientVersion: "17.0.0", Latency: 23 * time.Millisecond}},
		Statuses: map[string]tsh.Status{"a:443": {LoggedIn: true, ValidUntil: until}},
	}
	m := NewMonitor(f, 0)
	r := m.Check(context.Background(), "a:443")
	if !r.Reachable || r.Latency != 23*time.Millisecond || r.ServerVersion != "18.10.0" || !r.LoggedIn || !r.ValidUntil.Equal(until) || r.Checked.IsZero() {
		t.Fatalf("report = %+v", r)
	}

	f.Err = errors.New("dial tcp: no route to host")
	r = m.Check(context.Background(), "a:443")
	if r.Reachable || r.Err == nil || r.LoggedIn {
		t.Fatalf("unreachable report = %+v", r)
	}
}

func TestMonitorRun(t *testing.T) {
	f := &tsh.Fake{Pings: map[string]tsh.PingInfo{"a": {ServerVersion: "18.0.0"}, "b": {ServerVersion: "17.0.0"}}}
	m := NewMonitor(f, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var got []string
	done := make(chan struct{})
	reported := make(chan struct{}, 10)
	go func() {
		defer close(done)
		m.Run(ctx, func() []string { return []string{"a", "b"} }, func(r Report) {
			mu.Lock()
			got = append(got, r.Proxy+"="+r.ServerVersion)
			mu.Unlock()
			reported <- struct{}{}
		})
	}()
	wait := func(n int) {
		for range n {
			select {
			case <-reported:
			case <-time.After(5 * time.Second):
				t.Fatal("no report")
			}
		}
	}
	wait(2)
	m.Refresh() // a second round without waiting an hour
	wait(2)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(got)
	if strings.Join(got, " ") != "a=18.0.0 a=18.0.0 b=17.0.0 b=17.0.0" {
		t.Fatalf("reports = %v", got)
	}
}

func TestCheckClient(t *testing.T) {
	tests := []struct {
		version string
		want    CompatLevel
		short   string
	}{
		{"18.11.3", CompatOK, ""},
		{"v16.4.2", CompatOK, ""},
		{"17.0.0-aa", CompatOK, ""},
		{"15.9.9", CompatWarn, "tsh v15 unsupported"},
		{"19.0.0", CompatWarn, "tsh v19 untested"},
		{"", CompatUnknown, "version unknown"},
		{"garbage", CompatUnknown, "version unknown"},
	}
	for _, tt := range tests {
		got := CheckClient(tt.version)
		if got.Level != tt.want || got.Short != tt.short {
			t.Errorf("CheckClient(%q) = %+v, want level %d short %q", tt.version, got, tt.want, tt.short)
		}
		if tt.want == CompatWarn && got.Message == "" {
			t.Errorf("CheckClient(%q): no message", tt.version)
		}
	}
}
