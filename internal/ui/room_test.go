package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// windowStub is a maintenance window that is open while *open says so.
type windowStub struct {
	workflow.Base
	open *bool
}

func (windowStub) Name() string                     { return "maintenance_window" }
func (w windowStub) InReleaseWindow(time.Time) bool { return *w.open }

// roomHarness is the harness with a window workflow whose window is
// open while *open, on a w x h screen.
func roomHarness(t *testing.T, open *bool, w, h int) *harness {
	t.Helper()
	hs := newHarness(t)
	set, err := workflow.NewSet(nil, windowStub{open: open})
	if err != nil {
		t.Fatal(err)
	}
	hs.m.wf = set
	hs.m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	hs.load()
	return hs
}

func shown(h *harness) bool {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	_, ok := h.m.roomAt(h.m.h, h.m.w)
	return ok
}

// TestRoomShowsInTheWindow: the room follows the window, W flips it for
// the night, and the next window starts in the room again.
func TestRoomShowsInTheWindow(t *testing.T) {
	open := false
	h := roomHarness(t, &open, 160, 48)
	if shown(h) {
		t.Fatal("the room shows outside the window")
	}
	open = true
	if !shown(h) {
		t.Fatal("no room in the window")
	}
	if got := h.m.Width(); got >= 160 || got < 60 {
		t.Errorf("the dashboard is laid out %d wide in the room", got)
	}
	h.key('w')
	if shown(h) {
		t.Fatal("W did not step out of the room")
	}
	if got := h.m.Width(); got != 160 {
		t.Errorf("out of the room the layout is %d wide", got)
	}
	if !strings.Contains(h.screen(), "steps back into the room") {
		t.Errorf("no status for W:\n%s", h.screen())
	}
	h.key('w')
	if !shown(h) {
		t.Fatal("W did not step back in")
	}
	h.key('w')
	open = false // the window closes: W is forgotten
	if shown(h) {
		t.Fatal("the room outlived the window")
	}
	open = true // the next night's window opens in the room
	if !shown(h) {
		t.Fatal("the next window did not start in the room")
	}
	// outside the window W previews the room
	open = false
	shown(h)
	h.key('w')
	if !shown(h) {
		t.Fatal("W outside the window shows no room")
	}
}

// TestRoomNeedsTheSpace: a small terminal keeps the plain dashboard and
// W says why.
func TestRoomNeedsTheSpace(t *testing.T) {
	open := true
	h := roomHarness(t, &open, scenes.RoomMinW-1, 40)
	if shown(h) {
		t.Fatal("the room shows on a small terminal")
	}
	h.key('w')
	h.key('w') // the first w stepped out of a room too small to show; this one tries to step in
	if s := h.screen(); !strings.Contains(s, "the room needs a terminal") {
		t.Errorf("no reason given:\n%s", s)
	}
}

// TestRoomConfigOff: room = false keeps the window plain; W still shows it.
func TestRoomConfigOff(t *testing.T) {
	open := true
	h := roomHarness(t, &open, 160, 48)
	h.m.mu.Lock()
	h.m.cfg.Display.Room = false
	h.m.mu.Unlock()
	if shown(h) {
		t.Fatal("room = false still shows the room")
	}
	h.key('w')
	if !shown(h) {
		t.Fatal("W shows no room with room = false")
	}
}

// TestRoomScreen: in the room the dashboard is inside the glass with the
// mullions across it and the plaque under it; out of it the screen is
// the plain dashboard, exactly.
var digits = regexp.MustCompile(`[0-9]`)

func TestRoomScreen(t *testing.T) {
	open := true
	h := roomHarness(t, &open, 160, 48)
	s := h.screen()
	lines := strings.Split(s, "\n")
	l, _ := scenes.RoomFit(48, 160)
	if !strings.Contains(s, "w: back to work") {
		t.Errorf("no plaque:\n%s", s)
	}
	if !strings.Contains(s, "#101") {
		t.Errorf("the dashboard is not behind the glass:\n%s", s)
	}
	if mid := []rune(lines[l.Y+l.H/2]); string(mid[l.X+l.W/2]) != "+" {
		t.Errorf("no mullion cross at the middle of the glass: %q", string(mid))
	}
	// the list is laid out to the glass: its rows end inside it
	for y, ln := range lines {
		r := []rune(ln)
		if y >= l.Y && y < l.Y+l.H && strings.Contains(ln, "#101") && strings.Contains(string(r[:l.X]), "#101") {
			t.Errorf("row %d draws left of the glass: %q", y, ln)
		}
	}
	h.key('w')
	out := strings.Split(h.screen(), "\n")

	closed := false
	p := roomHarness(t, &closed, 160, 48)
	want := strings.Split(p.screen(), "\n")
	if len(out) != len(want) {
		t.Fatalf("%d lines out of the room, %d plain", len(out), len(want))
	}
	for i := range want {
		if i == len(want)-2 {
			continue // the status line says what W did
		}
		if i == len(want)-1 {
			if !strings.Contains(out[i], " w  room ") {
				t.Errorf("the bar lacks w while the window is open: %q", out[i])
			}
			continue
		}
		// digits masked: the header's clock and refresh countdown tick
		// between the two harnesses on a slow runner
		if digits.ReplaceAllString(out[i], "9") != digits.ReplaceAllString(want[i], "9") {
			t.Errorf("line %d out of the room:\n%q\nplain:\n%q", i, out[i], want[i])
		}
	}
}

// TestRoomChipAnyTime: the bar offers W outside the window (a way in)
// and in the window once you stepped out (the reminder); in the room
// the plaque names it instead.
func TestRoomChipAnyTime(t *testing.T) {
	open := false
	h := roomHarness(t, &open, 160, 48)
	bar := func() string {
		lines := strings.Split(h.screen(), "\n")
		return lines[len(lines)-1]
	}
	if !strings.Contains(bar(), "w  room") {
		t.Errorf("outside the window the bar has no w:\n%s", bar())
	}
	h.key('w')
	if !shown(h) || strings.Contains(bar(), "w  room") {
		t.Errorf("in the room the bar still offers w:\n%s", bar())
	}
	h.key('w')
	open = true
	if !shown(h) {
		t.Fatal("no room in the window")
	}
	h.key('w')
	if shown(h) || !strings.Contains(bar(), "w  room") {
		t.Errorf("stepped out in the window, the bar has no w:\n%s", bar())
	}
}
