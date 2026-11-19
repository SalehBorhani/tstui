package palette

// DefaultHistory is how many command lines History keeps.
const DefaultHistory = 100

// History is the list of command lines run, newest last, with a cursor
// for Up/Down browsing. It is not safe for concurrent use.
type History struct {
	items []string
	max   int
	pos   int    // index into items while browsing; len(items) when not
	draft string // what was typed before browsing started
}

// NewHistory keeps at most max lines; max <= 0 means DefaultHistory.
func NewHistory(max int) *History {
	if max <= 0 {
		max = DefaultHistory
	}
	return &History{max: max}
}

// Add records a line that was run and ends browsing. Empty lines and an
// immediate repeat are not recorded.
func (h *History) Add(line string) {
	if line != "" && (len(h.items) == 0 || h.items[len(h.items)-1] != line) {
		h.items = append(h.items, line)
		if len(h.items) > h.max {
			h.items = h.items[len(h.items)-h.max:]
		}
	}
	h.Reset()
}

// Reset ends browsing.
func (h *History) Reset() { h.pos, h.draft = len(h.items), "" }

// Prev moves to the older line and returns it. current is what the input
// holds now; it comes back after browsing past the newest line.
func (h *History) Prev(current string) string {
	if len(h.items) == 0 {
		return current
	}
	if h.pos == len(h.items) {
		h.draft = current
	}
	if h.pos > 0 {
		h.pos--
	}
	return h.items[h.pos]
}

// Next moves to the newer line and returns it, or the draft after the
// newest line.
func (h *History) Next() string {
	if h.pos >= len(h.items) {
		return h.draft
	}
	h.pos++
	if h.pos == len(h.items) {
		return h.draft
	}
	return h.items[h.pos]
}

// Items returns the recorded lines, oldest first.
func (h *History) Items() []string { return append([]string(nil), h.items...) }
