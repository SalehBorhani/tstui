package palette

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		line string
		want Command
		err  string // substring; "" means no error
	}{
		{":proxy a-teleport", Command{"proxy", "a-teleport"}, ""},
		{"proxy", Command{"proxy", ""}, ""},
		{"  :ctx   b  ", Command{"proxy", "b"}, ""},
		{":login", Command{"login", ""}, ""},
		{":login now", Command{}, "takes no argument"},
		{":ssh web-01", Command{"ssh", "web-01"}, ""},
		{":ssh", Command{}, "usage: :ssh <host>"},
		{":ssh a b", Command{}, "one argument"},
		{":filter env=prod, role!=db", Command{"filter", "env=prod, role!=db"}, ""},
		{":filter", Command{"filter", ""}, ""},
		{":exec uptime; df -h | grep '/ '", Command{"exec", "uptime; df -h | grep '/ '"}, ""},
		{":x ls", Command{"exec", "ls"}, ""},
		{":scp UP", Command{"scp", "up"}, ""},
		{":scp sideways", Command{}, "usage"},
		{":sort Name", Command{"sort", "name"}, ""},
		{":so cluster", Command{"sort", "cluster"}, ""},
		{":sort", Command{}, "usage: :sort <name|address|cluster|off>"},
		{":sort age", Command{}, "usage"},
		{":ref", Command{"refresh", ""}, ""}, // unique prefix
		{":q", Command{"quit", ""}, ""},
		{":q!", Command{"quit", ""}, ""},
		{":?", Command{"help", ""}, ""},
		{":skin", Command{}, "usage"},
		{":skin light", Command{"skin", "light"}, ""},
		{":lo", Command{}, "unknown"}, // login or logout: ambiguous
		{":sssh x", Command{}, "did you mean ssh"},
		{":zzzzzz", Command{}, "try :help"},
		{":", Command{}, "empty"},
		{"", Command{}, "empty"},
	}
	for _, tt := range tests {
		got, err := Parse(tt.line)
		if tt.err == "" {
			if err != nil || got != tt.want {
				t.Errorf("Parse(%q) = %+v, %v; want %+v", tt.line, got, err, tt.want)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("Parse(%q) error = %v; want it to contain %q", tt.line, err, tt.err)
		}
	}
	if _, err := Parse(":nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown command error = %v, want ErrUnknown", err)
	}
}

func TestComplete(t *testing.T) {
	src := Sources{
		Proxies:  []string{"a-teleport", "b-teleport", "tp.afr.example:443"},
		Hosts:    []string{"web-01", "web-02", "db-03"},
		Clusters: []string{"all", "afr", "hwb"},
		Skins:    []string{"default", "light", "high-contrast"},
	}
	tests := []struct {
		line string
		want []string
	}{
		{"", nil}, // checked separately: every command
		{"s", []string{"scp ", "sessions", "skin ", "sort ", "split ", "ssh "}},
		{"sort a", []string{"sort address"}},
		{":lo", []string{"login", "logout"}},
		{"ss", []string{"ssh "}},
		{"ssh w", []string{"ssh web-01", "ssh web-02"}},
		{"ssh WEB-0", []string{"ssh web-01", "ssh web-02"}},
		{"ssh ", []string{"ssh db-03", "ssh web-01", "ssh web-02"}},
		{"ctx b", []string{"proxy b-teleport"}}, // aliases complete to the name
		{"skin h", []string{"skin high-contrast"}},
		{"scp d", []string{"scp down"}},
		{"exec ls", nil},  // free text
		{"login x", nil},  // no argument
		{"zzz x", nil},    // unknown command
		{"proxy zz", nil}, // no match
		{"fav web-02", []string{"fav web-02"}},
		{"cl a", []string{"cluster afr", "cluster all"}},
		{"c", []string{"cluster "}},
	}
	for _, tt := range tests {
		got := Complete(tt.line, src)
		if tt.line == "" {
			if len(got) != len(Commands) {
				t.Errorf("Complete(\"\") = %d candidates, want %d", len(got), len(Commands))
			}
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Complete(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"ssh web-01"}, "ssh web-01"},
		{[]string{"ssh web-01", "ssh web-02"}, "ssh web-0"},
		{[]string{"login", "logout"}, "log"},
		{[]string{"a", "b"}, ""},
	}
	for _, tt := range tests {
		if got := CommonPrefix(tt.in); got != tt.want {
			t.Errorf("CommonPrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHistory(t *testing.T) {
	h := NewHistory(3)
	if got := h.Prev("typed"); got != "typed" {
		t.Fatalf("Prev on empty history = %q", got)
	}
	for _, l := range []string{"a", "b", "b", "", "c", "d"} {
		h.Add(l)
	}
	if got := h.Items(); !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Fatalf("items = %q (bounded, no repeats, no empties)", got)
	}
	steps := []struct {
		op, want string
	}{
		{"prev", "d"}, {"prev", "c"}, {"prev", "b"}, {"prev", "b"}, // stops at the oldest
		{"next", "c"}, {"next", "d"}, {"next", "draft"}, {"next", "draft"},
	}
	for i, s := range steps {
		var got string
		if s.op == "prev" {
			got = h.Prev("draft")
		} else {
			got = h.Next()
		}
		if got != s.want {
			t.Fatalf("step %d %s = %q, want %q", i, s.op, got, s.want)
		}
	}
	h.Prev("x")
	h.Add("e")
	if got := h.Prev(""); got != "e" {
		t.Fatalf("after Add, Prev = %q, want the newest line", got)
	}
}
