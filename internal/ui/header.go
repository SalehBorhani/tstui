package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/health"
	"github.com/root-ali/tstui/internal/tsh"
)

// headerInfo is everything the header shows.
type headerInfo struct {
	Version       string
	ClientVersion string
	Proxy         string
	Cluster       string
	User          string
	Roles         []string

	ServerVersion    string
	MinClientVersion string
	MFA              string
	Latency          time.Duration
	PingErr          error
	// HealthChecked is when the proxy was last pinged.
	HealthChecked time.Time

	Checking   bool // the first status check has not finished
	State      auth.CertState
	ValidUntil time.Time
	StatusErr  error

	// Clusters is the clusters panel: "all" first, then the current
	// proxy's root and leaf clusters. Empty before any listing.
	Clusters []clusterEntry
	// Sessions is the number of open session tabs.
	Sessions int
}

// clusterEntry is one line of the clusters panel.
type clusterEntry struct {
	// Key is the digit that shows the cluster; -1 when it has none.
	Key int
	// Name is the full cluster name, "" for the "all" line; Short is
	// what the panel shows.
	Name, Short string
	Root        bool
	Current     bool
	Count       int
	Loading     bool
	// Err is why the cluster's servers could not be listed.
	Err error
}

// proxyEntry is one known proxy, for the proxy picker and the monitor.
type proxyEntry struct {
	Name, Addr string
	Current    bool
	// Health, when Checked is set, is the proxy's last health report.
	Health health.Report
}

// header is the k9s-style info panel at the top: two columns of
// "Label: value", the clusters panel, and the logo on the right when the
// terminal is wide enough.
type header struct {
	theme    Theme
	view     *tview.Flex
	left     *tview.TextView
	middle   *tview.TextView
	clusters *tview.TextView
	logo     *tview.TextView
	logoSize logoSize
}

const headerHeight = 6

func newHeader(theme Theme) *header {
	h := &header{theme: theme}
	text := func() *tview.TextView {
		return tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	}
	h.left, h.middle, h.clusters, h.logo = text(), text(), text(), text()
	h.left.SetBorderPadding(0, 0, 1, 1)
	h.logo.SetBorderPadding(0, 0, 1, 1)
	h.view = tview.NewFlex().
		AddItem(h.left, 0, 3, false).
		AddItem(h.middle, 0, 3, false).
		AddItem(h.clusters, 0, 2, false).
		AddItem(h.logo, 0, 0, false) // sized by layoutLogo
	// The draw func runs before the flex lays out its items, so the logo
	// follows the width in the same frame.
	h.view.SetDrawFunc(func(_ tcell.Screen, x, y, w, ht int) (int, int, int, int) {
		h.layoutLogo(w)
		return x, y, w, ht
	})
	return h
}

// layoutLogo shows the logo size that fits width: the full logo on wide
// terminals, the compact one on medium ones, none on narrow ones.
func (h *header) layoutLogo(width int) {
	size := logoSizeFor(width)
	if size == h.logoSize {
		return
	}
	h.logoSize = size
	w := 0
	if size != logoNone {
		w = logoWidth(size) + 2 // border padding
	}
	h.view.ResizeItem(h.logo, w, 0)
	h.logo.SetText(logoText(size, h.theme))
}

func (h *header) render(in headerInfo, now time.Time) {
	label := func(l, v string) string {
		return fmt.Sprintf("[%s]%-10s[%s::b]%s[-::-]\n", tag(h.theme.InfoLabel), l, tag(h.theme.InfoValue), v)
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return tview.Escape(s)
	}

	var b strings.Builder
	b.WriteString(label("Proxy:", dash(in.Proxy)))
	b.WriteString(label("Cluster:", dash(in.Cluster)))
	b.WriteString(label("User:", dash(in.User)))
	b.WriteString(label("Roles:", dash(strings.Join(in.Roles, ","))))
	fmt.Fprintf(&b, "[%s]%-10s[-]%s\n", tag(h.theme.InfoLabel), "Login:", h.loginText(in, now))
	fmt.Fprintf(&b, "[%s]%-10s[-]%s", tag(h.theme.InfoLabel), "Health:", h.healthText(in, now))
	h.left.SetText(b.String())

	b.Reset()
	b.WriteString(label("tstui:", dash(in.Version)))
	b.WriteString(label("tsh:", dash(in.ClientVersion)))
	server := dash(in.ServerVersion)
	switch {
	case in.PingErr != nil:
		server = fmt.Sprintf("[%s]unreachable", tag(h.theme.Err))
	case in.ServerVersion != "":
		server = "v" + server
	}
	b.WriteString(label("Server:", server))
	b.WriteString(label("MFA:", dash(in.MFA)))
	latency := "-"
	if in.Latency > 0 {
		latency = in.Latency.Round(time.Millisecond).String()
	}
	b.WriteString(label("Latency:", latency))
	b.WriteString(label("Sessions:", fmt.Sprint(in.Sessions)))
	h.middle.SetText(b.String())

	h.clusters.SetText(h.clustersText(in.Clusters))
	h.logo.SetText(logoText(h.logoSize, h.theme)) // follows a skin change
}

