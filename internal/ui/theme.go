package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Theme holds the colours of the UI. The default copies k9s's default
// skin (k9s v0.51 internal/config/styles.go), except that the background
// is the terminal's own so transparent terminals stay transparent.
type Theme struct {
	Bg tcell.Color // every background
	Fg tcell.Color // plain text

	Border      tcell.Color
	BorderFocus tcell.Color
	Title       tcell.Color
	Counter     tcell.Color // the [n] after a title

	InfoLabel tcell.Color // header labels ("Proxy:")
	InfoValue tcell.Color // header values

	MenuKey  tcell.Color // footer "<q>"
	MenuText tcell.Color // footer "Quit"

	TableFg       tcell.Color
	TableHeader   tcell.Color
	TableCursorFg tcell.Color
	TableCursorBg tcell.Color

	DialogFg            tcell.Color // dialog text
	DialogLabel         tcell.Color
	DialogField         tcell.Color
	DialogPlaceholder   tcell.Color
	ButtonFg            tcell.Color
	ButtonBg            tcell.Color
	ButtonFocusFg       tcell.Color
	ButtonFocusBg       tcell.Color
	OK, Warn, Err, Info tcell.Color

	// LogoFrom and LogoTo are the ends of the logo's gradient.
	LogoFrom, LogoTo tcell.Color
}

// DefaultTheme is k9s's default skin on the terminal background.
func DefaultTheme() Theme {
	return Theme{
		Bg: tcell.ColorDefault,
		Fg: tcell.ColorCadetBlue,

		Border:      tcell.ColorDodgerBlue,
		BorderFocus: tcell.ColorLightSkyBlue,
		Title:       tcell.ColorAqua,
		Counter:     tcell.ColorPapayaWhip,

		InfoLabel: tcell.ColorOrange,
		InfoValue: tcell.ColorWhite,

		MenuKey:  tcell.ColorDodgerBlue,
		MenuText: tcell.ColorWhite,

		TableFg:       tcell.ColorAqua,
		TableHeader:   tcell.ColorWhite,
		TableCursorFg: tcell.ColorBlack,
		TableCursorBg: tcell.ColorAqua,

		DialogFg:          tcell.ColorCadetBlue,
		DialogLabel:       tcell.ColorWhite,
		DialogField:       tcell.ColorWhite,
		DialogPlaceholder: tcell.ColorSlateGray,
		ButtonFg:          tcell.ColorBlack,
		ButtonBg:          tcell.ColorDarkSlateBlue,
		ButtonFocusFg:     tcell.ColorBlack,
		ButtonFocusBg:     tcell.ColorDodgerBlue,

		OK:   tcell.ColorSpringGreen,
		Warn: tcell.ColorDarkOrange,
		Err:  tcell.ColorOrangeRed,
		Info: tcell.ColorAqua,

		LogoFrom: tcell.NewHexColor(0x00e5ff), // cyan
		LogoTo:   tcell.NewHexColor(0xb36bff), // violet
	}
}

// LightTheme suits terminals with a light background.
func LightTheme() Theme {
	t := DefaultTheme()
	t.Fg = tcell.ColorDarkSlateGray
	t.Border, t.BorderFocus = tcell.ColorSteelBlue, tcell.ColorNavy
	t.Title, t.Counter = tcell.ColorNavy, tcell.ColorDarkMagenta
	t.InfoLabel, t.InfoValue = tcell.ColorDarkOrange, tcell.ColorBlack
	t.MenuKey, t.MenuText = tcell.ColorNavy, tcell.ColorBlack
	t.TableFg, t.TableHeader = tcell.ColorBlack, tcell.ColorNavy
	t.TableCursorFg, t.TableCursorBg = tcell.ColorWhite, tcell.ColorSteelBlue
	t.DialogFg, t.DialogLabel, t.DialogField = tcell.ColorBlack, tcell.ColorNavy, tcell.ColorBlack
	t.DialogPlaceholder = tcell.ColorGray
	t.ButtonFg, t.ButtonBg = tcell.ColorWhite, tcell.ColorSlateGray
	t.ButtonFocusFg, t.ButtonFocusBg = tcell.ColorWhite, tcell.ColorNavy
	t.OK, t.Warn, t.Err, t.Info = tcell.ColorGreen, tcell.ColorDarkOrange, tcell.ColorRed, tcell.ColorNavy
	t.LogoFrom, t.LogoTo = tcell.NewHexColor(0x005f87), tcell.NewHexColor(0x8700af)
	return t
}

