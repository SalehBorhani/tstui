package layout

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// shape prints the tree: leaves as their value, splits as h(...) or v(...).
func shape(t *Tree[string]) string {
	var walk func(n *node[string]) string
	walk = func(n *node[string]) string {
		if n.isLeaf {
			return n.leaf
		}
		parts := make([]string, len(n.kids))
		for i, k := range n.kids {
			if k.parent != n {
				panic("bad parent link under " + fmt.Sprint(n.dir))
			}
			parts[i] = walk(k)
		}
		d := "h"
		if n.dir == Vertical {
			d = "v"
		}
		return d + "(" + strings.Join(parts, " ") + ")"
	}
	if t.root == nil {
		return ""
	}
	return walk(t.root)
}

func TestTreeSplitRemove(t *testing.T) {
	tr := New("a")
	steps := []struct {
		name string
		do   func() bool
		ok   bool
		want string
	}{
		{"split a right", func() bool { return tr.Split("a", "b", Horizontal) }, true, "h(a b)"},
		{"same direction adds a sibling", func() bool { return tr.Split("a", "c", Horizontal) }, true, "h(a c b)"},
		{"split c down nests", func() bool { return tr.Split("c", "d", Vertical) }, true, "h(a v(c d) b)"},
		{"missing at", func() bool { return tr.Split("zz", "e", Vertical) }, false, "h(a v(c d) b)"},
		{"duplicate leaf", func() bool { return tr.Split("a", "b", Vertical) }, false, "h(a v(c d) b)"},
		{"remove d collapses", func() bool { return tr.Remove("d") }, true, "h(a c b)"},
		{"remove missing", func() bool { return tr.Remove("d") }, false, "h(a c b)"},
		{"split b down", func() bool { return tr.Split("b", "e", Vertical) }, true, "h(a c v(b e))"},
		{"split e right nests again", func() bool { return tr.Split("e", "f", Horizontal) }, true, "h(a c v(b h(e f)))"},
		{"remove b merges h into h", func() bool { return tr.Remove("b") }, true, "h(a c e f)"},
		{"remove a", func() bool { return tr.Remove("a") }, true, "h(c e f)"},
		{"remove c", func() bool { return tr.Remove("c") }, true, "h(e f)"},
		{"remove e", func() bool { return tr.Remove("e") }, true, "f"},
		{"remove last", func() bool { return tr.Remove("f") }, true, ""},
	}
	for _, st := range steps {
		if ok := st.do(); ok != st.ok {
			t.Fatalf("%s: ok = %v, want %v", st.name, ok, st.ok)
		}
		if got := shape(tr); got != st.want {
			t.Fatalf("%s: shape = %q, want %q", st.name, got, st.want)
		}
	}
	if tr.Len() != 0 || tr.Contains("f") {
		t.Fatal("tree not empty")
	}
}

func TestTreeLeavesOrder(t *testing.T) {
	tr := New("a")
	tr.Split("a", "b", Horizontal)
	tr.Split("a", "c", Vertical)
	tr.Split("b", "d", Vertical)
	if got, want := tr.Leaves(), []string{"a", "c", "b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("leaves = %v, want %v", got, want)
	}
}

func rects(ps []Placed[string]) map[string]Rect {
	m := map[string]Rect{}
	for _, p := range ps {
		m[p.Value] = p.Rect
	}
	return m
}

func TestLayout(t *testing.T) {
	area := Rect{X: 0, Y: 0, W: 101, H: 40}
	tests := []struct {
		name  string
		build func() *Tree[string]
		gap   int
		want  map[string]Rect
		divs  []Rect
	}{
		{"single", func() *Tree[string] { return New("a") }, 1,
			map[string]Rect{"a": area}, nil},
		{"side by side with gap", func() *Tree[string] {
			tr := New("a")
			tr.Split("a", "b", Horizontal)
			return tr
		}, 1, map[string]Rect{"a": {0, 0, 50, 40}, "b": {51, 0, 50, 40}}, []Rect{{50, 0, 1, 40}}},
		{"three, no gap, remainder first", func() *Tree[string] {
			tr := New("a")
			tr.Split("a", "b", Horizontal)
			tr.Split("b", "c", Horizontal)
			return tr
		}, 0, map[string]Rect{"a": {0, 0, 34, 40}, "b": {34, 0, 34, 40}, "c": {68, 0, 33, 40}}, nil},
		{"nested", func() *Tree[string] {
			tr := New("a")
			tr.Split("a", "b", Horizontal)
			tr.Split("b", "c", Vertical)
			return tr
		}, 1, map[string]Rect{"a": {0, 0, 50, 40}, "b": {51, 0, 50, 20}, "c": {51, 20 + 1, 50, 19}},
			[]Rect{{50, 0, 1, 40}, {51, 20, 50, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, divs := tt.build().Layout(area, tt.gap)
			got := rects(ps)
			for k, w := range tt.want {
				if got[k] != w {
					t.Errorf("%s = %+v, want %+v", k, got[k], w)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("got %d panes, want %d", len(got), len(tt.want))
			}
			if !slices.Equal(divs, tt.divs) {
				t.Errorf("dividers = %+v, want %+v", divs, tt.divs)
			}
		})
	}
}

func TestNeighbor(t *testing.T) {
	// a | b
	//   | c
	// with d under everything.
	tr := New("a")
	tr.Split("a", "d", Vertical)
	tr.Split("a", "b", Horizontal)
	tr.Split("b", "c", Vertical)
	ps, _ := tr.Layout(Rect{W: 100, H: 41}, 1)
	tests := []struct {
		from string
		dir  Direction
		want string
		ok   bool
	}{
		{"a", Right, "b", true}, // b and c tie on distance; b shares the longer edge
		{"c", Left, "a", true},
		{"b", Down, "c", true},
		{"c", Down, "d", true},
		{"a", Down, "d", true},
		{"d", Up, "a", true}, // a and c both touch d; a shares the longer edge
		{"a", Left, "", false},
		{"b", Up, "", false},
		{"zz", Up, "", false},
	}
	for _, tt := range tests {
		got, ok := Neighbor(ps, tt.from, tt.dir)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Neighbor(%s, %d) = %q, %v; want %q, %v", tt.from, tt.dir, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCanSplit(t *testing.T) {
	tests := []struct {
		r    Rect
		d    Dir
		gap  int
		want bool
	}{
		{Rect{W: 41, H: 10}, Horizontal, 1, true},
		{Rect{W: 40, H: 10}, Horizontal, 1, false},
		{Rect{W: 40, H: 10}, Horizontal, 0, true},
		{Rect{W: 40, H: 11}, Vertical, 1, true},
		{Rect{W: 40, H: 10}, Vertical, 1, false},
		{Rect{W: 19, H: 40}, Vertical, 0, false},
	}
	for _, tt := range tests {
		if got := CanSplit(tt.r, tt.d, tt.gap); got != tt.want {
			t.Errorf("CanSplit(%+v, %d, %d) = %v, want %v", tt.r, tt.d, tt.gap, got, tt.want)
		}
	}
}
