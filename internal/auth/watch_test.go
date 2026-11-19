package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

const proxy = "tp.afr.example:443"

func TestWatcherCheck(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	valid := func(left time.Duration) tsh.Status {
		return tsh.Status{LoggedIn: true, User: "ali", ValidUntil: now.Add(left)}
	}
	fake := &tsh.Fake{}
	w := NewWatcher(fake, proxy, WatchOptions{Now: func() time.Time { return now }})

	steps := []struct {
		name        string
		status      tsh.Status
		err         error
		wantState   CertState
		wantChanged bool
	}{
		{"first poll always reports", valid(8 * time.Hour), nil, CertValid, true},
		{"no change, no event", valid(7 * time.Hour), nil, CertValid, false},
		{"under 30 minutes", valid(29 * time.Minute), nil, CertExpiring, true},
		{"still expiring", valid(5 * time.Minute), nil, CertExpiring, false},
		{"expired", valid(-time.Second), nil, CertExpired, true},
		{"logged out", tsh.Status{}, nil, CertLoggedOut, true},
		{"status fails", tsh.Status{}, errors.New("boom"), CertUnknown, true},
		{"logged in again", valid(12 * time.Hour), nil, CertValid, true},
	}
	for _, s := range steps {
		fake.SetStatus(proxy, s.status)
		fake.Err = s.err
		ev, changed := w.Check(context.Background())
		if ev.State != s.wantState || changed != s.wantChanged {
			t.Errorf("%s: state %v changed %v, want %v %v", s.name, ev.State, changed, s.wantState, s.wantChanged)
		}
		if (ev.Err != nil) != (s.err != nil) {
			t.Errorf("%s: err = %v", s.name, ev.Err)
		}
	}
}

func TestWatcherNextPoll(t *testing.T) {
	w := NewWatcher(&tsh.Fake{}, proxy, WatchOptions{Interval: time.Minute, Warning: 30 * time.Minute})
	tests := []struct {
		ev   WatchEvent
		want time.Duration
	}{
		{WatchEvent{State: CertValid, Remaining: 8 * time.Hour}, time.Minute},
		{WatchEvent{State: CertValid, Remaining: 30*time.Minute + 10*time.Second}, 10*time.Second + 100*time.Millisecond},
		{WatchEvent{State: CertExpiring, Remaining: 5 * time.Second}, 5*time.Second + 100*time.Millisecond},
		{WatchEvent{State: CertExpiring, Remaining: 0}, 100 * time.Millisecond},
		{WatchEvent{State: CertLoggedOut}, time.Minute},
	}
	for _, tt := range tests {
		if got := w.nextPoll(tt.ev); got != tt.want {
			t.Errorf("%v %s: next poll %s, want %s", tt.ev.State, tt.ev.Remaining, got, tt.want)
		}
	}
}

// TestWatcherRun checks the goroutine end to end: first event, an event
// on change, Refresh, and closing on cancel.
func TestWatcherRun(t *testing.T) {
	fake := &tsh.Fake{}
	fake.SetStatus(proxy, tsh.Status{LoggedIn: true, ValidUntil: time.Now().Add(time.Hour)})
	w := NewWatcher(fake, proxy, WatchOptions{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	events := w.Run(ctx)

	next := func() WatchEvent {
		t.Helper()
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("channel closed early")
			}
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return WatchEvent{}
	}
	if ev := next(); ev.State != CertValid || ev.Proxy != proxy {
		t.Fatalf("first event %+v", ev)
	}
	// The interval is an hour: only Refresh can make the next poll happen.
	_ = fake.Logout(ctx, proxy, "")
	w.Refresh()
	if ev := next(); ev.State != CertLoggedOut {
		t.Fatalf("after logout: %v", ev.State)
	}

	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("unexpected event after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}
