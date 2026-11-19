package ui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/tsh"
)

// --- clusters: the root and leaf clusters of the current proxy ---

// clusterEntries is the clusters panel: "all", then each cluster with
// its digit key (1-9) and server count.
func (a *App) clusterEntries() []clusterEntry {
	if a.clusters == nil && !a.serversLoaded && !a.serversLoading {
		return nil
	}
	view := a.list.view
	out := []clusterEntry{{Key: 0, Short: "all", Current: view == "", Count: a.list.countIn(""), Loading: a.serversLoading}}
	for i, c := range a.clusters {
		key := i + 1
		if key > 9 {
			key = -1
		}
		out = append(out, clusterEntry{
			Key: key, Name: c.Name, Short: a.list.shortName(c.Name), Root: c.Root(),
			Current: view == c.Name, Count: a.list.countIn(c.Name),
			Loading: a.serversLoading, Err: a.clusterErrs[c.Name],
		})
	}
	return out
}

// selectClusterKey shows the cluster with digit key n: 0 is every
// cluster, 1-9 the clusters in panel order.
func (a *App) selectClusterKey(n int) {
	if n == 0 {
		a.setClusterView("")
		return
	}
	if n > len(a.clusters) {
		if len(a.clusters) == 0 {
			a.flash("No clusters listed for this proxy yet.", a.theme.Info)
		} else {
			a.flash(fmt.Sprintf("No cluster %d: the proxy has %d (0 shows all).", n, len(a.clusters)), a.theme.Info)
		}
		return
	}
	a.setClusterView(a.clusters[n-1].Name)
}

// setClusterView shows one cluster's servers ("" for all) and says why a
// cluster shows none.
func (a *App) setClusterView(name string) {
	a.list.setView(name)
	a.renderHeader()
	if err := a.clusterErrs[name]; name != "" && err != nil {
		a.flash(clusterErrText(a.list.shortName(name), err), a.theme.Warn)
	}
}

// clusterByName finds a cluster by full name, short name or "all" (which
// returns "").
func (a *App) clusterByName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "all") || name == "0" {
		return "", true
	}
	for _, c := range a.clusters {
		if strings.EqualFold(c.Name, name) || strings.EqualFold(a.list.shortName(c.Name), name) {
			return c.Name, true
		}
	}
	return "", false
}

// clusterCommand runs :cluster [name]: without a name, a picker.
func (a *App) clusterCommand(arg string) {
	if arg == "" {
		a.openClusterPicker()
		return
	}
	name, ok := a.clusterByName(arg)
	if !ok {
		a.flash(fmt.Sprintf("Unknown cluster %q.", arg), a.theme.Err)
		return
	}
	a.setClusterView(name)
}

// openClusterPicker lists "all" and the clusters; Enter shows one.
func (a *App) openClusterPicker() {
	entries := a.clusterEntries()
	if len(entries) == 0 {
		a.flash("No clusters listed for this proxy yet.", a.theme.Info)
		return
	}
	items := make([]string, len(entries))
	cur := 0
	for i, e := range entries {
		mark := "[ ]"
		if e.Current {
			mark, cur = "[x]", i
		}
		name := e.Short
		if e.Name != "" && e.Name != e.Short {
			name += " (" + e.Name + ")"
		}
		state := fmt.Sprintf("%d servers", e.Count)
		if e.Err != nil {
			state = clusterErrText("", e.Err)
		}
		items[i] = tview.Escape(mark+" "+name) + "  " + tview.Escape(state)
	}
	a.pickFrom("clusters", "<Clusters>", items, cur, func(i int) { a.setClusterView(entries[i].Name) })
}

// clusterNames are the completions of :cluster.
func (a *App) clusterNames() []string {
	out := []string{"all"}
	for _, c := range a.clusters {
		out = append(out, a.list.shortName(c.Name))
	}
	return out
}

// reportClusterErrors flashes the clusters whose servers could not be
// listed (offline ones are only marked in the panel).
func (a *App) reportClusterErrors(l tsh.Listing) {
	var names []string
	for name, err := range l.Errs {
		if !errors.Is(err, tsh.ErrClusterOffline) {
			names = append(names, a.list.shortName(name))
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	a.flash("Cannot list servers of cluster "+strings.Join(names, ", ")+" (:cluster shows why).", a.theme.Warn)
}

func clusterErrText(name string, err error) string {
	prefix := ""
	if name != "" {
		prefix = "Cluster " + name + ": "
	}
	if errors.Is(err, tsh.ErrClusterOffline) {
		return prefix + "offline"
	}
	return prefix + err.Error()
}
