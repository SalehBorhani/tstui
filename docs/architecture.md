# Architecture

tstui is a Go program built on `tview` and `tcell`. It never talks to Teleport's APIs itself. Every Teleport operation runs the `tsh` binary, apart from one unauthenticated health check, `GET /webapi/ping`. This page describes how the pieces fit together. [CLAUDE.md](../CLAUDE.md) holds the rules the code must follow, and [tsh-notes.md](tsh-notes.md) lists what tstui assumes about tsh.

## Packages

```
cmd/tstui            flags, env and config precedence, signals, crash report, wiring
  │
  ├─ internal/config     config.yaml: load, validate, save (0600, atomic, .bak)
  ├─ internal/auth       Secret, Credentials, the tsh login state machine, the expiry watcher
  └─ internal/ui         the App: header, server list, tabs and panes, dialogs, palette
        │
        ├─ internal/session    Session (one PTY process + emulator), tab Manager, spawners,
        │                      stderr classification, remembered logins
        │     └─ internal/vterm    charmbracelet/x/vt behind a mutex; key, paste, mouse encoders
        ├─ internal/layout     split-pane tree and geometry (pure)
        ├─ internal/health     version rule, expiry thresholds, node heartbeat, proxy Monitor
        ├─ internal/palette    ':' parser, completion, history (pure)
        ├─ internal/filter     server filter language (pure)
        ├─ internal/transfer   tsh scp argv, local path completion, transfer log
        ├─ internal/portfwd    forward specs, port conflict checks, forward Manager
        ├─ internal/multiexec  concurrency-limited runner, risky-command detector, audit log
        │
        └─ internal/tsh        Client interface: the only way to run tsh
                               CLI (real), Fake (in memory), runner, PTY, parsers, server cache
```

The packages marked *pure* do no I/O and are tested with tables alone. `internal/ui` is the only package that imports tview.

## The tsh boundary

`tsh.Client` is the interface every tsh call goes through:

| Method | tsh command | Notes |
|---|---|---|
| `Version` | `tsh version --client --format=json` | no network; falls back to no `--client` on old tsh |
| `ServerPing` | none: tstui's own `GET https://<proxy>/webapi/ping` | server version, auth type, MFA, latency |
| `Status` | `tsh status --client --format=json --proxy=P` | reads local files only |
| `ListClusters` | `tsh clusters --format=json` | root first, then leaves |
| `ListServers` | `tsh ls --format=json --proxy=P --cluster=C` | one call per cluster, four at a time (`tsh.ListAll`) |
| `Profiles` | none: reads `~/.tsh/*.yaml` | |
| `StartPTY` | anything interactive: `tsh login`, `tsh ssh`, `tsh ssh -N --forward` | returns a `Process` with a PTY |
| `Exec` | non-interactive: `tsh scp`, `tsh ssh host cmd` | no PTY, empty stdin, bounded output |
| `Logout` | `tsh logout --proxy=P` | |

Arguments are always an argv. No call goes through a shell. A user-supplied value that tsh could read as a flag (one with a leading `-`) is refused or made absolute before it gets there. Every child gets `tsh.SafeEnv(environ)`, which drops `TSTUI_PASSWORD` and `TSTUI_MFA`. Every command is logged through `tsh.RedactArgs`, with its exit code and duration and never its output.

Tests use `tsh.Fake` (in memory) or the fake tsh script in `testdata/fake-tsh/`. The script is driven by `FAKE_TSH_STATE` and other variables documented in its header, and it replays fixtures captured from a real tsh.

## Login

`auth.Service.Login` runs `tsh login --proxy=P [--user=U] [--auth=C]` in a PTY with echo off. It reads tsh's output line by line and matches each line against `auth.Patterns` (built-in regexes plus the config's `tsh_prompts`):

```
            ┌───────────── password prompt ─────────────┐
start ──► reading ──► have a password? ── yes ──► type it, wipe it ──► reading
            │                  └── no ──► Prompter.Ask (popup) ──┘
            ├── OTP / tap / PIN / choice prompt: the same, with the matching question
            ├── "Detected security key tap" ──► Notify(EventKeyTapped)
            ├── SSO link ──► Notify(EventSSOURL)
            ├── "Logged in as" + exit 0 ──► success
            └── ERROR: line / exit ≠ 0 ──► typed error (ErrWrongPassword, ErrLocked, …)
```

