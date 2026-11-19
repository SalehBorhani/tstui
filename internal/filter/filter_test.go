package filter

import (
	"strings"
	"testing"

	"github.com/root-ali/tstui/internal/tsh"
)

var servers = []tsh.Server{
	{Hostname: "web-01", Addr: "10.0.1.11:3022", Labels: map[string]string{"env": "prod", "role": "web"}},
	{Hostname: "web-02", Labels: map[string]string{"env": "stage", "role": "web"}},
	{Hostname: "db-03", Addr: "10.0.1.30:3022", Labels: map[string]string{"env": "prod"}},
}

func names(ss []tsh.Server) string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Hostname)
	}
	return strings.Join(out, ",")
}

func TestFilter(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"", "web-01,web-02,db-03"},
		{"web", "web-01,web-02"},
		{"WEB", "web-01,web-02"},
		{"!web", "db-03"},
		{"env=prod", "web-01,db-03"},
		{"env=PROD", "web-01,db-03"},
		{"env!=prod", "web-02"},
		{"role=", "web-01,web-02"},
		{"env=prod,role=web", "web-01"},
		{"env=prod web", "web-01"},
		{"10.0.1.3", "db-03"},
		{"role!=web", "db-03"}, // missing label counts as "not web"
		{"nothing", ""},
		{" , ", "web-01,web-02,db-03"},
	}
	for _, tt := range tests {
		f, err := Parse(tt.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.expr, err)
		}
		if got := names(f.Apply(servers)); got != tt.want {
			t.Errorf("filter %q = %q, want %q", tt.expr, got, tt.want)
		}
	}
	for _, bad := range []string{"=prod", "!=prod"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", bad)
		}
	}
	if f, _ := Parse("  env=prod "); f.String() != "env=prod" || f.Empty() {
		t.Errorf("String/Empty wrong: %q %v", f.String(), f.Empty())
	}
}
