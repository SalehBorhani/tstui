package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/filter"
	"github.com/root-ali/tstui/internal/health"
	"github.com/root-ali/tstui/internal/tsh"
)

// serverList is the k9s-style table of the current proxy's SSH nodes,
// across its Teleport clusters. The view shows every cluster (with a
// CLUSTER column) or one of them. Next to the terminal it is compact:
// markers, name, cluster, env and the reachability dot. Favorites come
// first; a filter hides servers; marked servers (Space, a) are the
// targets of :exec.
type serverList struct {
	theme    Theme
	table    *tview.Table
	all      []tsh.Server // as listed, every cluster; nil while a message is shown
	servers  []tsh.Server // visible rows: in the view, filtered, favorites first
	listedAt time.Time
	// stale is why the rows are a cached listing (the proxy cannot be
	// reached, say); "" for a live listing.
	stale  string
	filter filter.Filter
	// view is the cluster shown; "" shows every cluster.
	view     string
	clusters []tsh.Cluster
	short    map[string]string // cluster name -> short name
	favs     map[string]bool   // hostname -> favorite, for the current proxy
	marks    map[string]bool   // serverID -> marked
	open     map[string]bool   // serverID -> has a session
	compact  bool
	// sortBy and sortDesc order the rows (N, A, C, :sort); favorites
	// still come first.
	sortBy   sortKey
	sortDesc bool
}

// serverID identifies a server across clusters: two leaf clusters can
// each have a web-01.
func serverID(cluster, host string) string { return cluster + "\x00" + host }

func idOf(srv tsh.Server) string { return serverID(srv.Cluster, srv.Hostname) }

func newServerList(theme Theme) *serverList {
	s := &serverList{theme: theme, favs: map[string]bool{}, marks: map[string]bool{}, short: map[string]string{}}
	s.table = tview.NewTable().
		SetFixed(1, 0).
		SetSelectable(true, false)
	s.table.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	s.applyTheme(theme)
	s.setMessage("Starting…", theme.DialogPlaceholder)
	return s
}

func (s *serverList) applyTheme(theme Theme) {
	s.theme = theme
	s.table.SetSelectedStyle(tcell.StyleDefault.Foreground(theme.TableCursorFg).Background(theme.TableCursorBg))
	s.table.SetBorderColor(theme.Border).SetTitleColor(theme.Title).SetBackgroundColor(theme.Bg)
}

// viewLabel names the view for the title: "all" or the cluster's short
// name; "" before any cluster is known.
func (s *serverList) viewLabel() string {
	switch {
	case s.view != "":
		return s.shortName(s.view)
	case len(s.clusters) > 0:
		return "all"
	}
	return ""
}

// shortName is the cluster's short name, or name itself if unknown.
func (s *serverList) shortName(name string) string {
	if n, ok := s.short[name]; ok {
		return n
	}
	return name
}

func (s *serverList) title(count string) {
	t := fmt.Sprintf(" [%s::b]Servers", tag(s.theme.Title))
	if v := s.viewLabel(); v != "" {
		t += fmt.Sprintf("([%s]%s[%s])", tag(tcell.ColorFuchsia), tview.Escape(v), tag(s.theme.Title))
	}
	if count != "" {
		t += fmt.Sprintf("[%s]%s", tag(s.theme.Counter), tview.Escape("["+count+"]"))
	}
	if !s.filter.Empty() && s.all != nil {
		t += fmt.Sprintf("[%s] /%s", tag(s.theme.Info), tview.Escape(s.filter.String()))
	}
	// The sorted column's header carries the arrow; name the key here
	// when that column is hidden (compact view, one cluster).
	if s.sortBy != sortNone && s.all != nil && !s.hasColumn(s.sortBy.column()) {
		t += fmt.Sprintf("[%s] %s%s", tag(s.theme.Info), strings.ToLower(s.sortBy.column()), arrow(s.sortDesc))
	}
	if n := len(s.markedHosts()); n > 0 && s.all != nil {
		t += fmt.Sprintf("[%s] ✓%d", tag(s.theme.Warn), n)
	}
	if s.stale != "" && s.all != nil {
		t += fmt.Sprintf("[%s::b] STALE %s", tag(s.theme.Warn), tview.Escape(staleAge(s.listedAt, time.Now())))
	}
	s.table.SetTitle(t + "[-::-] ")
}

