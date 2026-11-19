package tsh

import (
	"context"
	"strings"
	"sync"
)

// Fake is an in-memory Client for tests of packages that depend on tsh.
// Set the fields to the data each method should return; per-proxy maps
// are keyed by the proxy string the caller passes. Fake is safe for
// concurrent use and records every call.
type Fake struct {
	mu sync.Mutex

	VersionInfo VersionInfo
	Pings       map[string]PingInfo
	Statuses    map[string]Status
	// Servers is keyed by proxy. ListServers with a cluster returns the
	// servers whose Cluster is that cluster or empty.
	Servers map[string][]Server
	// Clusters is keyed by proxy; a proxy without clusters makes
	// ListClusters return none, as if tsh could not tell.
	Clusters    map[string][]Cluster
	ProfileList []Profile
	// PTY, if set, serves StartPTY; otherwise StartPTY returns ErrPTYUnsupported.
	PTY func(ctx context.Context, args []string, o PTYOptions) (Process, error)
	// ExecFunc, if set, serves Exec; otherwise Exec succeeds with no output.
	ExecFunc func(ctx context.Context, args []string, o ExecOptions) (Result, error)
	// Err, if set, is returned by every method.
	Err error
	// ListErr, if set, is returned by ListClusters and ListServers only:
	// the proxy cannot be reached, but the local profile can be read.
	ListErr error
	// Gate, if set, is called by ListServers after it has read its answer
	// and before it returns it, so a test can hold a listing back and let
	// newer ones overtake it.
	Gate func(call string)

	calls []string
}

var _ Client = (*Fake)(nil)

func (f *Fake) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.Err
}

// Calls returns the method calls made so far, as "Method proxy".
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Version implements Client.
func (f *Fake) Version(ctx context.Context) (VersionInfo, error) {
	if err := f.record("Version"); err != nil {
		return VersionInfo{}, err
	}
	return f.VersionInfo, ctx.Err()
}

// ServerPing implements Client.
func (f *Fake) ServerPing(ctx context.Context, proxy string) (PingInfo, error) {
	if err := f.record("ServerPing " + proxy); err != nil {
		return PingInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Pings[proxy], ctx.Err()
}

// Status implements Client.
func (f *Fake) Status(ctx context.Context, proxy string) (Status, error) {
	if err := f.record("Status " + proxy); err != nil {
		return Status{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Statuses[proxy], ctx.Err()
}

// ListClusters implements Client.
func (f *Fake) ListClusters(ctx context.Context, proxy string) ([]Cluster, error) {
	if err := f.record("ListClusters " + proxy); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return append([]Cluster(nil), f.Clusters[proxy]...), ctx.Err()
}

// ListServers implements Client.
func (f *Fake) ListServers(ctx context.Context, proxy, cluster string) ([]Server, error) {
	call := "ListServers " + proxy
	if cluster != "" {
		call += " " + cluster
	}
	if err := f.record(call); err != nil {
		return nil, err
	}
	f.mu.Lock()
	if err := f.ListErr; err != nil {
		f.mu.Unlock()
		return nil, err
	}
	var out []Server
	for _, srv := range f.Servers[proxy] {
		if cluster != "" {
			if srv.Cluster != "" && srv.Cluster != cluster {
				continue
			}
			srv.Cluster = cluster
		}
		out = append(out, srv)
	}
	gate := f.Gate
	f.mu.Unlock()
	if gate != nil {
		gate(call)
	}
	return out, ctx.Err()
}

// SetListErr changes ListErr; safe while other goroutines list.
func (f *Fake) SetListErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ListErr = err
}

// SetServers changes the servers listed for proxy; safe while other
// goroutines list them.
func (f *Fake) SetServers(proxy string, servers []Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Servers == nil {
		f.Servers = map[string][]Server{}
	}
	f.Servers[proxy] = servers
}

// Profiles implements Client.
func (f *Fake) Profiles() ([]Profile, error) {
	if err := f.record("Profiles"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Profile(nil), f.ProfileList...), nil
}

// SetStatus changes the Status returned for proxy; safe while other
// goroutines call Status.
func (f *Fake) SetStatus(proxy string, st Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Statuses == nil {
		f.Statuses = map[string]Status{}
	}
	f.Statuses[proxy] = st
}

// StartPTY implements Client.
func (f *Fake) StartPTY(ctx context.Context, args []string, o PTYOptions) (Process, error) {
	if err := f.record("StartPTY " + strings.Join(args, " ")); err != nil {
		return nil, err
	}
	if f.PTY == nil {
		return nil, ErrPTYUnsupported
	}
	return f.PTY(ctx, args, o)
}

// Exec implements Client.
func (f *Fake) Exec(ctx context.Context, args []string, o ExecOptions) (Result, error) {
	if err := f.record("Exec " + strings.Join(args, " ")); err != nil {
		return Result{ExitCode: -1}, err
	}
	if f.ExecFunc == nil {
		return Result{}, ctx.Err()
	}
	return f.ExecFunc(ctx, args, o)
}

// SetPing changes the PingInfo returned for proxy; safe while other
// goroutines call ServerPing.
func (f *Fake) SetPing(proxy string, p PingInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Pings == nil {
		f.Pings = map[string]PingInfo{}
	}
	f.Pings[proxy] = p
}

// Logout implements Client: it forgets the Status for proxy.
func (f *Fake) Logout(_ context.Context, proxy, _ string) error {
	if err := f.record("Logout " + proxy); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Statuses, proxy)
	return nil
}
