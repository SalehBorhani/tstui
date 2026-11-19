package ui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

const otherProxy = "b-teleport.example:443"

// A listing that started before a switch away and back must not replace
// the newer listing: proxy A -> B -> A, with A's first `tsh ls` slow.
func TestAppStaleListingAfterProxySwitch(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: map[string][]tsh.Server{
		profileProxy: {{Hostname: "old-a"}},
		otherProxy:   {{Hostname: "on-b"}},
	}}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	fake.SetStatus(otherProxy, validStatus(profileUser))

	release := make(chan struct{})
	var once sync.Once
	held, returned := make(chan struct{}), make(chan struct{})
	fake.Gate = func(call string) {
		if strings.HasPrefix(call, "ListServers "+profileProxy) {
			first := false
			once.Do(func() { first = true })
			if first {
				close(held)
				<-release // the first listing of A waits here
				defer close(returned)
			}
		}
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h := newAppHarness(t, fake, Options{})
	<-held

	h.ui(func() { h.app.switchProxyTo(otherProxy) })
	h.eventually("B's servers", func() bool { return h.app.serversLoaded && h.app.info.Proxy == otherProxy })
	fake.SetServers(profileProxy, []tsh.Server{{Hostname: "new-a"}})
	h.ui(func() { h.app.switchProxyTo(profileProxy) })
	h.eventually("A's new servers", func() bool {
		return h.app.serversLoaded && len(h.app.list.all) == 1 && h.app.list.all[0].Hostname == "new-a"
	})

	close(release) // the stale listing of A arrives last
	<-returned
	// Its result is posted right after ListAll returns; a marker posted
	// after a short wait runs after it.
	time.Sleep(50 * time.Millisecond)
	marker := make(chan struct{})
	h.app.post(func() { close(marker) })
	<-marker
	var got string
	h.ui(func() { got = h.app.list.all[0].Hostname })
	if got != "new-a" {
		t.Fatalf("list shows %q after the stale listing arrived, want new-a", got)
	}
}
