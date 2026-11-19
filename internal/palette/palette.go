// Package palette parses and completes the k9s-style command line that
// `:` opens, and keeps its history. It knows nothing about the UI: the UI
// runs the Command that Parse returns.
package palette

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ArgKind says what a command's argument is, for completion and checks.
type ArgKind int

// Argument kinds.
const (
	// ArgNone: the command takes no argument.
	ArgNone ArgKind = iota
	// ArgProxy: a proxy name or address.
	ArgProxy
	// ArgHost: a server hostname.
	ArgHost
	// ArgCluster: a Teleport cluster name, or "all".
	ArgCluster
	// ArgSkin: a skin name.
	ArgSkin
	// ArgChoice: one of Spec.Choices.
	ArgChoice
	// ArgText: free text, kept as typed (a filter, a remote command).
	ArgText
)

// Spec describes one command.
type Spec struct {
	Name    string
	Aliases []string
	Kind    ArgKind
	// Required means the argument must be given.
	Required bool
	Choices  []string
	// Usage is the argument part of the synopsis, e.g. "<host>".
	Usage string
	Help  string
}

// Commands is every palette command, in help order.
var Commands = []Spec{
	{Name: "proxy", Aliases: []string{"ctx", "p"}, Kind: ArgProxy, Usage: "[name]", Help: "switch proxy; without a name, pick from a list"},
	{Name: "cluster", Aliases: []string{"cl"}, Kind: ArgCluster, Usage: "[name|all]", Help: "show one cluster's servers, or all; without a name, pick from a list"},
	{Name: "login", Help: "log in to the current proxy"},
	{Name: "logout", Help: "log out of the current proxy"},
	{Name: "ssh", Kind: ArgHost, Required: true, Usage: "<host>", Help: "open a session to a server"},
	{Name: "sessions", Aliases: []string{"tabs"}, Help: "list open sessions"},
	{Name: "split", Kind: ArgHost, Usage: "[host]", Help: "open a server in a pane below this one (picker without a host)"},
	{Name: "vsplit", Aliases: []string{"vs"}, Kind: ArgHost, Usage: "[host]", Help: "open a server in a pane right of this one"},
	{Name: "fullscreen", Aliases: []string{"fs"}, Help: "show nothing but the terminal (again: everything)"},
	{Name: "zoom", Help: "show only the focused pane (again: all panes)"},
	{Name: "filter", Aliases: []string{"f"}, Kind: ArgText, Usage: "[expr]", Help: "filter servers: text, key=value, key!=value; empty clears"},
	{Name: "sort", Kind: ArgChoice, Required: true, Choices: []string{"name", "address", "cluster", "off"}, Usage: "<name|address|cluster|off>", Help: "sort servers (again: reverse); favorites stay first"},
	{Name: "fav", Kind: ArgHost, Usage: "[host]", Help: "toggle a favorite (default: the selected server)"},
	{Name: "refresh", Aliases: []string{"r"}, Help: "reload servers, login state and health"},
	{Name: "skin", Kind: ArgSkin, Required: true, Usage: "<name>", Help: "change the colour skin"},
	{Name: "scp", Aliases: []string{"cp"}, Kind: ArgChoice, Choices: []string{"up", "down"}, Usage: "[up|down]", Help: "copy files to or from the selected server"},
	{Name: "pf", Aliases: []string{"forward"}, Help: "port forward manager"},
	{Name: "exec", Aliases: []string{"x"}, Kind: ArgText, Usage: "[command]", Help: "run a command on the marked servers"},
	{Name: "help", Aliases: []string{"?", "h"}, Help: "show keys and commands"},
	{Name: "quit", Aliases: []string{"q", "q!"}, Help: "quit tstui"},
}

// Command is a parsed command line.
type Command struct {
	// Name is the canonical command name.
	Name string
	// Arg is the argument with surrounding spaces removed; for ArgText it
	// is otherwise exactly as typed.
	Arg string
}

