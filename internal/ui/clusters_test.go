package ui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestShortClusterNames(t *testing.T) {
	got := shortClusterNames([]string{"teleport-ast.example.com", "afr.teleport.example.com", "hwb.teleport.example.com",
		"dup.one.example", "dup.two.example", "plain"})
	want := map[string]string{
		"teleport-ast.example.com": "teleport-ast", "afr.teleport.example.com": "afr", "hwb.teleport.example.com": "hwb",
		"dup.one.example": "dup.one.example", "dup.two.example": "dup.two.example", "plain": "plain",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

// clusterSetup gives the fake proxy a root and two leaf clusters, one of
// them offline. Both online clusters have a web-01.
func clusterSetup(f *tsh.Fake, _ *Options) {
	f.Clusters = map[string][]tsh.Cluster{profileProxy: {
		{Name: "root.example.com", Type: "root", Status: "online"},
		{Name: "afr.example.com", Type: "leaf", Status: "online"},
		{Name: "old.example.com", Type: "leaf", Status: "offline"},
	}}
	soon := time.Now().Add(time.Hour)
	f.Servers = map[string][]tsh.Server{profileProxy: {
		{Hostname: "web-01", Cluster: "root.example.com", Labels: map[string]string{"env": "prod"}, Expires: soon},
		{Hostname: "db-03", Cluster: "root.example.com", Labels: map[string]string{"env": "prod"}, Expires: soon},
		{Hostname: "web-01", Cluster: "afr.example.com", Labels: map[string]string{"env": "afr"}, Expires: soon},
		{Hostname: "mq-01", Cluster: "afr.example.com", Labels: map[string]string{"env": "afr"}, Expires: soon},
	}}
}

func TestClusterViews(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, clusterSetup)

	// All clusters: every server, with a CLUSTER column, and the panel.
	h.eventually("all servers", func() bool { return len(h.visible()) == 4 })
	h.waitScreen("Servers(all)[4]")
	h.waitScreen("CLUSTER")
	h.waitScreen("<0> all 4")
	h.waitScreen("<1> root 2 root")
	h.waitScreen("<2> afr 2")
	h.waitScreen("<3> old ✘ offline")

	// 2 shows the afr leaf cluster alone, without the column.
	h.typeText("2")
	h.eventually("afr view", func() bool { return sorted(h.visible()) == "mq-01,web-01" })
	h.waitScreen("Servers(afr)[2]")
	h.ui(func() {
		if strings.Contains(h.textUnlocked(), "CLUSTER") {
			t.Error("CLUSTER column in a one-cluster view")
		}
	})

	// Enter connects to afr's web-01, not root's: tsh gets --cluster.
	h.ui(func() {
		for i, s := range h.app.list.servers {
			if s.Hostname == "web-01" {
				h.app.list.table.Select(i+1, 0)
			}
		}
	})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	if sp := h.sp.Spec(0); sp.Cluster != "afr.example.com" || sp.Host != "web-01" {
		t.Fatalf("spec = %+v", sp)
	}
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.eventually("list focused", func() bool { return h.app.focus == focusList })

	// The offline cluster says why it is empty; 9 does not exist.
	h.typeText("3")
	h.waitScreen("Cluster old: offline")
	h.typeText("9")
	h.waitScreen("No cluster 9")

	// :cluster by short name, and all.
	h.command("cluster root")
	h.eventually("root view", func() bool { return sorted(h.visible()) == "db-03,web-01" })
	h.command("cluster all")
	h.eventually("all again", func() bool { return len(h.visible()) == 4 })

	// Marks tell the two web-01 apart; :exec runs each in its cluster.
	h.ui(func() {
		for i, s := range h.app.list.servers {
			if s.Hostname == "web-01" {
				h.app.list.table.Select(i+1, 0)
				h.app.list.toggleMark()
			}
		}
	})
	h.eventually("two marked", func() bool { return len(h.app.list.markedHosts()) == 2 })
	h.command("exec uptime")
	h.waitScreen("Run as")
	h.key(tcell.KeyEnter)
	h.eventually("two runs", func() bool { return len(h.rec.all()) == 2 })
	var clusters []string
	for _, args := range h.rec.all() {
		for _, a := range args {
			if c, ok := strings.CutPrefix(a, "--cluster="); ok {
				clusters = append(clusters, c)
			}
		}
	}
	slices.Sort(clusters)
	if strings.Join(clusters, ",") != "afr.example.com,root.example.com" {
		t.Fatalf("exec clusters = %q (runs %q)", clusters, h.rec.all())
	}
}

// TestClustersUnavailable: a tsh that lists no clusters still lists the
// servers, under "all" alone.
func TestClustersUnavailable(t *testing.T) {
	h := newExtrasHarness(t, []string{"ali"}, nil)
	h.eventually("servers", func() bool { return len(h.visible()) == 3 })
	h.waitScreen("<0> all 3")
	h.typeText("1")
	h.waitScreen("No clusters listed")
}

func sorted(names []string) string {
	names = slices.Clone(names)
	slices.Sort(names)
	return strings.Join(names, ",")
}
