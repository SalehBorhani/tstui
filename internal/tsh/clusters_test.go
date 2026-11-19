package tsh

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// realClusters is `tsh clusters --format=json` as tsh v18.11.3 printed it
// against a v18 cluster with two leaf clusters (names changed).
const realClusters = `[
  {"cluster_name": "teleport-ast.example.com", "status": "online", "cluster_type": "root", "labels": null, "selected": true},
  {"cluster_name": "hwb.teleport.example.com", "status": "online", "cluster_type": "leaf", "labels": null, "selected": false},
  {"cluster_name": "afr.teleport.example.com", "status": "offline", "cluster_type": "leaf", "labels": {"env": "x"}, "selected": false}
]`

func TestParseClusters(t *testing.T) {
	got, err := parseClusters([]byte(realClusters))
	if err != nil {
		t.Fatal(err)
	}
	want := []Cluster{
		{Name: "teleport-ast.example.com", Status: "online", Type: "root", Selected: true},
		{Name: "afr.teleport.example.com", Status: "offline", Type: "leaf"},
		{Name: "hwb.teleport.example.com", Status: "online", Type: "leaf"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if !got[0].Root() || got[1].Online() || !got[2].Online() {
		t.Fatalf("Root/Online wrong: %+v", got)
	}
	if got, err := parseClusters([]byte(" \n")); err != nil || got != nil {
		t.Fatalf("empty: %v, %v", got, err)
	}
	if _, err := parseClusters([]byte("{")); err == nil {
		t.Fatal("bad JSON parsed")
	}
}

// listFake is a Client whose ListServers fails for some clusters.
type listFake struct {
	*Fake
	failFor map[string]error
	mu      sync.Mutex
	listed  []string
}

func (l *listFake) ListServers(ctx context.Context, proxy, cluster string) ([]Server, error) {
	l.mu.Lock()
	l.listed = append(l.listed, cluster)
	l.mu.Unlock()
	if err := l.failFor[cluster]; err != nil {
		return nil, err
	}
	return l.Fake.ListServers(ctx, proxy, cluster)
}

func TestListAll(t *testing.T) {
	clusters := []Cluster{
		{Name: "root", Type: "root", Status: "online"},
		{Name: "leaf-a", Type: "leaf", Status: "online"},
		{Name: "leaf-b", Type: "leaf", Status: "online"},
		{Name: "gone", Type: "leaf", Status: "offline"},
	}
	servers := []Server{
		{Hostname: "web-01", Cluster: "root"},
		{Hostname: "web-01", Cluster: "leaf-a"},
		{Hostname: "db-01", Cluster: "leaf-b"},
	}
	errBoom := errors.New("boom")
	tests := []struct {
		name     string
		clusters []Cluster
		servers  []Server
		fail     map[string]error
		want     []string // cluster/host
		wantErrs []string // clusters with an error
		wantErr  error
	}{
		{"every cluster", clusters, servers, nil,
			[]string{"root/web-01", "leaf-a/web-01", "leaf-b/db-01"}, []string{"gone"}, nil},
		{"one leaf fails", clusters, servers, map[string]error{"leaf-a": errBoom},
			[]string{"root/web-01", "leaf-b/db-01"}, []string{"gone", "leaf-a"}, nil},
		{"all fail", clusters[:2], servers, map[string]error{"root": ErrNotLoggedIn, "leaf-a": errBoom},
			nil, []string{"root", "leaf-a"}, ErrNotLoggedIn},
		// tsh could not list the clusters: the selected cluster alone.
		{"no clusters", nil, []Server{{Hostname: "web-01"}, {Hostname: "db-01"}}, nil,
			[]string{"/web-01", "/db-01"}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &listFake{
				Fake:    &Fake{Clusters: map[string][]Cluster{"p": tt.clusters}, Servers: map[string][]Server{"p": tt.servers}},
				failFor: tt.fail,
			}
			got, err := ListAll(context.Background(), c, "p")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			var names []string
			for _, s := range got.Servers {
				names = append(names, s.Cluster+"/"+s.Hostname)
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Fatalf("servers = %q, want %q", names, tt.want)
			}
			if len(got.Errs) != len(tt.wantErrs) {
				t.Fatalf("errs = %v, want %v", got.Errs, tt.wantErrs)
			}
			for _, name := range tt.wantErrs {
				if got.Errs[name] == nil {
					t.Fatalf("no error for %s: %v", name, got.Errs)
				}
			}
			// The offline cluster is reported, never listed.
			if err, ok := got.Errs["gone"]; ok && !errors.Is(err, ErrClusterOffline) {
				t.Fatalf("offline cluster error = %v", err)
			}
			for _, l := range c.listed {
				if l == "gone" {
					t.Fatal("offline cluster was listed")
				}
			}
		})
	}
}

func TestListAllNotLoggedIn(t *testing.T) {
	f := &Fake{Err: ErrNotLoggedIn}
	if _, err := ListAll(context.Background(), f, "p"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v", err)
	}
	if calls := strings.Join(f.Calls(), ","); calls != "ListClusters p" {
		t.Fatalf("calls = %s: a logged-out proxy must not be listed further", calls)
	}
}

// TestCLIClusters runs the fake tsh: clusters, then each online cluster's
// servers with --cluster.
func TestCLIClusters(t *testing.T) {
	c, argvLog := fakeCLI(t)
	got, err := ListAll(context.Background(), c, "tp.afr.example:443")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clusters) != 3 || got.Clusters[0].Name != "afr-prod" || !got.Clusters[0].Root() {
		t.Fatalf("clusters = %+v", got.Clusters)
	}
	per := map[string]int{}
	for _, s := range got.Servers {
		per[s.Cluster]++
	}
	if per["afr-prod"] != 3 || per["hwb-prod"] != 2 || len(per) != 2 {
		t.Fatalf("servers per cluster = %v", per)
	}
	if !errors.Is(got.Errs["old-dc"], ErrClusterOffline) {
		t.Fatalf("errs = %v", got.Errs)
	}
	log := readFile(t, argvLog)
	for _, want := range []string{"clusters --format=json --proxy=tp.afr.example:443", "ls --format=json --proxy=tp.afr.example:443 --cluster=hwb-prod"} {
		if !strings.Contains(log, want) {
			t.Errorf("argv log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "--cluster=old-dc") {
		t.Errorf("offline cluster listed:\n%s", log)
	}

	// An old or restricted tsh that cannot list clusters: the selected
	// cluster alone.
	c, _ = fakeCLI(t, "FAKE_TSH_CLUSTERS=none")
	got, err = ListAll(context.Background(), c, "tp.afr.example:443")
	if err != nil || got.Clusters != nil || len(got.Servers) != 3 {
		t.Fatalf("fallback = %+v, %v", got, err)
	}
}
