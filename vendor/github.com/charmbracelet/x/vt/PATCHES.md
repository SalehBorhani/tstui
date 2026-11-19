# Local patches to github.com/charmbracelet/x/vt v0.1.0

This is a copy of `github.com/charmbracelet/x/vt` at tag `vt/v0.1.0`
(commit 43427fdbad12, MIT licence, see LICENSE). tstui's go.mod replaces
the module with this directory. Why: see `docs/terminal-decision.md`.

Changes, all marked `tstui patch` in the code:

1. `screen.go`, `Screen.DeleteLine`: a full-width scroll rotates the line
   slices (`rotateLinesUp`) instead of copying every cell. Each linefeed at
   the bottom of the screen was O(width*height) cell copies.
2. `scrollback.go`: the scrollback is a ring buffer; pushing onto a full
   buffer was a memmove of every stored line header (`slices.Delete`). The
   evicted line's storage is reused. The trailing-blank scan uses a cheap
   field check (`blankCell`) instead of `Cell.Equal`.

The upstream tests (`*_test.go`) pass unchanged:
`cd third_party/charmbracelet-x-vt && go test ./...` (needs network or a
module cache for its own dependencies).

To upgrade: copy the new upstream release over this directory, re-apply
the changes above (or drop the ones upstream has fixed), run the upstream
tests, then `make vendor` in the repo root.
3. `screen.go`, `Screen.DeleteLine`: lines go to the scrollback only when
   the scroll region starts at the top row, as in xterm. Upstream also
   saved lines scrolled out of a region further down.