// clustersText is the clusters panel, k9s-namespace style: a digit key,
// the short name and the server count (or why there is none).
func (h *header) clustersText(entries []clusterEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]Clusters[-]\n", tag(h.theme.InfoLabel))
	if len(entries) == 0 {
		fmt.Fprintf(&b, "[%s]none yet\n", tag(h.theme.DialogPlaceholder))
	}
	for i, c := range entries {
		if i == headerHeight-2 && len(entries) > headerHeight-1 {
			fmt.Fprintf(&b, "[%s]+%d more (:cluster)\n", tag(h.theme.DialogPlaceholder), len(entries)-i)
			break
		}
		key := "   "
		if c.Key >= 0 {
			key = fmt.Sprintf("<%d>", c.Key)
		}
		color, attr := h.theme.InfoValue, "-"
		if c.Current {
			color, attr = h.theme.Title, "b"
		}
		fmt.Fprintf(&b, "[%s::b]%s[-::-] [%s::%s]%s[-::-]", tag(h.theme.MenuKey), key, tag(color), attr, tview.Escape(c.Short))
		switch {
		case errors.Is(c.Err, tsh.ErrClusterOffline):
			fmt.Fprintf(&b, " [%s]✘ offline", tag(h.theme.Err))
		case c.Err != nil:
			fmt.Fprintf(&b, " [%s]✘ error", tag(h.theme.Err))
		case c.Loading:
			fmt.Fprintf(&b, " [%s]…", tag(h.theme.DialogPlaceholder))
		default:
			fmt.Fprintf(&b, " [%s]%d", tag(h.theme.Counter), c.Count)
		}
		if c.Root {
			fmt.Fprintf(&b, " [%s]root", tag(h.theme.DialogPlaceholder))
		}
		b.WriteString("[-]\n")
	}
	return b.String()
}

func (h *header) loginText(in headerInfo, now time.Time) string {
	c := func(color tcell.Color, s string) string { return fmt.Sprintf("[%s::b]%s[-::-]", tag(color), s) }
	switch {
	case in.Proxy == "":
		return c(h.theme.Err, "✘ no proxy")
	case in.Checking:
		return c(h.theme.DialogPlaceholder, "checking…")
	}
	left := in.ValidUntil.Sub(now)
	kept := ""
	if in.Sessions > 0 {
		kept = " · sessions kept"
	}
	switch in.State {
	case auth.CertValid, auth.CertExpiring:
		switch health.LevelFor(left) {
		case health.ExpiryPast:
			return c(h.theme.Err, "✘ expired"+kept+" · <L> log in")
		case health.ExpiryImminent:
			return c(h.theme.Err, "⚠ expires in "+countdown(left)+" · <L> re-login")
		case health.ExpirySoon:
			return c(tcell.ColorYellow, "⚠ expires in "+countdown(left)+" · <L> re-login")
		}
		return c(h.theme.OK, "✔ logged in · cert "+countdown(left))
	case auth.CertExpired:
		return c(h.theme.Err, "✘ expired"+kept+" · <l> log in")
	case auth.CertLoggedOut:
		return c(h.theme.Err, "✘ not logged in"+kept+" · <l> log in")
	}
	msg := "status unknown"
	if in.StatusErr != nil {
		msg = in.StatusErr.Error()
	}
	return c(h.theme.Err, "? "+tview.Escape(msg))
}

// healthText is the current proxy's reachability, version compatibility
// and the age of the last check.
func (h *header) healthText(in headerInfo, now time.Time) string {
	if in.Proxy == "" {
		return "-"
	}
	age := ""
	if !in.HealthChecked.IsZero() {
		age = fmt.Sprintf("[%s] · %s ago", tag(h.theme.DialogPlaceholder), ago(now.Sub(in.HealthChecked)))
	}
	if in.PingErr != nil {
		return fmt.Sprintf("[%s::b]✘ proxy unreachable[-::-]%s", tag(h.theme.Err), age)
	}
	if in.ServerVersion == "" {
		return fmt.Sprintf("[%s]checking…", tag(h.theme.DialogPlaceholder))
	}
	switch cp := health.CheckVersions(in.ClientVersion, in.ServerVersion, in.MinClientVersion); cp.Level {
	case health.CompatWarn:
		return fmt.Sprintf("[%s::b]⚠ %s[-::-]%s", tag(tcell.ColorYellow), tview.Escape(cp.Short), age)
	case health.CompatUnknown:
		return fmt.Sprintf("[%s]? tsh version unknown[-]%s", tag(h.theme.DialogPlaceholder), age)
	}
	return fmt.Sprintf("[%s::b]✔ reachable · versions ok[-::-]%s", tag(h.theme.OK), age)
}

// proxyState is the short health suffix of a proxies-panel line: a dot
// for reachability, the latency, and the certificate time left.
func (h *header) proxyState(r health.Report, now time.Time) string {
	if r.Checked.IsZero() {
		return ""
	}
	if !r.Reachable {
		return fmt.Sprintf(" [%s]✘ down[-]", tag(h.theme.Err))
	}
	cert := fmt.Sprintf("[%s]no session", tag(h.theme.DialogPlaceholder))
	if r.LoggedIn {
		left := r.ValidUntil.Sub(now)
		switch lvl := health.LevelFor(left); lvl {
		case health.ExpiryPast:
			cert = fmt.Sprintf("[%s]expired", tag(h.theme.Err))
		case health.ExpiryOK:
			cert = fmt.Sprintf("[%s]%s", tag(h.theme.OK), countdown(left))
		default:
			cert = fmt.Sprintf("[%s]%s", tag(tcell.ColorYellow), countdown(left))
		}
	}
	latency := ""
	if r.Latency > 0 {
		latency = r.Latency.Round(time.Millisecond).String() + " "
	}
	return fmt.Sprintf(" [%s]● [%s]%s%s[-]", tag(h.theme.OK), tag(h.theme.InfoValue), latency, cert)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// countdown formats d as 07h42m, or 04m12s under an hour.
func countdown(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%02dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%02dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
