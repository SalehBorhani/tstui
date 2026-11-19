package auth

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// CertState is the login state of one proxy as the watcher sees it.
type CertState int

// Certificate states.
const (
	// CertUnknown: tsh status failed; see WatchEvent.Err.
	CertUnknown CertState = iota
	// CertValid: logged in, more than the warning time left.
	CertValid
	// CertExpiring: logged in, less than the warning time left.
	CertExpiring
	// CertExpired: the profile exists but its certificate has expired.
	CertExpired
	// CertLoggedOut: there is no profile for the proxy.
	CertLoggedOut
)

func (s CertState) String() string {
	switch s {
	case CertValid:
		return "valid"
	case CertExpiring:
		return "cert-expiring"
	case CertExpired:
		return "cert-expired"
	case CertLoggedOut:
		return "logged-out"
	}
	return "unknown"
}

// WatchEvent is sent when a proxy's login state changes.
type WatchEvent struct {
	Proxy  string
	State  CertState
	Status tsh.Status
	// Remaining is the certificate lifetime left when the event was made.
	Remaining time.Duration
	// Err is set for CertUnknown.
	Err error
}

// Watch defaults.
const (
	DefaultWatchInterval = 30 * time.Second
	// DefaultExpiryWarning is when CertExpiring starts.
	DefaultExpiryWarning = 30 * time.Minute
)

// WatchOptions configures a Watcher. Zero values give the defaults.
type WatchOptions struct {
	// Interval between `tsh status` polls.
	Interval time.Duration
	// Warning is how long before expiry CertExpiring is reported.
	Warning time.Duration
	// Now is the clock (tests).
	Now    func() time.Time
	Logger *slog.Logger
}

// Watcher polls `tsh status` for one proxy and reports state changes:
// valid, cert-expiring, cert-expired, logged-out. The header shows them
// and the UI offers a re-login.
type Watcher struct {
	client  tsh.Client
	proxy   string
	o       WatchOptions
	refresh chan struct{}

	last    CertState
	started bool
}

// NewWatcher returns a watcher for proxy. Start it with Run.
func NewWatcher(client tsh.Client, proxy string, o WatchOptions) *Watcher {
	if o.Interval <= 0 {
		o.Interval = DefaultWatchInterval
	}
	if o.Warning <= 0 {
		o.Warning = DefaultExpiryWarning
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Watcher{client: client, proxy: proxy, o: o, refresh: make(chan struct{}, 1)}
}

// Refresh asks for a poll now, e.g. right after a login or logout.
func (w *Watcher) Refresh() {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
}

// Run polls until ctx is done and sends an event for the first poll and
// for every change of state after it. The channel is closed when Run
// stops. Run itself does not block: it starts a goroutine.
func (w *Watcher) Run(ctx context.Context) <-chan WatchEvent {
	ch := make(chan WatchEvent, 1)
	go func() {
		defer close(ch)
		for {
			ev, changed := w.Check(ctx)
			if ctx.Err() != nil {
				return
			}
			if changed {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
			t := time.NewTimer(w.nextPoll(ev))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-w.refresh:
				t.Stop()
			case <-t.C:
			}
		}
	}()
	return ch
}

// Check polls once and reports whether the state differs from the last
// poll. The first poll always counts as a change.
func (w *Watcher) Check(ctx context.Context) (WatchEvent, bool) {
	st, err := w.client.Status(ctx, w.proxy)
	ev := w.classify(st, err)
	changed := !w.started || ev.State != w.last
	w.started, w.last = true, ev.State
	if changed {
		w.o.Logger.Debug("login state", "proxy", w.proxy, "state", ev.State.String(), "remaining", ev.Remaining.Round(time.Second))
	}
	return ev, changed
}

func (w *Watcher) classify(st tsh.Status, err error) WatchEvent {
	now := w.o.Now()
	ev := WatchEvent{Proxy: w.proxy, Status: st, Remaining: st.Remaining(now)}
	switch {
	case err != nil:
		ev.State, ev.Err = CertUnknown, err
	case !st.LoggedIn:
		ev.State = CertLoggedOut
	case st.Expired(now):
		ev.State = CertExpired
	case !st.ValidUntil.IsZero() && ev.Remaining <= w.o.Warning:
		ev.State = CertExpiring
	default:
		ev.State = CertValid
	}
	return ev
}

// nextPoll waits the interval, but wakes up in time for the next state
// boundary (warning start, expiry) so those are reported promptly.
func (w *Watcher) nextPoll(ev WatchEvent) time.Duration {
	d := w.o.Interval
	var boundary time.Duration
	switch ev.State {
	case CertValid:
		boundary = ev.Remaining - w.o.Warning
	case CertExpiring:
		boundary = ev.Remaining
	default:
		return d
	}
	boundary += 100 * time.Millisecond // land just past the boundary
	if boundary < d {
		d = boundary
	}
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	return d
}