// Errors from Parse.
var (
	ErrEmpty   = errors.New("empty command")
	ErrUnknown = errors.New("unknown command")
)

// Lookup finds a command by name, alias or unique prefix of a name.
func Lookup(name string) (Spec, bool) {
	name = strings.ToLower(name)
	for _, s := range Commands {
		if s.Name == name {
			return s, true
		}
		for _, a := range s.Aliases {
			if a == name {
				return s, true
			}
		}
	}
	var found []Spec
	for _, s := range Commands {
		if strings.HasPrefix(s.Name, name) {
			found = append(found, s)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return Spec{}, false
}

// Parse reads a command line, with or without the leading ':'.
func Parse(line string) (Command, error) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ":"))
	if line == "" {
		return Command{}, ErrEmpty
	}
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	spec, ok := Lookup(name)
	if !ok {
		if c := Suggest(name); c != "" {
			return Command{}, fmt.Errorf("%w %q (did you mean %s?)", ErrUnknown, name, c)
		}
		return Command{}, fmt.Errorf("%w %q (try :help)", ErrUnknown, name)
	}
	cmd := Command{Name: spec.Name, Arg: arg}
	switch {
	case spec.Kind == ArgNone && arg != "":
		return Command{}, fmt.Errorf(":%s takes no argument", spec.Name)
	case spec.Required && arg == "":
		return Command{}, fmt.Errorf("usage: :%s %s", spec.Name, spec.Usage)
	case spec.Kind == ArgChoice && arg != "" && !contains(spec.Choices, strings.ToLower(arg)):
		return Command{}, fmt.Errorf("usage: :%s %s", spec.Name, spec.Usage)
	case spec.Kind == ArgChoice:
		cmd.Arg = strings.ToLower(arg)
	case spec.Kind != ArgText && strings.ContainsAny(arg, " \t"):
		return Command{}, fmt.Errorf(":%s takes one argument: %s", spec.Name, spec.Usage)
	}
	return cmd, nil
}

// Suggest returns the command whose name is closest to name, if close.
func Suggest(name string) string {
	best, bestD := "", 3
	for _, s := range Commands {
		if d := distance(strings.ToLower(name), s.Name); d < bestD {
			best, bestD = s.Name, d
		}
	}
	return best
}

// distance is the Levenshtein distance.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Sources are the values completion offers for each argument kind.
type Sources struct {
	Proxies  []string
	Clusters []string
	Hosts    []string
	Skins    []string
}

// Complete returns the complete command lines that line could become,
// sorted. A command name is completed with a trailing space when it takes
// an argument. Free text (ArgText) is not completed.
func Complete(line string, src Sources) []string {
	line = strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(line, " "), ":"), " ")
	name, arg, hasArg := strings.Cut(line, " ")
	if !hasArg {
		var out []string
		seen := map[string]bool{}
		for _, s := range Commands {
			if strings.HasPrefix(s.Name, strings.ToLower(name)) && !seen[s.Name] {
				seen[s.Name] = true
				c := s.Name
				if s.Kind != ArgNone {
					c += " "
				}
				out = append(out, c)
			}
		}
		sort.Strings(out)
		return out
	}
	spec, ok := Lookup(name)
	if !ok {
		return nil
	}
	arg = strings.TrimLeft(arg, " ")
	var values []string
	switch spec.Kind {
	case ArgProxy:
		values = src.Proxies
	case ArgCluster:
		values = src.Clusters
	case ArgHost:
		values = src.Hosts
	case ArgSkin:
		values = src.Skins
	case ArgChoice:
		values = spec.Choices
	default:
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		if strings.HasPrefix(strings.ToLower(v), strings.ToLower(arg)) && !seen[v] {
			seen[v] = true
			out = append(out, spec.Name+" "+v)
		}
	}
	sort.Strings(out)
	return out
}

// CommonPrefix is the longest prefix shared by every candidate.
func CommonPrefix(cands []string) string {
	if len(cands) == 0 {
		return ""
	}
	p := cands[0]
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
