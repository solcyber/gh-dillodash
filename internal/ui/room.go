package ui

import (
	"fmt"
	"github.com/solcyber/gh-dillodash/internal/fortress"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// The room: while the release window is open the dashboard is drawn as
// the view out of a window (scenes.Room). W steps out of it to the plain
// dashboard and back in; outside the window W shows the room too. A
// change of the window (it opens, it closes) forgets W, so every night's
// window starts in the room ([display] room = false starts it outside).

// inReleaseWindow: the maintenance window workflow says the release span
// is open now. Without that workflow there is no window.
func (m *Model) inReleaseWindow() bool {
	w, ok := m.wf.Get("maintenance_window").(interface{ InReleaseWindow(time.Time) bool })
	return ok && w.InReleaseWindow(clock.Now())
}

// roomWanted: the room should be shown (if the screen has room for it).
func (m *Model) roomWanted() bool {
	in := m.inReleaseWindow()
	if in != m.roomIn {
		m.roomIn, m.roomFlip = in, false
	}
	base := in && m.cfg != nil && m.cfg.Display.Room
	return base != m.roomFlip
}

// roomAt is the room's layout on an h x w screen, when it is shown.
func (m *Model) roomAt(h, w int) (scenes.RoomLayout, bool) {
	if !m.roomWanted() {
		return scenes.RoomLayout{}, false
	}
	l, ok := scenes.RoomFit(h, w)
	if ok && m.view == ViewFortress && (l.H < fortress.MinH || l.W < fortress.MinW) {
		// the glass is too small for the map on this screen (100x32 makes
		// it 17 rows): the fortress takes the whole terminal instead of
		// saying the terminal is too small
		return scenes.RoomLayout{}, false
	}
	return l, ok
}

// layoutW and layoutH are the size the dashboard is laid out in: the
// glass while the room is shown, else the terminal.
func (m *Model) layoutW() int {
	if l, ok := m.roomAt(m.h, m.w); ok {
		return l.W
	}
	return m.w
}

func (m *Model) layoutH() int {
	if l, ok := m.roomAt(m.h, m.w); ok {
		return l.H
	}
	return m.h
}

// roomChipInWindow and roomChipOutside gate W in the key bar: the screen
// has room for the room and it is not shown (in the room the plaque on
// the wall names the key instead). In the release window the chip is the
// reminder that you stepped out, so it stays late when the bar is short;
// outside the window it is a way in, and among the first to go.
func (m *Model) roomChipInWindow() bool {
	return m.inReleaseWindow() && m.roomChipFits()
}

func (m *Model) roomChipOutside() bool {
	return !m.inReleaseWindow() && m.roomChipFits()
}

func (m *Model) roomChipFits() bool {
	_, fits := scenes.RoomFit(m.h, m.w)
	_, shown := m.roomAt(m.h, m.w)
	return fits && !shown
}

// roomSign is the plaque's text.
func roomSign() string {
	if glyphs.Active() == glyphs.ASCII {
		return "W: back to work"
	}
	return "W · back to work"
}

// drawRoom paints the room with the dashboard behind its glass; false
// when the room is not shown (draw paints the plain dashboard).
func (m *Model) drawRoom(c *canvas.Canvas) bool {
	l, ok := m.roomAt(c.H, c.W)
	if !ok {
		return false
	}
	if m.roomInner == nil || m.roomInner.W != l.W || m.roomInner.H != l.H {
		m.roomInner = canvas.New(l.W, l.H)
	} else {
		m.roomInner.Clear()
	}
	m.drawFrame(m.roomInner)
	in := scenes.RoomInfo{Tier: theme.Current(), Moving: fx.Ambient, T: fx.Now(), Sign: roomSign()}
	in.Clock = clock.Now()
	if m.cfg != nil {
		if loc := m.cfg.Display.ClockLocal(); loc != nil {
			in.Clock = loc.In(in.Clock)
		}
	}
	scenes.Room(c, l, in)
	c.Blit(m.roomInner, l.Y, l.X)
	scenes.RoomFront(c, l, in)
	if in.Moving && scenes.RoomAnimated(in.Tier) {
		m.ambient = true // the plants sway, the steam rises
	}
	return true
}

// doRoom is W: into the room, or back to work.
func doRoom(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.roomWanted() // settle the window's state before flipping
	m.roomFlip = !m.roomFlip
	if !m.roomWanted() {
		m.setStatus("back to work — W steps back into the room")
		return nil
	}
	if _, ok := scenes.RoomFit(m.h, m.w); !ok {
		m.roomFlip = !m.roomFlip
		m.notice(fmt.Sprintf("the room needs a terminal of %d×%d or more — this one is %d×%d", scenes.RoomMinW, scenes.RoomMinH, m.w, m.h))
		return nil
	}
	m.setStatus("in the room — the plaque under the window says how to leave")
	return nil
}