// HighContrastTheme uses only the 16 basic colours, bold-friendly.
func HighContrastTheme() Theme {
	t := DefaultTheme()
	t.Fg = tcell.ColorWhite
	t.Border, t.BorderFocus = tcell.ColorWhite, tcell.ColorYellow
	t.Title, t.Counter = tcell.ColorYellow, tcell.ColorWhite
	t.InfoLabel, t.InfoValue = tcell.ColorYellow, tcell.ColorWhite
	t.MenuKey, t.MenuText = tcell.ColorYellow, tcell.ColorWhite
	t.TableFg, t.TableHeader = tcell.ColorWhite, tcell.ColorYellow
	t.TableCursorFg, t.TableCursorBg = tcell.ColorBlack, tcell.ColorYellow
	t.DialogFg, t.DialogLabel, t.DialogField = tcell.ColorWhite, tcell.ColorYellow, tcell.ColorWhite
	t.DialogPlaceholder = tcell.ColorSilver
	t.ButtonFg, t.ButtonBg = tcell.ColorBlack, tcell.ColorWhite
	t.ButtonFocusFg, t.ButtonFocusBg = tcell.ColorBlack, tcell.ColorYellow
	t.OK, t.Warn, t.Err, t.Info = tcell.ColorLime, tcell.ColorYellow, tcell.ColorRed, tcell.ColorAqua
	t.LogoFrom, t.LogoTo = tcell.ColorYellow, tcell.ColorYellow // no gradient
	return t
}

var skins = map[string]func() Theme{
	"default":       DefaultTheme,
	"light":         LightTheme,
	"high-contrast": HighContrastTheme,
}

// SkinNames lists the built-in skins, sorted.
func SkinNames() []string {
	return []string{"default", "high-contrast", "light"}
}

// ThemeByName returns the built-in skin called name.
func ThemeByName(name string) (Theme, bool) {
	f, ok := skins[name]
	if !ok {
		return DefaultTheme(), false
	}
	return f(), true
}

// apply sets tview's global defaults, which primitives read when they are
// created; k9s does the same. Call it before building any primitive.
func (t Theme) apply() {
	tview.Styles.PrimitiveBackgroundColor = t.Bg
	tview.Styles.ContrastBackgroundColor = t.Bg
	tview.Styles.MoreContrastBackgroundColor = t.Bg
	tview.Styles.PrimaryTextColor = t.Fg
	tview.Styles.SecondaryTextColor = t.Fg
	tview.Styles.TertiaryTextColor = t.Fg
	tview.Styles.InverseTextColor = t.Fg
	tview.Styles.ContrastSecondaryTextColor = t.Fg
	tview.Styles.BorderColor = t.Border
	tview.Styles.TitleColor = t.Title
	tview.Styles.GraphicsColor = t.Fg
	// k9s keeps single-line borders on focus; tview would switch to double.
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical
	tview.Borders.TopLeftFocus = tview.Borders.TopLeft
	tview.Borders.TopRightFocus = tview.Borders.TopRight
	tview.Borders.BottomLeftFocus = tview.Borders.BottomLeft
	tview.Borders.BottomRightFocus = tview.Borders.BottomRight
}

// tag returns c as a tview colour tag value, e.g. "#ff4500".
func tag(c tcell.Color) string {
	if c == tcell.ColorDefault {
		return "-"
	}
	return c.CSS()
}
