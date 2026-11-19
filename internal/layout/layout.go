// Package layout is the pane tree of a tab: nested left/right and
// top/bottom splits of equal size, tmux-style, and the geometry that goes
// with it (pane rectangles, dividers, the neighbour in a direction).
//
// It is pure: the tree holds comparable leaf values and knows nothing
// about terminals or screens.
package layout

// Dir is how a split node arranges its children.
type Dir int

const (
	// Horizontal puts the children side by side, left to right.
	Horizontal Dir = iota
	// Vertical stacks the children, top to bottom.
	Vertical
)

// Direction is a way to move from one pane to another.
type Direction int

// The four directions.
const (
	Left Direction = iota
	Right
	Up
	Down
)

// Rect is a screen rectangle in cells.
type Rect struct{ X, Y, W, H int }

// Smallest pane CanSplit allows.
const (
	MinCols = 20
	MinRows = 5
)

// Placed is a leaf and where it is drawn.
type Placed[T comparable] struct {
	Value T
	Rect  Rect
}

type node[T comparable] struct {
	leaf   T
	isLeaf bool
	dir    Dir
	kids   []*node[T]
	parent *node[T]
}

// Tree is a pane tree. The zero value is empty; New starts one with a
// single pane. Leaf values must be unique.
type Tree[T comparable] struct{ root *node[T] }

// New returns a tree with one pane.
func New[T comparable](leaf T) *Tree[T] {
	return &Tree[T]{root: &node[T]{leaf: leaf, isLeaf: true}}
}

func (t *Tree[T]) find(leaf T) *node[T] {
	var walk func(n *node[T]) *node[T]
	walk = func(n *node[T]) *node[T] {
		if n == nil {
			return nil
		}
		if n.isLeaf {
			if n.leaf == leaf {
				return n
			}
			return nil
		}
		for _, k := range n.kids {
			if f := walk(k); f != nil {
				return f
			}
		}
		return nil
	}
	return walk(t.root)
}

// Contains reports whether leaf is in the tree.
func (t *Tree[T]) Contains(leaf T) bool { return t.find(leaf) != nil }

