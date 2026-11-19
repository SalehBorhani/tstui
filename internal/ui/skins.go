package ui

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"go.yaml.in/yaml/v3"
)

// Skins is the set of skins tstui can use: the built-in ones plus the skin
// files loaded from a directory. A file named like a built-in skin
// replaces it.
type Skins struct {
	themes map[string]Theme
}

// BuiltinSkins returns the built-in skins only.
func BuiltinSkins() *Skins {
	s := &Skins{themes: map[string]Theme{}}
	for name, f := range skins {
		s.themes[name] = f()
	}
	return s
}

// skinNameRE is the config's rule for skin names (config.nameRE).
var skinNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// maxSkinFile bounds what is read from one skin file.
const maxSkinFile = 64 << 10

// LoadSkins returns the built-in skins plus every <name>.yaml in dir. A
// missing dir is not an error. A file that does not parse is skipped, and
// the returned warnings say why; they never stop tstui.
func LoadSkins(dir string) (*Skins, []string) {
	s := BuiltinSkins()
	if dir == "" {
		return s, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, []string{fmt.Sprintf("skins: %v", err)}
	}
	var warnings []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok || e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !skinNameRE.MatchString(name) {
			warnings = append(warnings, fmt.Sprintf("skin %s: the file name may only contain letters, digits, '.', '_' and '-'", path))
			continue
		}
		t, err := loadSkinFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skin %s: %v", path, err))
			continue
		}
		s.themes[name] = t
	}
	return s, warnings
}

func loadSkinFile(path string) (Theme, error) {
	f, err := os.Open(path)
	if err != nil {
		return Theme{}, err
	}
	defer f.Close()
	return ParseSkin(io.LimitReader(f, maxSkinFile))
}

// Theme returns the skin called name.
func (s *Skins) Theme(name string) (Theme, bool) {
	t, ok := s.themes[name]
	if !ok {
		return DefaultTheme(), false
	}
	return t, true
}

// Names lists the skins, sorted.
func (s *Skins) Names() []string {
	names := make([]string, 0, len(s.themes))
	for n := range s.themes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// skinFile is the YAML form of a skin.
type skinFile struct {
	// Base is the built-in skin the colours are applied to; default
	// "default".
	Base   string            `yaml:"base"`
	Colors map[string]string `yaml:"colors"`
}

// skinKeys maps a skin file's colour keys to the Theme fields.
var skinKeys = map[string]func(*Theme) *tcell.Color{
	"bg":                 func(t *Theme) *tcell.Color { return &t.Bg },
	"fg":                 func(t *Theme) *tcell.Color { return &t.Fg },
	"border":             func(t *Theme) *tcell.Color { return &t.Border },
	"border_focus":       func(t *Theme) *tcell.Color { return &t.BorderFocus },
	"title":              func(t *Theme) *tcell.Color { return &t.Title },
	"counter":            func(t *Theme) *tcell.Color { return &t.Counter },
	"info_label":         func(t *Theme) *tcell.Color { return &t.InfoLabel },
	"info_value":         func(t *Theme) *tcell.Color { return &t.InfoValue },
	"menu_key":           func(t *Theme) *tcell.Color { return &t.MenuKey },
	"menu_text":          func(t *Theme) *tcell.Color { return &t.MenuText },
	"table_fg":           func(t *Theme) *tcell.Color { return &t.TableFg },
	"table_header":       func(t *Theme) *tcell.Color { return &t.TableHeader },
	"table_cursor_fg":    func(t *Theme) *tcell.Color { return &t.TableCursorFg },
	"table_cursor_bg":    func(t *Theme) *tcell.Color { return &t.TableCursorBg },
	"dialog_fg":          func(t *Theme) *tcell.Color { return &t.DialogFg },
	"dialog_label":       func(t *Theme) *tcell.Color { return &t.DialogLabel },
	"dialog_field":       func(t *Theme) *tcell.Color { return &t.DialogField },
	"dialog_placeholder": func(t *Theme) *tcell.Color { return &t.DialogPlaceholder },
	"button_fg":          func(t *Theme) *tcell.Color { return &t.ButtonFg },
	"button_bg":          func(t *Theme) *tcell.Color { return &t.ButtonBg },
	"button_focus_fg":    func(t *Theme) *tcell.Color { return &t.ButtonFocusFg },
	"button_focus_bg":    func(t *Theme) *tcell.Color { return &t.ButtonFocusBg },
	"ok":                 func(t *Theme) *tcell.Color { return &t.OK },
	"warn":               func(t *Theme) *tcell.Color { return &t.Warn },
	"error":              func(t *Theme) *tcell.Color { return &t.Err },
	"info":               func(t *Theme) *tcell.Color { return &t.Info },
	"logo_from":          func(t *Theme) *tcell.Color { return &t.LogoFrom },
	"logo_to":            func(t *Theme) *tcell.Color { return &t.LogoTo },
}

// SkinColorKeys lists the colour keys a skin file accepts, sorted.
func SkinColorKeys() []string {
	keys := make([]string, 0, len(skinKeys))
	for k := range skinKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseSkin reads a skin file: a built-in base skin plus colour overrides.
// Colours are W3C names ("dodgerblue"), "#rrggbb", or "default" for the
// terminal's own colour. Unknown keys and colours are errors.
func ParseSkin(r io.Reader) (Theme, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var sf skinFile
	if err := dec.Decode(&sf); err != nil && !errors.Is(err, io.EOF) {
		return Theme{}, fmt.Errorf("parse: %w", err)
	}
	base := sf.Base
	if base == "" {
		base = "default"
	}
	t, ok := ThemeByName(base)
	if !ok {
		return Theme{}, fmt.Errorf("base %q is not a built-in skin (%s)", base, strings.Join(SkinNames(), ", "))
	}
	var errs []error
	for key, value := range sf.Colors {
		field, ok := skinKeys[key]
		if !ok {
			errs = append(errs, fmt.Errorf("colors.%s: unknown key", key))
			continue
		}
		c, err := parseColor(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("colors.%s: %w", key, err))
			continue
		}
		*field(&t) = c
	}
	if err := errors.Join(errs...); err != nil {
		return Theme{}, err
	}
	return t, nil
}

var hexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func parseColor(s string) (tcell.Color, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "default" {
		return tcell.ColorDefault, nil
	}
	if strings.HasPrefix(s, "#") {
		if !hexColorRE.MatchString(s) {
			return tcell.ColorDefault, fmt.Errorf("%q is not a #rrggbb colour", s)
		}
		return tcell.GetColor(s), nil
	}
	if c, ok := tcell.ColorNames[s]; ok {
		return c, nil
	}
	return tcell.ColorDefault, fmt.Errorf("unknown colour %q (use a W3C colour name, #rrggbb or default)", s)
}
