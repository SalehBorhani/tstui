# Terminal emulator decision

**Decision:** `github.com/charmbracelet/x/vt` **v0.1.0**, with a small local patch in `third_party/charmbracelet-x-vt`: two performance fixes and one correctness fix. It is wired in with a `replace` directive in `go.mod` and vendored as usual. The patch is described below and is meant to go upstream.

Measured on 2026-11-18: Linux 7.3, i7-1165G7, Go 1.27.1. Each candidate had a throwaway spike harness. Programs ran in a real PTY (`creack/pty`, `TERM=xterm-256color`), their output went through the emulator, and the emulator's screen was dumped as text. A second set of probes wrote escape sequences straight into each emulator and read the cells back. The harness is not committed. The checks it ran are listed here so they can be rerun.

## Candidates

| | hinshun/vt10x | charmbracelet/x/vt | vito/midterm |
|---|---|---|---|
| Version | v0.0.0-20220301 (no tags) | v0.1.0 (tagged 2026-10-12) | v0.2.5 (2026-08-07) |
| Maintained | no, last commit 2022 | yes, Charm's terminal stack | yes, built for dagger's log UI |
| Cell model | one `rune`, 16 colour bits + flags | grapheme cluster + width, `color.Color`, underline styles, hyperlinks | one `rune` per cell |

## Feature checks

| Check | vt10x | charm/x/vt | midterm |
|---|---|---|---|
| vim: open, `:set number`, resize to 60x15, quit | ok | ok | not run |
| htop: render, resize 100x30 → 80x20 | ok | ok | not run |
| less: page, quit, alt screen restored | ok | ok | not run |
| mc, tmux | not installed here (the `mc` on this machine is the MinIO client). Their building blocks were probed instead, see the next two rows | | |
| DEC line drawing `ESC(0 lqk` (mc and tmux borders) | ┌─┐ | ┌─┐ | `lqk` ✘ |
| Scroll region `DECSTBM` (tmux panes) | ok | ok | ok |
| 256 colours `38;5;196` | ok | ok | ? |
| Truecolor `38;2;r;g;b` | **✘** stored as `r<<16\|g<<8\|b` in the same space as indexed colours, so `38;2;0;0;5` turns into palette colour 5 | ok (`color.RGBA`) | ? |
| Wide chars `日本語` | **✘** one cell each, so the line overflows | ok, width 2 plus a placeholder cell | ✘ |
| Combining marks `e◌́` | ✘ the mark gets its own cell | ok, one grapheme in one cell | ✘ |
| UTF-8 rune split across two writes | **✘** `Write` returns a short count and the rest of the rune is lost unless the caller re-feeds it | ok | ok |
| Alternate screen 1049 | ok | ok | ok |
| Scrollback | **none** | built in, 10k lines by default, resizable | callback per line |
| Mouse reporting | tracks the mode flags, no encoder | tracks the modes (1000/1002/1003/1006) and encodes events (`SendMouse`) | forwards the requests only |
| Bracketed paste 2004 | ✘ | ok (`Paste`) | ? |
| Key encoding (cursor key mode, etc.) | none | `SendKey` (not used: tstui has its own encoder, see below) | none |
| Replies to `ESC[6n` and `OSC 11 ?` (tsh v18 sends both at start and waits 5 s for the answer) | ok | ok | `6n` only |
| Resize | ok | ok | ok |

## Throughput

The input was written in 4 KiB chunks, the size a PTY read delivers, on a 120x40 screen. "yes" is `y\r\n` repeated (ONLCR turns `\n` into `\r\n` on a real PTY). "bigfile" is lines with SGR colours, bold, `—`, `λ` and `日本`.

| | yes | bigfile |
|---|---|---|
| vt10x | 7.1 MB/s | 25.2 MB/s |
| midterm v0.2.5 | 0.9 MB/s | 12.1 MB/s |
| charm/x/vt v0.1.0, **stock** | **0.1 MB/s** | 2.7 MB/s |
| charm/x/vt v0.1.0, **patched** | 3.1 MB/s (about 1M lines/s) | 14.3 MB/s |

Stock charm/x/vt is too slow for `yes` or `cat bigfile`. After Ctrl+C, the up to 2 MB that tsh's SSH window already holds would take about 20 s to drain. A profile showed two costs per line scrolled off the bottom:

1. `uv.Buffer.DeleteLineArea` copies every cell of the scroll region one by one: O(width × height) per linefeed, and a cell is about 100 bytes.
2. `Scrollback.Push` calls `slices.Delete(lines, 0, 1)` once the buffer is full: a memmove of all 10k line headers per line. Its trailing-blank scan also calls `Cell.Equal`, which is slow.

Upstream `main` (2026-11-01) has the same code.

## The patch (`third_party/charmbracelet-x-vt/PATCHES.md`)

- `Screen.DeleteLine`: when the scroll region spans the full width (the normal case), rotate the line slices instead of copying cells, then blank the lines that were recycled. uv's `Buffer.Lines` is exported and each line has its own allocation, so this is safe.
- `Scrollback`: a ring buffer. Pushing onto a full buffer overwrites the oldest line and reuses its storage, with no allocation in steady state. The blank check compares fields directly.
- Correctness, found by tstui's tests: upstream also pushed lines into the scrollback when a scroll region that does not start at the top row scrolled (a status line above a pane, for example). Now only a region that starts at row 0 feeds the scrollback, as in xterm.

Upstream's own test suite (`emulator_test.go`, `scrollback_test.go` and the rest) passes on the patched copy. tstui adds its own tests in `internal/vterm/terminal_test.go`: scrollback order after the ring wraps, scrolling inside regions, colours, wide characters, query replies, and a throughput benchmark (`BenchmarkTerminalScroll`).

## Why not vt10x

It is the fastest, but it is unmaintained, has no scrollback, gets truecolor wrong, has no notion of wide characters (CJK and emoji in a remote shell corrupt the line), loses runes split across reads, and has no bracketed paste. Fixing all of that would mean a much bigger fork than the two-function patch above.

## How tstui uses it

- `internal/vterm` wraps `vt.Emulator` with a mutex. The emulator is not safe for concurrent use; `vt.SafeEmulator` exists, but tstui needs to hold the lock across a whole frame. A goroutine drains the emulator's reply pipe (DSR, DA, OSC colour queries) into the PTY; without it, `Write` blocks on the pipe.
- Modes that matter to input (cursor keys, bracketed paste, mouse modes, alternate screen) are tracked through the `EnableMode` and `DisableMode` callbacks.
- Key encoding is tstui's own (`vterm.EncodeKey`, tcell key → xterm bytes, table-tested). tcell's key model does not map one to one onto `uv.KeyEvent`, and upstream's `SendKey` still has TODOs for modified keys.
- Colours: `ansi.BasicColor` and `ansi.IndexedColor` become tcell palette colours, anything else becomes `tcell.NewRGBColor`. The emulator's default colour (nil) maps to `tcell.ColorDefault`, so the host terminal's background shows through, like the rest of the UI.

## Known gaps

- Kitty keyboard protocol, `modifyOtherKeys`, and Sixel/kitty graphics are not supported.
- Reflow on resize: lines are cut or padded, not rewrapped (same as xterm).
- tmux and Midnight Commander were not run end to end because neither is installed on the spike machine. Run them once with `tstui --demo-shell`.