A repeated prompt means the last answer was rejected, so the popup shows it as a retry. tsh v18 first sends two terminal queries and waits 5 seconds for replies, so the login answers them the way a dark terminal would. The whole login is bounded by `DefaultLoginTimeout` (3 minutes). `auth.Watcher` polls `tsh status` and reports the 30-minute and 5-minute thresholds and expiry.

The UI implements `auth.Prompter` with `LoginModal`. `Ask` is called from the login goroutine and blocks until the user answers or the question is withdrawn. For example, touching the key while the OTP field is open withdraws the OTP question.

## The UI goroutine

tview runs one event loop. Nothing slow may run on it, so:

- Every tsh call runs in a goroutine started with `App.goSafe`, which recovers panics. The goroutine hands its result back with `App.post(f)`, which queues `f` to run on the UI goroutine.
- `pump` is the only goroutine that calls tview's `QueueUpdateDraw`. Because `QueueUpdateDraw` waits for the event loop, `App.shutdown` closes `quit`, waits for `pump` to return, and only then stops tview. Code never calls `tv.Stop` directly.
- Sessions report output through `Options.OnChange`, which must not block. The app turns it into a signal, and `redrawLoop` turns the signals into at most one redraw per 16 ms frame.
- `Run` returns only after every background goroutine has ended, every session and port forward is closed, and the credentials are wiped. The `session`, `vterm` and `ui` tests run goleak to enforce this.

## Sessions, tabs and panes

A `session.Session` is one process in a PTY (`tsh ssh login@host`, or bash with `--demo-shell`) and its `vterm.Terminal`. A reader goroutine feeds PTY output into the emulator. tsh's stderr is a separate pipe, shown in the pane and kept (last 8 KiB) so that `session.Classify` can explain a failure. The emulator is `charmbracelet/x/vt` v0.1.0 with a local patch for scroll speed and scrollback ([terminal-decision.md](terminal-decision.md)).

`session.Manager` owns the tabs. Each tab holds a `layout.Tree` of panes, equal-sized nested splits like tmux. `ui.paneView` draws the active tab: it gets rectangles and dividers from `layout`, and each pane is a `TerminalView` that draws the emulator's cells and encodes keys, paste and mouse events for the remote program. Resizing a pane resizes the PTY and the emulator.

Focus works like tmux. While a terminal has focus it gets every key. The prefix key (`keybindings.focus`, `Ctrl-B` by default) arms a one-key command such as `tab`, a split, a pane move, zoom or detach.

## Health and offline mode

`health.Monitor` pings every known proxy once a minute, four at a time, and reads each proxy's certificate time with `tsh status --client`. `health.CheckVersions` is the only place the client/server version rule lives. Node reachability comes from the `metadata.expires` heartbeat in `tsh ls`. tstui never probes a node.

After each successful listing, `tsh.ServerCache` saves it to `$XDG_STATE_HOME/tstui/servers/`. When a proxy cannot be reached, the server list shows the cached copy, marked `STALE` with its age.

## Crashes and signals

A panic on the UI goroutine or in any `goSafe` goroutine is recovered and recorded. The app then shuts down the normal way: sessions and forwards are killed, secrets wiped and the screen restored. `Run` returns a `*ui.PanicError`, and `main` prints its value and stack (code locations only) and exits with status 3. SIGINT, SIGTERM and SIGHUP cancel the root context, which goes through the same shutdown, and the exit status is 128 + the signal number. A second signal kills tstui at once. Core dumps are disabled at start (`RLIMIT_CORE` = 0) for tstui and its children.

## Build

Builds are static (`CGO_ENABLED=0`), from `vendor/` (`-mod=vendor`), with `-trimpath`. The version, commit and date come from ldflags (`scripts/version.sh`). `make offline-build` builds in a network namespace with empty Go caches to prove the build needs no network. `make release` (`scripts/package.sh`) builds the plain binaries, `.tar.gz` and `.deb` artifacts, with `THIRD_PARTY_LICENSES` collected from `vendor/`, using only Go and standard tools, so it works air-gapped. Windows is not a target: `tsh.StartPTY` is Unix-only (`pty_other.go` returns `ErrPTYUnsupported`).