// Leaves returns the panes in reading order (left to right, top to bottom
// within each split).
func (t *Tree[T]) Leaves() []T {
	var out []T
	var walk func(n *node[T])
	walk = func(n *node[T]) {
		if n == nil {
			return
		}
		if n.isLeaf {
			out = append(out, n.leaf)
			return
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(t.root)
	return out
}

// Len is the number of panes.
func (t *Tree[T]) Len() int { return len(t.Leaves()) }

// Split adds leaf right after (or below) at. When at's parent already
// splits in direction d, leaf becomes its sibling and the parent's panes
// share the room equally; otherwise at's place becomes a new split. It
// returns false when at is missing or leaf is already in the tree.
func (t *Tree[T]) Split(at, leaf T, d Dir) bool {
	n := t.find(at)
	if n == nil || t.find(leaf) != nil {
		return false
	}
	nl := &node[T]{leaf: leaf, isLeaf: true}
	if p := n.parent; p != nil && p.dir == d {
		i := indexOf(p.kids, n)
		nl.parent = p
		p.kids = append(p.kids[:i+1], append([]*node[T]{nl}, p.kids[i+1:]...)...)
		return true
	}
	s := &node[T]{dir: d, parent: n.parent}
	t.replace(n, s)
	n.parent, nl.parent = s, s
	s.kids = []*node[T]{n, nl}
	return true
}

// Remove takes leaf out of the tree; a split left with one pane gives its
// place to that pane. It returns false when leaf is missing.
func (t *Tree[T]) Remove(leaf T) bool {
	n := t.find(leaf)
	if n == nil {
		return false
	}
	p := n.parent
	if p == nil {
		t.root = nil
		return true
	}
	p.kids = append(p.kids[:indexOf(p.kids, n)], p.kids[indexOf(p.kids, n)+1:]...)
	if len(p.kids) == 1 {
		c := p.kids[0]
		gp := p.parent
		c.parent = gp
		if gp != nil && !c.isLeaf && c.dir == gp.dir {
			// Same direction as the grandparent: its panes join it.
			i := indexOf(gp.kids, p)
			for _, k := range c.kids {
				k.parent = gp
			}
			gp.kids = append(gp.kids[:i], append(c.kids, gp.kids[i+1:]...)...)
		} else {
			t.replace(p, c)
		}
	}
	return true
}

// replace puts n2 where n is.
func (t *Tree[T]) replace(n, n2 *node[T]) {
	p := n.parent
	if p == nil {
		t.root = n2
		return
	}
	p.kids[indexOf(p.kids, n)] = n2
	n2.parent = p
}

func indexOf[T comparable](kids []*node[T], n *node[T]) int {
	for i, k := range kids {
		if k == n {
			return i
		}
	}
	return -1
}

// Layout places the panes in area. Panes of a split share its room
// equally, the first ones taking the remainder; gap cells separate them
// and are returned as dividers.
func (t *Tree[T]) Layout(area Rect, gap int) (panes []Placed[T], dividers []Rect) {
	var walk func(n *node[T], r Rect)
	walk = func(n *node[T], r Rect) {
		if n.isLeaf {
			panes = append(panes, Placed[T]{Value: n.leaf, Rect: r})
			return
		}
		count := len(n.kids)
		length := r.W
		if n.dir == Vertical {
			length = r.H
		}
		avail := max(length-gap*(count-1), 0)
		base, rem := avail/count, avail%count
		pos := 0
		for i, k := range n.kids {
			size := base
			if i < rem {
				size++
			}
			kr, dr := r, r
			if n.dir == Horizontal {
				kr.X, kr.W = r.X+pos, size
				dr.X, dr.W = r.X+pos+size, gap
			} else {
				kr.Y, kr.H = r.Y+pos, size
				dr.Y, dr.H = r.Y+pos+size, gap
			}
			walk(k, kr)
			if i < count-1 && gap > 0 {
				dividers = append(dividers, dr)
			}
			pos += size + gap
		}
	}
	if t.root != nil {
		walk(t.root, area)
	}
	return panes, dividers
}

// CanSplit reports whether a pane drawn in r can be split in direction d
// with gap divider cells and leave two panes of at least MinCols x MinRows.
func CanSplit(r Rect, d Dir, gap int) bool {
	if d == Horizontal {
		return (r.W-gap)/2 >= MinCols && r.H >= MinRows
	}
	return (r.H-gap)/2 >= MinRows && r.W >= MinCols
}

// Neighbor returns the pane next to from in direction dir: one that lies
// wholly on that side and overlaps from on the other axis. The nearest
// wins, then the one sharing the longest edge, then the top-most or
// left-most.
func Neighbor[T comparable](panes []Placed[T], from T, dir Direction) (T, bool) {
	var zero T
	var fr Rect
	found := false
	for _, p := range panes {
		if p.Value == from {
			fr, found = p.Rect, true
		}
	}
	if !found {
		return zero, false
	}
	best, bestDist, bestOverlap, have := Placed[T]{}, 0, 0, false
	for _, p := range panes {
		if p.Value == from {
			continue
		}
		c := p.Rect
		var dist, ov int
		switch dir {
		case Left:
			dist, ov = fr.X-(c.X+c.W), overlap(c.Y, c.H, fr.Y, fr.H)
		case Right:
			dist, ov = c.X-(fr.X+fr.W), overlap(c.Y, c.H, fr.Y, fr.H)
		case Up:
			dist, ov = fr.Y-(c.Y+c.H), overlap(c.X, c.W, fr.X, fr.W)
		case Down:
			dist, ov = c.Y-(fr.Y+fr.H), overlap(c.X, c.W, fr.X, fr.W)
		}
		if dist < 0 || ov <= 0 {
			continue
		}
		better := !have || dist < bestDist ||
			(dist == bestDist && ov > bestOverlap) ||
			(dist == bestDist && ov == bestOverlap && (c.Y < best.Rect.Y || (c.Y == best.Rect.Y && c.X < best.Rect.X)))
		if better {
			best, bestDist, bestOverlap, have = p, dist, ov, true
		}
	}
	return best.Value, have
}

func overlap(a, alen, b, blen int) int {
	return min(a+alen, b+blen) - max(a, b)
}
