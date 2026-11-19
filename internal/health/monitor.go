package health

import (
	"context"
	"sync"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// Source is what Monitor needs from tsh.Client.
type Source interface {
	ServerPing(ctx context.Context, proxy string) (tsh.PingInfo, error)
	Status(ctx context.Context, proxy string) (tsh.Status, error)
}

// Report is the health of one proxy at one moment.
type Report struct {
	Proxy string
	// Reachable is true when /webapi/ping answered; Err says why not.
	Reachable bool
	Err       error
	Latency   time.Duration
	// ServerVersion and MinClientVersion come from the ping.
	ServerVersion    string
	MinClientVersion string
	// LoggedIn and ValidUntil come from `tsh status --client` (local, no
	// network).
	LoggedIn   bool
	ValidUntil time.Time
	// Checked is when the report was made.
	Checked time.Time
}

// DefaultInterval is how often Monitor checks every proxy.
const DefaultInterval = time.Minute

// maxParallel bounds concurrent checks.
const maxParallel = 4

// Monitor checks a set of proxies periodically.
type Monitor struct {
	src      Source
	interval time.Duration
	now      func() time.Time
	refresh  chan struct{}
}

// NewMonitor returns a monitor; interval <= 0 means DefaultInterval.
func NewMonitor(src Source, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Monitor{src: src, interval: interval, now: time.Now, refresh: make(chan struct{}, 1)}
}

// Check makes one report for proxy.
func (m *Monitor) Check(ctx context.Context, proxy string) Report {
	r := Report{Proxy: proxy}
	p, err := m.src.ServerPing(ctx, proxy)
	if err != nil {
		r.Err = err
	} else {
		r.Reachable = true
		r.Latency = p.Latency
		r.ServerVersion, r.MinClientVersion = p.ServerVersion, p.MinClientVersion
	}
	if st, err := m.src.Status(ctx, proxy); err == nil && st.LoggedIn {
		r.LoggedIn, r.ValidUntil = true, st.ValidUntil
	}
	r.Checked = m.now()
	return r
}

// Refresh asks Run for a round of checks now.
func (m *Monitor) Refresh() {
	select {
	case m.refresh <- struct{}{}:
	default:
	}
}

// Run checks proxies() every interval until ctx is done, calling report
// for each result from its own goroutines; report must not block for
// long. Run returns when ctx is done and every check has finished.
func (m *Monitor) Run(ctx context.Context, proxies func() []string, report func(Report)) {
	for {
		m.round(ctx, proxies(), report)
		t := time.NewTimer(m.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-m.refresh:
			t.Stop()
		case <-t.C:
		}
	}
}

func (m *Monitor) round(ctx context.Context, proxies []string, report func(Report)) {
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for _, p := range proxies {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p string) {
			defer func() { <-sem; wg.Done() }()
			r := m.Check(ctx, p)
			if ctx.Err() == nil {
				report(r)
			}
		}(p)
	}
	wg.Wait()
}
