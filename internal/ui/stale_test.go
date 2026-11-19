package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/root-ali/tstui/internal/health"
	"github.com/root-ali/tstui/internal/tsh"
)

// The proxy cannot be reached: the last good listing is shown, marked
// stale; when the proxy answers again the live list replaces it. A good
// listing is cached for next time.
func TestAppOfflineShowsStaleCache(t *testing.T) {
	stateDir := t.TempDir()
	cache := tsh.ServerCache{Dir: stateDir + "/servers"}
	if err := cache.Save(profileProxy, tsh.Listing{Servers: []tsh.Server{{Hostname: "cached-01"}}}, time.Now().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fake := &tsh.Fake{ProfileList: profiles(), Servers: map[string][]tsh.Server{profileProxy: {{Hostname: "live-01"}}},
		ListErr: errors.New("dial tcp 10.0.0.1:443: i/o timeout")}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	h := newAppHarness(t, fake, Options{StateDir: stateDir})

	h.eventually("stale list", func() bool {
		return strings.Contains(h.textUnlocked(), "cached-01") && strings.Contains(h.textUnlocked(), "STALE listed 3h ago")
	})

	fake.SetListErr(nil)
	h.ui(func() { h.app.onHealth(health.Report{Proxy: profileProxy, Reachable: true, Checked: time.Now()}) })
	h.eventually("live list", func() bool {
		txt := h.textUnlocked()
		return strings.Contains(txt, "live-01") && !strings.Contains(txt, "STALE")
	})
	got, _, err := cache.Load(profileProxy)
	if err != nil || len(got.Servers) != 1 || got.Servers[0].Hostname != "live-01" {
		t.Fatalf("cache after a good listing: %+v, %v", got, err)
	}
}

// Without a cache, a failed listing says so, as before.
func TestAppOfflineNoCache(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), ListErr: errors.New("dial tcp: i/o timeout")}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	h := newAppHarness(t, fake, Options{StateDir: t.TempDir()})
	h.eventually("error message", func() bool { return strings.Contains(h.textUnlocked(), "Cannot list servers") })
}

func TestStaleAge(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "listed just now"},
		{now.Add(-5 * time.Minute), "listed 5m ago"},
		{now.Add(-30 * time.Hour), "listed 30h ago"},
		{time.Time{}, "cached"},
	}
	for _, tt := range tests {
		if got := staleAge(tt.at, now); got != tt.want {
			t.Errorf("staleAge(%s) = %q, want %q", tt.at, got, tt.want)
		}
	}
}