// staleAge says how old a cached listing is: "listed 3h ago".
func staleAge(listedAt, now time.Time) string {
	d := now.Sub(listedAt)
	switch {
	case listedAt.IsZero():
		return "cached"
	case d < time.Minute:
		return "listed just now"
	case d < time.Hour:
		return fmt.Sprintf("listed %dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("listed %dh ago", int(d.Hours()))
	}
	return "listed " + listedAt.Local().Format("2006-01-02")
}

// setMessage replaces the table with one line of text.
func (s *serverList) setMessage(text string, color tcell.Color) {
	s.all, s.servers, s.stale = nil, nil, ""
	s.table.Clear()
	s.title("")
	s.table.SetCell(0, 0, tview.NewTableCell(text).SetTextColor(color).SetSelectable(false).SetExpansion(1))
}

// setClusters records the proxy's clusters, for the short names, the
// title and the CLUSTER column. A view on a cluster that is gone falls
// back to all.
func (s *serverList) setClusters(clusters []tsh.Cluster) {
	s.clusters = clusters
	names := make([]string, len(clusters))
	for i, c := range clusters {
		names[i] = c.Name
	}
	s.short = shortClusterNames(names)
	if s.view != "" {
		if _, ok := s.short[s.view]; !ok {
			s.view = ""
		}
	}
}

// setView shows one cluster ("" for all), keeping the selected server if
// it is still visible.
func (s *serverList) setView(cluster string) {
	s.view = cluster
	keep := s.selectedID()
	if s.all == nil {
		s.title("")
		return
	}
	s.rebuild(keep)
}

// setServers shows a new listing, keeping the selected server and the
// marks of servers that are still there.
func (s *serverList) setServers(servers []tsh.Server, listedAt time.Time) {
	s.setListing(servers, listedAt, "")
}

// setStale shows a cached listing, marked stale in the title for reason.
func (s *serverList) setStale(servers []tsh.Server, listedAt time.Time, reason string) {
	s.setListing(servers, listedAt, reason)
}

func (s *serverList) setListing(servers []tsh.Server, listedAt time.Time, stale string) {
	keep := s.selectedID()
	s.all = append([]tsh.Server{}, servers...)
	s.listedAt, s.stale = listedAt, stale
	present := map[string]bool{}
	for _, srv := range s.all {
		present[idOf(srv)] = true
	}
	for id := range s.marks {
		if !present[id] {
			delete(s.marks, id)
		}
	}
	s.rebuild(keep)
}

// inView reports whether srv belongs to the current view.
func (s *serverList) inView(srv tsh.Server) bool {
	return s.view == "" || srv.Cluster == s.view
}

// viewServers is every listed server of the current view.
func (s *serverList) viewServers() []tsh.Server {
	if s.view == "" {
		return s.all
	}
	var out []tsh.Server
	for _, srv := range s.all {
		if s.inView(srv) {
			out = append(out, srv)
		}
	}
	return out
}

// countIn is the number of listed servers of cluster ("" for all).
func (s *serverList) countIn(cluster string) int {
	if cluster == "" {
		return len(s.all)
	}
	n := 0
	for _, srv := range s.all {
		if srv.Cluster == cluster {
			n++
		}
	}
	return n
}

// rebuild recomputes the visible rows and redraws, selecting keep (a
// serverID) if it is visible.
func (s *serverList) rebuild(keep string) {
	if s.all == nil {
		return
	}
	vis := s.filter.Apply(s.viewServers())
	sorted := make([]tsh.Server, len(vis))
	copy(sorted, vis)
	sort.SliceStable(sorted, func(i, j int) bool { return s.less(sorted[i], sorted[j]) })
	s.servers = sorted
	row := 1
	for i, srv := range s.servers {
		if idOf(srv) == keep {
			row = i + 1
		}
	}
	s.render(row)
}

// selected returns the server under the cursor.
func (s *serverList) selected() (tsh.Server, bool) {
	row, _ := s.table.GetSelection()
	if s.servers == nil || row < 1 || row > len(s.servers) {
		return tsh.Server{}, false
	}
	return s.servers[row-1], true
}

func (s *serverList) selectedHost() string {
	if srv, ok := s.selected(); ok {
		return srv.Hostname
	}
	return ""
}

func (s *serverList) selectedID() string {
	if srv, ok := s.selected(); ok {
		return idOf(srv)
	}
	return ""
}

// setFilter applies f, keeping the selection when it stays visible.
func (s *serverList) setFilter(f filter.Filter) {
	s.filter = f
	s.rebuild(s.selectedID())
}

// setFavorites replaces the favorite set (hostnames).
func (s *serverList) setFavorites(favs map[string]bool) {
	s.favs = favs
	s.rebuild(s.selectedID())
}

// toggleMark marks or unmarks the selected server and moves down.
func (s *serverList) toggleMark() {
	srv, ok := s.selected()
	if !ok {
		return
	}
	if id := idOf(srv); s.marks[id] {
		delete(s.marks, id)
	} else {
		s.marks[id] = true
	}
	row, _ := s.table.GetSelection()
	s.render(min(row+1, len(s.servers)))
}

// markAll marks every visible server, or unmarks them all when they are
// all marked already.
func (s *serverList) markAll() {
	all := len(s.servers) > 0
	for _, srv := range s.servers {
		if !s.marks[idOf(srv)] {
			all = false
		}
	}
	for _, srv := range s.servers {
		if all {
			delete(s.marks, idOf(srv))
		} else {
			s.marks[idOf(srv)] = true
		}
	}
	s.rerender()
}

func (s *serverList) clearMarks() {
	s.marks = map[string]bool{}
	s.rerender()
}

// markedHosts lists the marked servers, in list order. Marks survive a
// view change, so this can include servers of other clusters.
func (s *serverList) markedHosts() []tsh.Server {
	var out []tsh.Server
	for _, srv := range s.all {
		if s.marks[idOf(srv)] {
			out = append(out, srv)
		}
	}
	return out
}

// hostnames lists every server's hostname once, for completion.
func (s *serverList) hostnames() []string {
	out := make([]string, 0, len(s.all))
	seen := map[string]bool{}
	for _, srv := range s.all {
		if !seen[srv.Hostname] {
			seen[srv.Hostname] = true
			out = append(out, srv.Hostname)
		}
	}
	return out
}

// find looks host up, preferring the current view when two clusters have
// a server of that name.
func (s *serverList) find(host string) (tsh.Server, bool) {
	var other *tsh.Server
	for i, srv := range s.all {
		if srv.Hostname != host {
			continue
		}
		if s.inView(srv) {
			return srv, true
		}
		if other == nil {
			other = &s.all[i]
		}
	}
	if other != nil {
		return *other, true
	}
	return tsh.Server{}, false
}

// setOpen marks the servers (serverIDs) that have a session.
func (s *serverList) setOpen(open map[string]bool) {
	changed := len(open) != len(s.open)
	for h := range open {
		if !s.open[h] {
			changed = true
		}
	}
	s.open = open
	if changed && s.servers != nil {
		s.rerender()
	}
}

func (s *serverList) setCompact(on bool) {
	if s.compact != on {
		s.compact = on
		if s.servers != nil {
			s.rerender()
		}
	}
}

// rerender redraws the rows, keeping the selection.
func (s *serverList) rerender() {
	if s.servers == nil {
		return
	}
	row, _ := s.table.GetSelection()
	s.render(row)
}

// showClusterColumn reports whether rows need a CLUSTER column: in the
// all view, when the servers come from more than one cluster.
func (s *serverList) showClusterColumn() bool {
	if s.view != "" {
		return false
	}
	first := ""
	for i, srv := range s.all {
		if i == 0 {
			first = srv.Cluster
		} else if srv.Cluster != first {
			return true
		}
	}
	return false
}

// column is one table column: its header, whether it takes spare width,
// and its cell for a server.
type column struct {
	head   string
	expand bool
	cell   func(srv tsh.Server) (string, tcell.Color)
}

func (s *serverList) columns() []column {
	plain := func(f func(tsh.Server) string) func(tsh.Server) (string, tcell.Color) {
		return func(srv tsh.Server) (string, tcell.Color) { return f(srv), s.theme.TableFg }
	}
	cols := []column{
		{"", false, func(srv tsh.Server) (string, tcell.Color) {
			marks := []rune("   ")
			if s.marks[idOf(srv)] {
				marks[0] = '✓'
			}
			if s.favs[srv.Hostname] {
				marks[1] = '★'
			}
			if s.open[idOf(srv)] {
				marks[2] = '▸'
			}
			return string(marks), s.theme.Warn
		}},
		{"NAME", true, plain(func(srv tsh.Server) string { return srv.Hostname })},
	}
	if s.showClusterColumn() {
		cols = append(cols, column{"CLUSTER", true, func(srv tsh.Server) (string, tcell.Color) {
			return s.shortName(srv.Cluster), s.theme.Info
		}})
	}
	cols = append(cols,
		column{"ENV", true, plain(func(srv tsh.Server) string { return srv.Labels["env"] })},
		column{"", false, s.reachDot},
	)
	if s.compact {
		return cols
	}
	return append(cols,
		column{"ADDRESS", true, plain(func(srv tsh.Server) string {
			if srv.Addr == "" {
				return "⟵ tunnel"
			}
			return srv.Addr
		})},
		column{"VERSION", true, plain(func(srv tsh.Server) string { return srv.Version })},
		column{"LABELS", true, plain(func(srv tsh.Server) string { return formatLabels(srv.Labels) })},
	)
}

// hasColumn reports whether the list shows the column headed head.
func (s *serverList) hasColumn(head string) bool {
	for _, c := range s.columns() {
		if c.head == head {
			return true
		}
	}
	return false
}

func (s *serverList) render(selectRow int) {
	s.table.Clear()
	total := len(s.viewServers())
	count := fmt.Sprint(total)
	if !s.filter.Empty() {
		count = fmt.Sprintf("%d/%d", len(s.servers), total)
	}
	s.title(count)
	cols := s.columns()
	for i, c := range cols {
		head := c.head
		if s.sortBy != sortNone && head == s.sortBy.column() {
			head += arrow(s.sortDesc)
		}
		s.table.SetCell(0, i, tview.NewTableCell(head).
			SetTextColor(s.theme.TableHeader).SetAttributes(tcell.AttrBold).SetSelectable(false).SetExpansion(expansion(c.expand)))
	}
	if len(s.servers) == 0 {
		msg := "No servers visible to you on this proxy."
		switch {
		case !s.filter.Empty():
			msg = "No server matches the filter " + s.filter.String() + " (/ to change, :filter to clear)."
		case s.view != "":
			msg = "No servers visible to you in cluster " + s.shortName(s.view) + " (0 shows all)."
		}
		s.table.SetCell(1, 1, tview.NewTableCell(tview.Escape(msg)).
			SetTextColor(s.theme.DialogPlaceholder).SetSelectable(false))
		return
	}
	for row, srv := range s.servers {
		for i, c := range cols {
			text, color := c.cell(srv)
			s.table.SetCell(row+1, i, tview.NewTableCell(tview.Escape(text)).SetTextColor(color).SetExpansion(expansion(c.expand)))
		}
	}
	s.table.Select(min(max(selectRow, 1), len(s.servers)), 0)
}

// reachDot is the reachability marker from the node's heartbeat in
// `tsh ls`: ● online, ○ stale, · unknown.
func (s *serverList) reachDot(srv tsh.Server) (string, tcell.Color) {
	switch health.NodeStatus(srv, s.listedAt) {
	case health.NodeOnline:
		return "●", s.theme.OK
	case health.NodeStale:
		return "○", s.theme.Err
	}
	return "·", s.theme.DialogPlaceholder
}

// expansion keeps the marker and dot columns narrow.
func expansion(expand bool) int {
	if expand {
		return 1
	}
	return 0
}

// shortClusterNames gives each cluster the first label of its name
// ("afr" for afr.teleport.example.com) when that is unique, else the
// full name.
func shortClusterNames(names []string) map[string]string {
	first := func(n string) string {
		if i := strings.IndexByte(n, '.'); i > 0 {
			return n[:i]
		}
		return n
	}
	uses := map[string]int{}
	for _, n := range names {
		uses[first(n)]++
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		if uses[first(n)] == 1 {
			out[n] = first(n)
		} else {
			out[n] = n
		}
	}
	return out
}

func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k != "env" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ",")
}

// footer shows the key hints, k9s style.
type footer struct {
	theme Theme
	view  *tview.TextView
}

func newFooter(theme Theme) *footer {
	f := &footer{theme: theme, view: tview.NewTextView().SetDynamicColors(true).SetWrap(false)}
	f.view.SetBorderPadding(0, 0, 1, 1)
	return f
}

type hint struct{ key, text string }

func (f *footer) set(hints ...hint) {
	var b strings.Builder
	for _, h := range hints {
		fmt.Fprintf(&b, "[%s::b]<%s>[%s::-] %s  ", tag(f.theme.MenuKey), h.key, tag(f.theme.MenuText), h.text)
	}
	f.view.SetText(b.String())
}
