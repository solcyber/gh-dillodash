package scenes

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

func TestRoomFit(t *testing.T) {
	for _, c := range []struct {
		h, w int
		ok   bool
	}{
		{48, 160, true}, {40, 120, true}, {RoomMinH, RoomMinW, true},
		{RoomMinH - 1, 200, false}, {60, RoomMinW - 1, false}, {24, 80, false},
	} {
		l, ok := RoomFit(c.h, c.w)
		if ok != c.ok {
			t.Errorf("%dx%d: fit %v, want %v", c.w, c.h, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if l.W < 60 || l.H < 16 || l.X < 14 || l.X+l.W > c.w-14 || l.Y < 4 || l.Y+l.H > c.h-8 {
			t.Errorf("%dx%d: layout %+v leaves no room", c.w, c.h, l)
		}
	}
}

// roomFrame draws the room at a tier with a fake dashboard behind the
// glass: every cell of its top half a letter, its bottom half blank.
func roomFrame(t *testing.T, tier colorprofile.Profile, set glyphs.Set, h, w int, in RoomInfo) (*canvas.Canvas, RoomLayout, *canvas.Canvas) {
	t.Helper()
	wasSet, wasTier := glyphs.Active(), theme.Current()
	t.Cleanup(func() {
		glyphs.Use(wasSet)
		theme.Setup(map[theme.Tier]colorprofile.Profile{
			theme.Mono: colorprofile.Ascii, theme.Color16: colorprofile.ANSI,
			theme.Color256: colorprofile.ANSI256, theme.TrueColor: colorprofile.TrueColor,
		}[wasTier], false)
	})
	theme.Setup(tier, false)
	glyphs.Use(set)
	in.Tier = theme.Current()
	l, ok := RoomFit(h, w)
	if !ok {
		t.Fatalf("no room at %dx%d", w, h)
	}
	inner := canvas.New(l.W, l.H)
	for y := 0; y < l.H/2; y++ {
		inner.PutStr(y, 0, strings.Repeat("x", l.W), "text")
	}
	c := canvas.New(w, h)
	Room(c, l, in)
	c.Blit(inner, l.Y, l.X)
	RoomFront(c, l, in)
	return c, l, inner
}

// TestRoomKeepsTheDashboard: what is in front of the glass (mullions
// aside) never covers a letter of the dashboard behind it.
func TestRoomKeepsTheDashboard(t *testing.T) {
	in := RoomInfo{Clock: time.Date(2031, 4, 16, 22, 10, 30, 0, time.UTC), Moving: true, T: 12.3, Sign: "W · back to work"}
	for _, tier := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.Ascii} {
		c, l, _ := roomFrame(t, tier, glyphs.Unicode, 48, 160, in)
		my, mx := l.Y+l.H/2, l.X+l.W/2
		for y := l.Y; y < l.Y+l.H/2; y++ {
			if y == my {
				continue
			}
			r := []rune(c.Text(y))
			for x := l.X; x < l.X+l.W; x++ {
				if x != mx && r[x] != 'x' {
					t.Fatalf("tier %v: (%d,%d) is %q, not the dashboard's letter", tier, y, x, string(r[x]))
				}
			}
		}
		if r := []rune(c.Text(my)); r[mx] != '╋' && r[mx] != '┼' {
			t.Errorf("tier %v: no mullion cross, %q", tier, string(r[mx]))
		}
		if s := strings.Join(c.Lines(), "\n"); !strings.Contains(s, "back to work") {
			t.Errorf("tier %v: no plaque", tier)
		}
	}
}

// TestRoomStillWithoutMotion: with nothing moving, the frame is the same
// whatever the frame clock says.
func TestRoomStillWithoutMotion(t *testing.T) {
	at := time.Date(2031, 4, 16, 22, 10, 0, 0, time.UTC)
	a, _, _ := roomFrame(t, colorprofile.TrueColor, glyphs.Nerd, 40, 120, RoomInfo{Clock: at, T: 1, Sign: "W · back to work"})
	b, _, _ := roomFrame(t, colorprofile.TrueColor, glyphs.Nerd, 40, 120, RoomInfo{Clock: at, T: 99, Sign: "W · back to work"})
	if a.Render(rawRender) != b.Render(rawRender) {
		t.Error("the still room changed between frames")
	}
	// and it moves when motion is on
	c, _, _ := roomFrame(t, colorprofile.TrueColor, glyphs.Nerd, 40, 120, RoomInfo{Clock: at, T: 1, Moving: true, Sign: "W · back to work"})
	d, _, _ := roomFrame(t, colorprofile.TrueColor, glyphs.Nerd, 40, 120, RoomInfo{Clock: at, T: 3.4, Moving: true, Sign: "W · back to work"})
	if c.Render(rawRender) == d.Render(rawRender) {
		t.Error("nothing in the room moves")
	}
}

func rawRender(s, style string, _ text.Attr, _, _ bool) string { return style + s }
