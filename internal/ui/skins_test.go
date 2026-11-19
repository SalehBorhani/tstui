package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestParseSkin(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		check   func(t *testing.T, th Theme)
		wantErr string
	}{
		{
			name: "empty file is the default skin",
			in:   "",
			check: func(t *testing.T, th Theme) {
				if th != DefaultTheme() {
					t.Error("theme differs from DefaultTheme")
				}
			},
		},
		{
			name: "overrides on a base",
			in:   "base: light\ncolors:\n  border: \"#268BD2\"\n  error: Crimson\n  bg: default\n",
			check: func(t *testing.T, th Theme) {
				want := LightTheme()
				want.Border = tcell.NewHexColor(0x268bd2)
				want.Err = tcell.ColorCrimson
				want.Bg = tcell.ColorDefault
				if th != want {
					t.Errorf("got %+v, want %+v", th, want)
				}
			},
		},
		{name: "unknown base", in: "base: neon\n", wantErr: `base "neon" is not a built-in skin`},
		{name: "unknown key", in: "colors:\n  bordr: red\n", wantErr: "colors.bordr: unknown key"},
		{name: "unknown colour", in: "colors:\n  border: reddish\n", wantErr: `unknown colour "reddish"`},
		{name: "short hex", in: "colors:\n  border: \"#fff\"\n", wantErr: "not a #rrggbb colour"},
		{name: "unknown top-level key", in: "colours:\n  border: red\n", wantErr: "field colours not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th, err := ParseSkin(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, th)
		})
	}
}

func TestLoadSkins(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ocean.yaml", "colors:\n  border: navy\n")
	write("default.yaml", "colors:\n  title: red\n") // replaces the built-in
	write("broken.yaml", "colors:\n  border: nope\n")
	write("notes.txt", "ignored")
	write("-bad.yaml", "")

	s, warnings := LoadSkins(dir)
	if got, want := strings.Join(s.Names(), ","), "default,high-contrast,light,ocean"; got != want {
		t.Errorf("names = %s, want %s", got, want)
	}
	if th, ok := s.Theme("ocean"); !ok || th.Border != tcell.ColorNavy {
		t.Errorf("ocean: ok=%v border=%v", ok, th.Border)
	}
	if th, _ := s.Theme("default"); th.Title != tcell.ColorRed {
		t.Errorf("default.yaml did not replace the built-in: title %v", th.Title)
	}
	if len(warnings) != 2 || !strings.Contains(strings.Join(warnings, "\n"), "broken.yaml") {
		t.Errorf("warnings = %q, want broken.yaml and -bad.yaml", warnings)
	}

	if s, w := LoadSkins(filepath.Join(dir, "missing")); len(w) != 0 || len(s.Names()) != 3 {
		t.Errorf("missing dir: names %v, warnings %v", s.Names(), w)
	}
}

// The example skins shipped in examples/skins must load.
func TestExampleSkins(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "skins")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no example skins")
	}
	s, warnings := LoadSkins(dir)
	if len(warnings) > 0 {
		t.Fatalf("warnings: %q", warnings)
	}
	for _, e := range entries {
		if _, ok := s.Theme(strings.TrimSuffix(e.Name(), ".yaml")); !ok {
			t.Errorf("%s did not load", e.Name())
		}
	}
}
