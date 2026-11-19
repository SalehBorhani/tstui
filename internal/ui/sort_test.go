package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestNaturalCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"web-2", "web-10", -1},
		{"web-10", "web-2", 1},
		{"Web-01", "web-1", 0}, // case and leading zeros ignored
		{"db", "web", -1},
		{"web", "web-1", -1},
		{"node9x", "node10", -1},
		{"", "", 0},
		{"", "a", -1},
	}
	for _, tt := range tests {
		if got := naturalCompare(tt.a, tt.b); got != tt.want {
			t.Errorf("naturalCompare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCompareAddr(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"10.0.0.2:3022", "10.0.0.10:3022", -1}, // numeric, not text
		{"10.0.0.1:3022", "10.0.0.1:22", 1},     // then the port
		{"192.168.1.1:22", "[::1]:22", -1},      // IPv4 before IPv6
		{"10.0.0.1:22", "alpha.example:22", -1}, // IPs before names
		{"node-2.example:22", "node-10.example:22", -1},
		{"10.0.0.1", "10.0.0.1:22", -1}, // no port is port 0
		{"10.0.0.1:22", "10.0.0.1:22", 0},
	}
	for _, tt := range tests {
		if got := compareAddr(tt.a, tt.b); got != tt.want {
			t.Errorf("compareAddr(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseSortKey(t *testing.T) {
	for in, want := range map[string]sortKey{"name": sortName, "ADDRESS": sortAddr, "cluster": sortCluster, "off": sortNone} {
		if got, ok := parseSortKey(in); !ok || got != want {
			t.Errorf("parseSortKey(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := parseSortKey("age"); ok {
		t.Error("parseSortKey(age) accepted")
	}
}

func TestServerListSort(t *testing.T) {
	servers := []tsh.Server{
		{Hostname: "web-10", Cluster: "root", Addr: "10.0.0.3:3022"},
		{Hostname: "web-2", Cluster: "afr", Addr: "10.0.0.20:3022"},
		{Hostname: "tunnel-1", Cluster: "hwb"},
		{Hostname: "db-1", Cluster: "hwb", Addr: "10.0.0.1:3022"},
		{Hostname: "web-2", Cluster: "root", Addr: "10.0.0.4:3022"},
	}
	row := func(s tsh.Server) string { return s.Cluster + "/" + s.Hostname }
	tests := []struct {
		name string
		keys []sortKey // pressed in order
		favs []string
		want string
	}{
		{"as listed", nil, nil, "root/web-10 afr/web-2 hwb/tunnel-1 hwb/db-1 root/web-2"},
		{"name", []sortKey{sortName}, nil, "hwb/db-1 hwb/tunnel-1 afr/web-2 root/web-2 root/web-10"},
		{"name reversed", []sortKey{sortName, sortName}, nil, "root/web-10 root/web-2 afr/web-2 hwb/tunnel-1 hwb/db-1"},
		{"name again ascending", []sortKey{sortName, sortName, sortName}, nil, "hwb/db-1 hwb/tunnel-1 afr/web-2 root/web-2 root/web-10"},
		{"address, tunnel last", []sortKey{sortAddr}, nil, "hwb/db-1 root/web-10 root/web-2 afr/web-2 hwb/tunnel-1"},
		{"address reversed, tunnel still last", []sortKey{sortAddr, sortAddr}, nil, "afr/web-2 root/web-2 root/web-10 hwb/db-1 hwb/tunnel-1"},
		{"cluster then name", []sortKey{sortCluster}, nil, "afr/web-2 hwb/db-1 hwb/tunnel-1 root/web-2 root/web-10"},
		{"other key starts ascending", []sortKey{sortName, sortName, sortCluster}, nil, "afr/web-2 hwb/db-1 hwb/tunnel-1 root/web-2 root/web-10"},
		{"off restores tsh order", []sortKey{sortName, sortNone}, nil, "root/web-10 afr/web-2 hwb/tunnel-1 hwb/db-1 root/web-2"},
		{"favorites first, sorted among themselves", []sortKey{sortName, sortName}, []string{"web-2", "db-1"}, "root/web-2 afr/web-2 hwb/db-1 root/web-10 hwb/tunnel-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServerList(DefaultTheme())
			favs := map[string]bool{}
			for _, f := range tt.favs {
				favs[f] = true
			}
			s.setFavorites(favs)
			s.setServers(servers, time.Now())
			for _, k := range tt.keys {
				s.setSort(k)
			}
			var got []string
			for _, srv := range s.servers {
				got = append(got, row(srv))
			}
			if g := strings.Join(got, " "); g != tt.want {
				t.Errorf("order\n got %s\nwant %s", g, tt.want)
			}
		})
	}
}

func TestServerListSortIndicator(t *testing.T) {
	s := newServerList(DefaultTheme())
	s.setServers([]tsh.Server{{Hostname: "b", Addr: "10.0.0.2:22"}, {Hostname: "a", Addr: "10.0.0.1:22"}}, time.Now())
	head := func(col int) string { return s.table.GetCell(0, col).Text }

	s.setSort(sortName)
	if head(1) != "NAME↑" {
		t.Fatalf("name header %q", head(1))
	}
	s.setSort(sortName)
	if head(1) != "NAME↓" {
		t.Fatalf("name header after reverse %q", head(1))
	}
	// ADDRESS is hidden in the compact view: the title names the sort.
	s.setCompact(true)
	s.setSort(sortAddr)
	if !strings.Contains(s.table.GetTitle(), "address↑") || head(1) != "NAME" {
		t.Fatalf("compact: title %q, header %q", s.table.GetTitle(), head(1))
	}
	s.setCompact(false)
	s.rerender()
	if strings.Contains(s.table.GetTitle(), "address") {
		t.Fatalf("title names a visible column's sort: %q", s.table.GetTitle())
	}
}
