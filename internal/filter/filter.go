// Package filter matches servers against a small filter language:
//
//	web          hostname (or address) contains "web", case-insensitively
//	!web         ... does not contain "web"
//	env=prod     label env is "prod"
//	env!=prod    label env is not "prod" (or is missing)
//	env=         label env is present
//
// Terms are separated by commas or spaces and must all match.
package filter

import (
	"fmt"
	"strings"

	"github.com/root-ali/tstui/internal/tsh"
)

// Filter is a parsed filter expression. The zero value matches everything.
type Filter struct {
	expr  string
	terms []term
}

type term struct {
	key, value string
	label      bool
	neg        bool
	anyValue   bool // "key=": the label only has to exist
}

// Parse reads a filter expression.
func Parse(expr string) (Filter, error) {
	f := Filter{expr: strings.TrimSpace(expr)}
	for _, tok := range strings.FieldsFunc(expr, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if k, v, ok := strings.Cut(tok, "!="); ok {
			if k == "" {
				return Filter{}, fmt.Errorf("filter %q: label name missing before !=", tok)
			}
			f.terms = append(f.terms, term{key: k, value: v, label: true, neg: true})
			continue
		}
		if k, v, ok := strings.Cut(tok, "="); ok {
			if k == "" {
				return Filter{}, fmt.Errorf("filter %q: label name missing before =", tok)
			}
			f.terms = append(f.terms, term{key: k, value: v, label: true, anyValue: v == ""})
			continue
		}
		neg := strings.HasPrefix(tok, "!")
		text := strings.ToLower(strings.TrimPrefix(tok, "!"))
		if text == "" {
			continue
		}
		f.terms = append(f.terms, term{value: text, neg: neg})
	}
	return f, nil
}

// String returns the expression as given.
func (f Filter) String() string { return f.expr }

// Empty reports whether the filter matches everything.
func (f Filter) Empty() bool { return len(f.terms) == 0 }

// Match reports whether s passes every term.
func (f Filter) Match(s tsh.Server) bool {
	for _, t := range f.terms {
		var ok bool
		if t.label {
			v, has := s.Labels[t.key]
			ok = has && (t.anyValue || strings.EqualFold(v, t.value))
		} else {
			ok = strings.Contains(strings.ToLower(s.Hostname), t.value) ||
				strings.Contains(strings.ToLower(s.Addr), t.value)
		}
		if ok == t.neg {
			return false
		}
	}
	return true
}

// Apply returns the servers that match, in order.
func (f Filter) Apply(servers []tsh.Server) []tsh.Server {
	if f.Empty() {
		return servers
	}
	out := make([]tsh.Server, 0, len(servers))
	for _, s := range servers {
		if f.Match(s) {
			out = append(out, s)
		}
	}
	return out
}
