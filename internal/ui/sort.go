package ui

import (
	"cmp"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/root-ali/tstui/internal/tsh"
)

// sortKey is the column the server list is sorted by. Favorites stay
// pinned at the top whatever the key; the key orders the rows within the
// favorites and within the rest.
type sortKey int

// Sort keys.
const (
	// sortNone keeps the order tsh listed the servers in.
	sortNone sortKey = iota
	sortName
	sortAddr
	sortCluster
)

// parseSortKey reads a :sort argument, one of the palette's choices for
// it.
func parseSortKey(s string) (sortKey, bool) {
	switch strings.ToLower(s) {
	case "name":
		return sortName, true
	case "address":
		return sortAddr, true
	case "cluster":
		return sortCluster, true
	case "off":
		return sortNone, true
	}
	return sortNone, false
}

// column is the header of the column the key sorts.
func (k sortKey) column() string {
	switch k {
	case sortName:
		return "NAME"
	case sortAddr:
		return "ADDRESS"
	case sortCluster:
		return "CLUSTER"
	}
	return ""
}

// arrow shows the direction: ↑ ascending, ↓ descending.
func arrow(desc bool) string {
	if desc {
		return "↓"
	}
	return "↑"
}

// compareServers orders a and b by key, ascending. Ties fall back to the
// name, then the cluster, so the order does not depend on tsh's. short
// maps a cluster to the name the list shows.
func compareServers(a, b tsh.Server, key sortKey, short func(string) string) int {
	byName := func() int {
		return cmp.Or(naturalCompare(a.Hostname, b.Hostname), naturalCompare(short(a.Cluster), short(b.Cluster)))
	}
	switch key {
	case sortName:
		return byName()
	case sortAddr:
		return cmp.Or(compareAddr(a.Addr, b.Addr), byName())
	case sortCluster:
		return cmp.Or(naturalCompare(short(a.Cluster), short(b.Cluster)), naturalCompare(a.Hostname, b.Hostname))
	}
	return 0
}

// compareAddr orders node addresses: IP addresses numerically (IPv4
// before IPv6), then hostnames, then by port. Tunnel nodes (no address)
// are kept last by the caller.
func compareAddr(a, b string) int {
	ha, pa := splitAddr(a)
	hb, pb := splitAddr(b)
	ia, errA := netip.ParseAddr(ha)
	ib, errB := netip.ParseAddr(hb)
	var c int
	switch {
	case errA == nil && errB == nil:
		c = ia.Compare(ib)
	case errA == nil:
		c = -1
	case errB == nil:
		c = 1
	default:
		c = naturalCompare(ha, hb)
	}
	return cmp.Or(c, cmp.Compare(pa, pb))
}

// splitAddr splits host:port; an address without a port has port 0.
func splitAddr(s string) (string, int) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, 0
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// naturalCompare compares strings case-insensitively, with runs of
// digits compared as numbers: web-2 sorts before web-10.
func naturalCompare(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			i, j := digitRun(a), digitRun(b)
			na, nb := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if c := cmp.Or(cmp.Compare(len(na), len(nb)), strings.Compare(na, nb)); c != 0 {
				return c
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return cmp.Compare(a[0], b[0])
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string) int {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

// less is the visible-row order: favorites first, then the sort key in
// its direction. Tunnel nodes stay last when sorting by address, in both
// directions.
func (s *serverList) less(a, b tsh.Server) bool {
	if fa, fb := s.favs[a.Hostname], s.favs[b.Hostname]; fa != fb {
		return fa
	}
	if s.sortBy == sortAddr && (a.Addr == "") != (b.Addr == "") {
		return b.Addr == ""
	}
	c := compareServers(a, b, s.sortBy, s.shortName)
	if s.sortDesc {
		c = -c
	}
	return c < 0
}

// setSort sorts by key; the key already in use flips the direction. It
// returns a description of the new order, for the footer.
func (s *serverList) setSort(key sortKey) string {
	if key != sortNone && key == s.sortBy {
		s.sortDesc = !s.sortDesc
	} else {
		s.sortBy, s.sortDesc = key, false
	}
	s.rebuild(s.selectedID())
	if key == sortNone {
		return "Sort off: servers in the order tsh lists them."
	}
	dir := "ascending"
	if s.sortDesc {
		dir = "descending"
	}
	return "Sorted by " + strings.ToLower(key.column()) + ", " + dir + "."
}
