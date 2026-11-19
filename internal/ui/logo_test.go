package ui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestHalfBlocks(t *testing.T) {
	got := halfBlocks([]string{"#  #", "# # ", "#"})
	want := []string{"█ ▄▀", "▀   "} // both, none, bottom, top; then a lone top row
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("halfBlocks = %q, want %q", got, want)
	}
}

func TestLogoShapes(t *testing.T) {
	for _, tt := range []struct {
		size        logoSize
		rows, width int
		first       string
	}{
		{logoFull, 4, 33, "██████ ▄█████ ██████ ██  ██ ██   "},
		{logoCompact, 3, 19, "▀█▀ █▀▀ ▀█▀ █ █ █  "},
	} {
		lines := logoLines(tt.size)
		if len(lines) != tt.rows || logoWidth(tt.size) != tt.width || lines[0] != tt.first {
			t.Errorf("size %d: %d rows, width %d, first %q", tt.size, len(lines), logoWidth(tt.size), lines[0])
		}
		// The logo and its tagline fit in the header.
		if len(lines)+1 > headerHeight {
			t.Errorf("size %d is %d rows with the tagline; the header has %d", tt.size, len(lines)+1, headerHeight)
		}
	}
	if logoLines(logoNone) != nil || logoText(logoNone, DefaultTheme()) != "" {
		t.Error("no logo should be empty")
	}
}

func TestLogoSizeFor(t *testing.T) {
	for w, want := range map[int]logoSize{80: logoNone, 119: logoNone, 120: logoCompact, 159: logoCompact, 160: logoFull, 300: logoFull} {
		if got := logoSizeFor(w); got != want {
			t.Errorf("logoSizeFor(%d) = %d, want %d", w, got, want)
		}
	}
}

func TestLogoGradient(t *testing.T) {
	from, to := tcell.NewHexColor(0x000000), tcell.NewHexColor(0xff0000)
	if gradient(from, to, 0, 11) != from || gradient(from, to, 10, 11) != to {
		t.Error("gradient ends")
	}
	if r, _, _ := gradient(from, to, 5, 11).RGB(); r < 120 || r > 135 {
		t.Errorf("middle red = %d", r)
	}
	if gradient(tcell.ColorDefault, to, 3, 11) != tcell.ColorDefault {
		t.Error("a colour without RGB is kept")
	}
	// The plain text of the tagged logo is the logo plus the tagline.
	plain := regexp.MustCompile(`\[[^\]]*\]`).ReplaceAllString(logoText(logoFull, DefaultTheme()), "")
	if !strings.HasPrefix(plain, logoLines(logoFull)[0]) || !strings.Contains(plain, logoTagline) {
		t.Fatalf("logo text:\n%s", plain)
	}
}

func TestHeaderLogoFollowsWidth(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	h := newAppHarness(t, fake, Options{})
	h.eventually("server list", func() bool { return h.app.serversLoaded })
	for _, tt := range []struct {
		width     int
		want, not string
	}{
		{180, "██████ ▄█████ ██████", "▀█▀ █▀▀ ▀█▀"},
		{140, "▀█▀ █▀▀ ▀█▀", "▄█████"},
		{100, "Clusters", "▀█▀"},
	} {
		h.screen.SetSize(tt.width, 40)
		h.app.tv.QueueUpdateDraw(func() {})
		h.eventually(fmt.Sprintf("logo at width %d", tt.width), func() bool {
			s := h.textUnlocked()
			return strings.Contains(s, tt.want) && !strings.Contains(s, tt.not) && (tt.width == 100 || strings.Contains(s, "tstui"))
		})
	}
}
