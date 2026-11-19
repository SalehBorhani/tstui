package vt

import (
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
)

// DefaultScrollbackSize is the default maximum number of lines in the scrollback buffer.
const DefaultScrollbackSize = 10000

// Scrollback represents a scrollback buffer that stores lines scrolled off the screen.
//
// tstui patch: the lines are kept in a ring, so pushing a line onto a full
// buffer is O(width) instead of O(maxLines), and the evicted line's storage
// is reused.
type Scrollback struct {
	lines    []uv.Line // ring storage; lines[head] is the oldest once full
	head     int
	maxLines int
}

// NewScrollback creates a new scrollback buffer with the given maximum number of lines.
func NewScrollback(maxLines int) *Scrollback {
	if maxLines <= 0 {
		maxLines = DefaultScrollbackSize
	}
	return &Scrollback{
		lines:    make([]uv.Line, 0, min(maxLines, 1000)), // Pre-allocate reasonable capacity
		maxLines: maxLines,
	}
}

// Push adds a line to the scrollback buffer.
// If the buffer is full, the oldest line is removed.
func (s *Scrollback) Push(line uv.Line) {
	if s == nil || s.maxLines <= 0 {
		return
	}

	// Find last non-empty cell to trim trailing empty cells.
	// This helps with wrapping and window resizing.
	lastNonEmpty := -1
	for i := len(line) - 1; i >= 0; i-- {
		// tstui patch: cheap field checks first; Cell.Equal is much slower
		// and this loop runs over every scrolled line.
		if c := &line[i]; !blankCell(c) {
			lastNonEmpty = i
			break
		}
	}
	content := line[:lastNonEmpty+1]

	if len(s.lines) < s.maxLines {
		s.lines = append(s.lines, slices.Clone(content))
		return
	}
	// Full: overwrite the oldest line, reusing its storage.
	s.lines[s.head] = append(s.lines[s.head][:0], content...)
	s.head = (s.head + 1) % len(s.lines)
}

// PushN adds n lines from the buffer starting at line y to the scrollback.
func (s *Scrollback) PushN(buf *uv.RenderBuffer, y, n int) {
	if s == nil || buf == nil || n <= 0 {
		return
	}

	for i := range min(n, buf.Height()-y) {
		if line := buf.Line(y + i); line != nil {
			s.Push(line)
		}
	}
}

// Len returns the number of lines in the scrollback buffer.
func (s *Scrollback) Len() int {
	if s == nil {
		return 0
	}
	return len(s.lines)
}

// MaxLines returns the maximum number of lines the scrollback buffer can hold.
func (s *Scrollback) MaxLines() int {
	if s == nil {
		return 0
	}
	return s.maxLines
}

// linearize puts the oldest line first in storage.
func (s *Scrollback) linearize() {
	if s.head == 0 {
		return
	}
	out := make([]uv.Line, 0, len(s.lines))
	out = append(out, s.lines[s.head:]...)
	out = append(out, s.lines[:s.head]...)
	s.lines, s.head = out, 0
}

// SetMaxLines sets the maximum number of lines in the scrollback buffer.
// If the current number of lines exceeds the new maximum, oldest lines are removed.
func (s *Scrollback) SetMaxLines(maxLines int) {
	if s == nil || maxLines <= 0 {
		return
	}

	s.linearize()
	s.maxLines = maxLines
	if len(s.lines) > maxLines {
		// Remove oldest lines
		s.lines = slices.Clone(s.lines[len(s.lines)-maxLines:])
	}
}

// Line returns the line at the given index.
// Index 0 is the oldest line, Len()-1 is the most recent.
// Returns nil if index is out of bounds.
func (s *Scrollback) Line(index int) uv.Line {
	if s == nil || index < 0 || index >= len(s.lines) {
		return nil
	}
	return s.lines[(s.head+index)%len(s.lines)]
}

// Lines returns all lines in the scrollback buffer.
// Index 0 is the oldest line.
func (s *Scrollback) Lines() []uv.Line {
	if s == nil {
		return nil
	}
	s.linearize()
	return s.lines
}

// Clear removes all lines from the scrollback buffer.
func (s *Scrollback) Clear() {
	if s == nil {
		return
	}
	s.lines, s.head = s.lines[:0], 0
}

// CellAt returns the cell at the given position in the scrollback buffer.
// x is the column, y is the line index (0 = oldest).
// Returns nil if position is out of bounds.
func (s *Scrollback) CellAt(x, y int) *uv.Cell {
	line := s.Line(y)
	if line == nil || x < 0 || x >= len(line) {
		return nil
	}
	return &line[x]
}

// blankCell reports whether c is the zero cell or an unstyled space.
func blankCell(c *uv.Cell) bool {
	if c.Content != " " && c.Content != "" {
		return false
	}
	return c.Style.Fg == nil && c.Style.Bg == nil && c.Style.UnderlineColor == nil &&
		c.Style.Underline == 0 && c.Style.Attrs == 0 && c.Link.URL == "" &&
		(c.Width == 1 || (c.Width == 0 && c.Content == ""))
}
