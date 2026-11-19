# tstui features

This page covers what tstui adds on top of the server list and the embedded terminal: tabs, split panes and fullscreen, health and expiry warnings, the command palette, filters and favorites, file transfer, port forwarding and multi-exec. Press `?` in tstui for the same keys on one screen.

Every feature runs `tsh` with an argument list (argv). Nothing you type is passed to a local shell. Paths, hosts and commands go to tsh as separate arguments, and a value that tsh could read as a flag (a leading `-`) is refused or made absolute first.

## Keys on the server list

| Key | Action |
|---|---|
| `enter` | Connect, or show the server's open session |
| `n`, `shift-enter` | Open another session |
| `\|` / `-` | Open the server in a new pane right of / below the focused pane (see [Tabs, split panes and fullscreen](#tabs-split-panes-and-fullscreen)) |
| `F` | Fullscreen terminal |
| `/` | Filter servers (opens the palette on `:filter`) |
| `:` | Command palette |
| `space` | Mark or unmark the server (for `:exec`), then move down |
| `a` | Mark every server the filter shows; press again to unmark them |
| `esc` | Clear the marks, then dismiss the notice |
| `f` | Toggle favorite |
| `N` / `A` / `C` | Sort by name / address / cluster; press again to reverse (see [Sorting](#sorting)) |
| `u` / `d` | Upload to / download from the selected server |
| `0` | Show the servers of every cluster |
| `1`-`9` | Show the servers of one cluster (the number in the header's clusters panel) |
| `p` | Proxies |
| `l`, `L` | Log in (`L` is the re-login key that the expiry notices name) |
| `o` | Log out |
| `r` | Refresh servers, login state and health |
| `?` | Help |
| `q`, `ctrl-c` | Quit (asks first if sessions or port forwards are open) |

The terminal keeps every key, so these keys work only while the server list has focus. From a terminal, use `ctrl-b tab` to get back to the list, or `ctrl-b l` to log in.

## Tabs, split panes and fullscreen

Each tab holds one or more SSH sessions as split panes, like tmux windows. The tab bar shows the focused pane's name, `+N` for the other panes in the tab, and `Z` when the tab is zoomed. The dot shows the worst state among the tab's panes, and `*` marks unseen output in a tab you are not looking at.

Panes share their room equally. Splitting a pane in the same direction as its neighbours adds one more equal pane; splitting it the other way nests a new split inside it. A pane must stay at least 20 columns by 5 rows, so a split that would make it smaller is refused with a footer message. Only the focused pane gets keys; its border, or in fullscreen the divider next to it, is highlighted. A click on a pane focuses it.

In the terminal, press the prefix (`ctrl-b` by default, config `keybindings.focus`) and then:

| Key | Action |
|---|---|
| `\|` or `%` | Split: pick a server for a new pane right of this one |
| `-` or `"` | Split: pick a server for a new pane below this one |
| `←` `↑` `↓` `→` | Go to the pane in that direction |
| `o` | Next pane |
| `z` | Zoom: show only this pane; again shows all panes |
| `f` | Fullscreen: nothing but the terminal; again shows everything |
| `1`-`9`, `n` / `p` | Show tab 1-9 / the next / the previous tab (`alt-1`..`alt-9` work without the prefix) |
| `c` | Close the pane; the tab closes with its last pane |
| `d` | Detach: back to the list, sessions keep running |
| `r` | Reconnect |
| `[` | Scroll mode (`q` leaves) |
| `:` | Command palette |
| `?` | Help |
| `l` | Log in |
| `q` | Quit |
| `tab` | Back to the server list (this also leaves fullscreen) |
| the prefix again | Send the prefix key to the program |

The split picker lists the servers the list shows (the current cluster view), with the focused pane's server highlighted. Type to filter by hostname, use `↑` `↓` to pick, `enter` to open and `esc` to cancel. A split always opens a new session, even to a server that is already open. To reuse an open session instead, press `enter` on the server in the list: it focuses that session's pane, in whatever tab it is.

Fullscreen hides the header, the server list, the tab bar, the footer and the pane borders. Split panes are separated by one-cell lines. While the prefix is pending, and while a footer message lasts, a small badge in the bottom right corner shows it. Notices still appear in the top right corner, and the palette brings its line and the footer back while it is open. Fullscreen ends when you go to the list, detach, or close the last session.

## Clusters

One Teleport proxy can front several clusters: its root cluster and the leaf clusters that trust it. tstui asks `tsh clusters` for them and lists each one's servers with `tsh ls --cluster=…`, four at a time. The header's clusters panel shows them the way k9s shows namespaces:

```
Clusters
<0> all 155
<1> teleport-ast 59 root
<2> afr 55
<3> hwb ✘ offline
```

- `0` shows every cluster's servers. The list then has a CLUSTER column. `1`-`9` show one cluster. `:cluster afr`, `:cluster all`, or `:cluster` alone (a picker) do the same, and `tab` completes the names.
- Names are shortened to their first label (`afr` for `afr.teleport.example.com`) when that is unique. Commands accept the short or the full name.
- A cluster that tsh reports offline is not listed (`✘ offline`). A cluster whose listing fails shows `✘ error`, and a notice names it. The other clusters still load.
- Connecting, `u`/`d` (scp), `:pf` and `:exec` pass the server's own `--cluster`, so the same hostname in two clusters reaches the right node. Marks are per cluster too. The `:exec` confirmation, results, export and audit log name a leaf node as `cluster/host`. Favorites are by hostname, so starring `web-01` stars it in every cluster.
- A saved port forward can name its `cluster:`. Without one, tstui uses the cluster the server list finds the host in.
- If `tsh clusters` fails (for example on an older tsh), tstui lists the proxy's selected cluster alone, under `<0> all`.
- The view goes back to `all` when you switch proxy.

## Logo

The tstui logo sits at the right of the header. On terminals at least 160 columns wide it is the full 4-row logo with a tagline. From 120 to 159 columns it is a compact 3-row version. Below 120 it is hidden so the header text keeps its room. Its gradient follows the skin, and `high-contrast` draws it in plain yellow.

## Health and certificate expiry

**Header.** The `Health:` line shows the current proxy:

- `✔ reachable · versions ok · 12s ago`
- `⚠ tsh v16 too old for server v18` (see the version rule below)
- `✘ proxy unreachable`

`Latency:` is the round-trip time of the last `/webapi/ping`. The proxy picker (`p`) shows each proxy's reachability, latency and remaining certificate time (`● 23ms 07h42m`, `no session`, `expired`, `✘ down`).

tstui pings every known proxy once a minute: the configured ones, the `~/.tsh` profiles, and the current one. Up to four pings run at a time. Certificate times come from `tsh status --client`, which reads local files and makes no network call. `r` and `:refresh` check again at once.

**Version rule.** Teleport's [component compatibility](https://goteleport.com/docs/upgrading/overview/) says that servers support clients one major version behind, but not clients on a newer major version. A server on v18 therefore supports tsh v18 and v17. tstui warns when tsh is outside that range. It also warns when tsh is below the proxy's advertised `min_client_version`. The full message appears once as a notice. The rule lives in one function, `health.CheckVersions`.

**Certificate countdown.** The `Login:` line turns yellow under 30 minutes and red under 5. A notice in the top-right corner appears at 30 minutes (for 20 seconds), at 5 minutes, and at expiry (both stay until you log in again or press `esc`). Press `L` to log in again. Open sessions are never closed because the certificate expired.

**Server reachability.** The dot after ENV comes from what `tsh ls` already reports, the node's heartbeat (`metadata.expires`). tstui never opens a connection just to check a node.

| Dot | Meaning |
|---|---|
| `●` | The heartbeat was current when the list was loaded |
| `○` | The heartbeat had already run out: the node has probably gone |
| `·` | tsh gave no heartbeat time |

## Command palette

Press `:`. `tab` completes command names, proxy names, cluster names, hostnames, skins and `up`/`down`. `↑` and `↓` walk the history of this run. `esc` closes the palette. While you type, the footer shows the completions or the command's help. A unique prefix is enough (`:ref`).

| Command | Does |
|---|---|
| `:proxy [name]` (`:ctx`) | Switch proxy by config name, profile name or address; no name opens the picker |
| `:cluster [name\|all]` (`:cl`) | Show one cluster's servers, or all; no name opens the picker |
| `:login`, `:logout` | Log in or out of the current proxy |
| `:ssh <host>` | Open a session; the host does not have to be in the list |
| `:sessions` | List the open sessions (`tab.pane` for split tabs); `enter` focuses one |
| `:split [host]` | Open a server in a new pane below the focused one; no host opens the picker |
| `:vsplit [host]` (`:vs`) | Open a server in a new pane right of the focused one |
| `:fullscreen` (`:fs`) | Toggle fullscreen |
| `:zoom` | Toggle zoom of the focused pane |
| `:filter [expr]` | Filter the servers; no expression clears the filter |
| `:sort <name\|address\|cluster\|off>` | Sort the servers; the same key again reverses; `off` keeps tsh's order |
| `:fav [host]` | Toggle a favorite (default: the selected server) |
| `:refresh` | Reload the servers, the login state and the health checks |
| `:skin <name>` | `default`, `light`, `high-contrast`, or a skin file from `~/.config/tstui/skins/` (see the README); applied at once and saved |
| `:scp [up\|down]` | File transfer dialog for the selected server |
| `:pf` | Port forward manager |
| `:exec [command]` | Run a command on the marked servers |
| `:help` | Keys and commands |
| `:quit` | Quit |

## Filters and favorites

A filter is a list of terms separated by commas or spaces. A server must match every term:

| Term | Matches |
|---|---|
| `web` | the hostname or address contains `web` (case-insensitive) |
| `!web` | the hostname and address do not contain `web` |
| `env=prod` | label `env` is `prod` (case-insensitive) |
| `env!=prod` | label `env` is not `prod`, or is missing |
| `env=` | label `env` exists |

The list title shows `[shown/total]` and the filter.

`f` stars a server. Favorites are pinned at the top and saved under `favorites:` in `config.yaml`, keyed by the proxy's config name. For a proxy that is not in the config (one known only from `~/.tsh`), favorites last for the current run only.

## Sorting

`N`, `A` and `C` sort the server list by name, address or cluster, the way k9s sorts with shift and a letter. `:sort name`, `:sort address` and `:sort cluster` do the same. Pressing the same key again, or giving the same `:sort` key again, reverses the order. `:sort off` goes back to the order tsh lists the servers in, which is the default.

- The sorted column's header shows `↑` (ascending) or `↓` (descending). When that column is hidden, the list title names the sort instead: ADDRESS is hidden while a terminal is open next to the list, and CLUSTER is hidden when one cluster is shown.
- Favorites stay pinned at the top. The sort orders the favorites and the other servers separately.
- Names sort case-insensitively, with numbers compared by value, so `web-2` comes before `web-10`.
- Addresses sort IP addresses by value (IPv4 before IPv6), then hostnames, then by port. Tunnel nodes (`⟵ tunnel`, no address) come last in both directions.
- Cluster sorting uses the short names the list shows, then the server name.
- Rows with equal keys fall back to the name, then the cluster. The order lasts for the current run and applies to every proxy and cluster view.

**Saving rewrites config.yaml.** Favorites, saved port forwards and `:skin` all save the config. Saving rewrites the whole file, so comments are lost. The first save in each run copies the old file to `config.yaml.bak` first.

## File transfer (`u`, `d`, `:scp`)

The dialog has these fields: direction, login (the one last used on that server is preselected), local path, remote path, and recursive. The local path completes as you type (`↓` and `enter` pick an entry). A directory keeps the list open so you can go deeper.

- `~/` expands to your home directory. Relative paths are taken from tstui's working directory. The path is sent to tsh as an absolute path.
- An empty remote path means the login's home directory.
- Uploading a directory turns on recursive copy automatically.
- A local path containing `:` is refused, because tsh scp would read it as `host:path`.
- A spinner and the elapsed time show while the copy runs. `Cancel` or `esc` stops it. tsh's error appears in the dialog.

tstui runs `tsh scp --proxy=P [--recursive] SRC login@host:PATH` (or the reverse for a download) with no terminal. The dialog lists recent transfers. Each finished transfer is also appended to `$XDG_STATE_HOME/tstui/transfers.log` (mode 0600; default `~/.local/state/tstui/transfers.log`), recording time, direction, paths, duration and result.

## Port forwards (`:pf`)

The manager lists the forwards tstui runs, followed by the saved forwards that are not running.

| Key | Action |
|---|---|
| `n` | New forward: server, login, `[bind:]localport:host:port`, optional name, save to config |
| `enter` | Start a saved forward |
| `s` | Stop |
| `x` | Remove a stopped or failed forward from the list |
| `f` | Save a running forward to the config (asks for a name) |
| `D` | Delete a saved forward |
| `esc` | Close |

Each forward is one `tsh ssh -N --forward=BIND:LPORT:HOST:RPORT login@node` process, run in a pseudo-terminal so that a per-session MFA prompt can be answered with a key tap. The state is `starting` until the local port is listening, then `active`. A forward becomes `failed` (with tsh's error) when tsh exits on its own, and `stopped` when you stop it. Forwards bind to `127.0.0.1` unless you give an address. Only IPv4 addresses and `localhost` are accepted.

Before starting a forward, tstui checks the local port against the forwards it already runs (a wildcard bind `0.0.0.0` overlaps everything on that port) and tries to listen on it once. Either conflict is reported before tsh starts.

tstui stops every forward when it quits. If forwards are running, quitting asks first.

Saved forwards in `config.yaml`:

```yaml
port_forwards:
  - name: pg
    host: db-03
    login: ali          # optional: else the login last used on the server
    proxy: a-teleport   # optional: config name or address; default the current proxy
    forward: 5432:localhost:5432
```

Why `tsh ssh -L`: `tsh proxy ssh` is a ProxyCommand for OpenSSH, and `tsh proxy app` and `tsh proxy db` reach only registered apps and databases. Of the three, only `tsh ssh -L` forwards to any host:port behind a node.

## Multi-exec (`:exec`)

1. Mark servers with `space`, or press `a` to mark everything the filter shows. Without marks, the selected server is the target.
2. Type `:exec uptime`, or `:exec` alone to be asked for the command.
3. If you have several logins, pick one for the run.
4. Confirm. The dialog lists the hosts, the login, the command, the concurrency and the timeout. For a risky command the frame turns red, the reasons are listed, and you must type `yes`.
5. The results view shows a table of host, status, exit code and time. The selected host's stdout and stderr appear on the right. `x` cancels the run. `e` exports a text report (mode 0600, default `~/tstui-exec-<time>.txt`). `esc` closes the view once the run has finished. `:exec` returns to a run that is still going.

Each host runs `tsh ssh --proxy=P login@host '<command>'` with no terminal and empty stdin. The command is a single argument. The remote login shell interprets it, as with `ssh host 'cmd'`; no local shell does. At most `exec.concurrency` hosts run at once (default 5, at most 50), and each is killed after `exec.timeout` (default `60s`). Up to 256 KiB of stdout and stderr is kept per host. A command must not start with `-`.

```yaml
exec:
  concurrency: 10
  timeout: 2m
```

**Risky commands.** tstui asks for the typed `yes` when a command contains any of the following. Matching errs on the side of warning: the words count anywhere, so `sudo …`, `xargs rm -rf` and `bash -c "reboot"` are caught too.

- recursive `rm` (`-r`, `-R`, `-rf`, `--recursive`)
- `shutdown`, `reboot`, `halt`, `poweroff`, `init 0`/`6`, `systemctl reboot|poweroff|halt|…`
- `mkfs*`, `mke2fs`, `mkswap`, `wipefs`, `shred`, `fdisk`, `sfdisk`, `parted`, `sgdisk`
- `dd`
- a redirect onto a disk device (`> /dev/sda`)
- `chmod`, `chown` or `chgrp` with `-R` on `/`
- `kill -1`, `killall5`
- a fork bomb

**Audit log.** Every run is recorded in `$XDG_STATE_HOME/tstui/audit.log` (mode 0600; default `~/.local/state/tstui/audit.log`). Each line is JSON. A `start` line, written before anything runs, holds the time, proxy, login, hosts, command and risk reasons. A `finish` line holds each host's status and exit code. Output is not recorded. If the audit log cannot be written, the run is refused.

## Files tstui writes

| File | What |
|---|---|
| `~/.config/tstui/config.yaml` (+ `.bak`) | favorites, saved port forwards, skin |
| `~/.local/state/tstui/logins.json` | the login last used per server |
| `~/.local/state/tstui/servers/` | the last good server list per proxy, shown marked STALE when the proxy is unreachable |
| `~/.local/state/tstui/transfers.log` | finished transfers |
| `~/.local/state/tstui/audit.log` | multi-exec audit |

All of these are created with mode 0600, in directories with mode 0700.
