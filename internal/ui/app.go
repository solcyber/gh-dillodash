package ui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

type tickMsg struct{}

// Run starts the dashboard and blocks until it quits.
func Run(ctx context.Context, o Options) error {
	m := New(ctx, o)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	m.prog = p
	_, err := p.Run()
	return err
}

// Init starts the first refresh, the splash and the tick clock.
func (m *Model) Init() tea.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh(false)
	m.fx.Start("splash", 3600, nil) // the splash ends itself; any key skips it
	return tea.Batch(tea.RequestWindowSize, m.tick())
}

// tick schedules the next frame (lock held: tickMS reads the state).
func (m *Model) tick() tea.Cmd {
	d := time.Duration(m.tickMS()) * time.Millisecond
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// tickMS: FrameMS while a one-shot effect plays, TickMS while a spinner
// spins, AmbientMS for ambient motion, 400ms otherwise (still often
// enough for the once-a-second countdowns).
func (m *Model) tickMS() int {
	if m.view == ViewFortress && m.fortress != nil {
		return m.fortressTickMS()
	}
	if m.fx.Active() {
		return fx.FrameMS
	}
	if m.animating {
		return TickMS
	}
	if m.ambient {
		return fx.AmbientMS
	}
	return 400
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		if m.refreshDue() {
			m.refresh(false) // no-ops while rate-limited
		}
		return m, m.tick()
	case refreshDoneMsg:
		m.applyRefresh(msg.out)
	case showModalMsg:
		m.modal = msg.modal
		m.modalDone = msg.done
		m.fx.Start("menu", 0.2, nil)
	case redrawMsg:
	case execMsg:
		return m, m.handleExec(msg)
	case execDoneMsg:
		msg.done <- msg.err
	case actionDoneMsg:
		m.action = false
	case tea.KeyPressMsg:
		return m, m.handleKey(toKey(msg))
	}
	return m, nil
}

// View paints the frame.
func (m *Model) View() tea.View {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := canvas.New(max(1, m.w), max(1, m.h))
	m.draw(c)
	v := tea.NewView(c.Render(render))
	v.AltScreen = true
	return v
}

func render(s, style string, extra text.Attr, sel, band bool) string {
	return theme.Render([]text.Seg{{Text: s, Style: style, Extra: extra}}, sel, band)
}

// toKey normalizes a Bubble Tea key press.
func toKey(k tea.KeyPressMsg) widgets.Key {
	s := k.String()
	switch s {
	case "pgdown":
		s = "pgdn"
	case "space":
		return widgets.Key{Rune: ' '}
	}
	if k.Text != "" && !strings.Contains(s, "ctrl+") && !strings.Contains(s, "alt+") {
		r := []rune(k.Text)
		if len(r) == 1 && r[0] >= 0x20 {
			return widgets.Key{Rune: r[0]}
		}
	}
	return widgets.Key{Name: s}
}

func isEnter(k widgets.Key) bool { return k.Name == "enter" }
func isEsc(k widgets.Key) bool   { return k.Name == "esc" }

// handleKey dispatches a key (lock held).
func (m *Model) handleKey(k widgets.Key) tea.Cmd {
	if m.modal != nil {
		if m.modal.Handle(k) {
			close(m.modalDone)
			m.modal, m.modalDone = nil, nil
		}
		return nil
	}
	m.lastKey = fx.Now()
	if _, splash := m.fx.Elapsed("splash"); splash {
		m.endSplash() // any key skips the splash, and only that
		return nil
	}
	// any key ends the release launch and still does its own job
	m.fx.Stop("launch")
	if m.overlay != "" {
		m.overlay = "" // any key closes the help
		return nil
	}
	if k.Rune == '?' {
		m.overlay = "help"
		m.fx.Start("help", 0.2, nil)
		return nil
	}
	if k.Rune == ':' {
		if m.view == ViewList || m.view == ViewFortress {
			m.detailFrom = m.view
		}
		app := &lockedApp{m}
		m.runAction(func() { m.doGoto(app) })
		return nil
	}
	if m.action {
		return nil // an action is mid-flight: its modal owns the keys
	}
	page := max(1, m.h-4)
	app := &lockedApp{m}
	switch m.view {
	case ViewList:
		return m.listKey(k, page, app)
	case ViewFortress:
		if !m.fortressKey(k) {
			m.view = ViewList
		}
	case ViewDetail:
		m.detailKey(k, page, app)
	case ViewFiles:
		m.filesKey(k, page, app)
	default:
		m.patchKey(k, page, app)
	}
	return nil
}

func (m *Model) listKey(k widgets.Key, page int, app *lockedApp) tea.Cmd {
	if wk, ok := m.wf.Key(ViewList, string(k.Rune)); ok && k.Rune != 0 {
		m.runAction(func() { wk.Handler(app) })
		return nil
	}
	switch {
	case k.Rune == 'q':
		return tea.Quit
	case isEsc(k):
		if m.filter != "" {
			m.filter = ""
			m.refilter()
		}
	case k.Rune == 'j' || k.Name == "down":
		was := m.selected
		m.moveCursor(+1, true)
		m.leaveTrail(was)
	case k.Rune == 'k' || k.Name == "up":
		was := m.selected
		m.moveCursor(-1, true)
		m.leaveTrail(was)
	case k.Rune == 'g' || k.Name == "home":
		m.selected = 0
		m.cursorOffHeaders(+1)
	case k.Rune == 'G' || k.Name == "end":
		m.selected = max(0, len(m.rows)-1)
		m.cursorOffHeaders(-1)
	case k.Name == "pgdn" || k.Rune == ' ':
		for i := 0; i < page; i++ {
			m.moveCursor(+1, false)
		}
	case k.Name == "pgup":
		for i := 0; i < page; i++ {
			m.moveCursor(-1, false)
		}
	case isEnter(k) || k.Rune == 'l' || k.Name == "right":
		if row, ok := m.Selected(); ok {
			if rk, has := m.wf.RowKinds()[row.Kind]; has && rk.Open != nil {
				if rk.Open(m, row) {
					return nil
				}
			}
			m.detailFrom = ViewList
			switch v := row.Item.(type) {
			case *model.PR:
				repo, n := m.repoOf(v), v.Number
				m.runAction(func() { m.openDetail(app, repo, n, "pr", v) })
			case *model.Issue:
				repo, n := m.repoOf(v), v.Number
				m.runAction(func() { m.openDetail(app, repo, n, "issue", nil) })
			}
		}
	case k.Rune == 'A':
		m.showAll = !m.showAll
		m.refilter()
	case k.Rune == 'D':
		m.runAction(func() { m.doDeployApprove(app) })
	case k.Rune == 'd':
		m.openFortress()
	case k.Rune == '/':
		m.runAction(func() { m.startFilter(app) })
	case k.Rune == 'y':
		m.copyURL()
	case k.Rune == 'o':
		m.openInBrowser()
	case k.Rune == 'r':
		m.manualRefresh()
	}
	return nil
}

func (m *Model) detailKey(k widgets.Key, page int, app *lockedApp) {
	view := ViewDetail
	if m.detailKind == "issue" {
		view = "issue"
	}
	if wk, ok := m.wf.Key(view, string(k.Rune)); ok && k.Rune != 0 {
		m.runAction(func() { wk.Handler(app) })
		return
	}
	switch {
	case isEsc(k) && m.searchQ != "":
		m.searchQ = "" // Esc first clears a search highlight
	case k.Rune == 'q' || k.Rune == 'h' || isEsc(k) || k.Name == "left":
		if m.fortress != nil && m.detailFrom == ViewFortress {
			m.view = ViewFortress
		} else {
			m.view = ViewList
		}
		m.detailFrom = ViewList
		m.detail = nil
		m.detailListed = nil
		m.searchQ = ""
	case k.Rune == 'j' || k.Name == "down":
		m.scroll++
	case k.Rune == 'k' || k.Name == "up":
		m.scroll = max(0, m.scroll-1)
	case k.Name == "pgdn" || k.Rune == ' ':
		m.scroll += page
	case k.Name == "pgup":
		m.scroll = max(0, m.scroll-page)
	case k.Rune == 'g' || k.Name == "home":
		m.scroll = 0
	case k.Rune == 'G' || k.Name == "end":
		m.scroll = len(m.detailLines)
	case k.Rune == '/':
		m.runAction(func() { m.startSearch(app) })
	case k.Rune == 'n':
		m.searchStep(+1)
	case k.Rune == 'N':
		m.searchStep(-1)
	case k.Rune == 'm':
		m.raw = !m.raw
		if m.raw {
			m.setStatus("raw markdown")
		} else {
			m.setStatus("rendered markdown")
		}
	case k.Rune == 'a':
		m.runAction(func() { m.doApprove(app) })
	case k.Rune == 'M':
		m.runAction(func() { m.doMerge(app) })
	case k.Rune == 'D':
		m.runAction(func() { m.doDeployApprove(app) })
	case k.Rune == 'c':
		m.runAction(func() { m.doComment(app, false, "") })
	case k.Rune == 'C':
		m.runAction(func() { m.doComment(app, true, "") })
	case k.Rune == 't':
		m.runAction(func() { m.doTag(app) })
	case k.Rune == 'f':
		if m.detailKind == "issue" {
			m.notice("issues have no changed files")
		} else {
			m.runAction(func() { m.openFiles(app, false) })
		}
	case k.Rune == 'v':
		if m.detailKind == "issue" {
			m.notice("issues have no diff")
		} else {
			m.runAction(func() { m.openDiffPager(app) })
		}
	case k.Rune == 'y':
		m.copyURL()
	case k.Rune == 'o':
		m.openInBrowser()
	case k.Rune == 'r':
		m.manualRefresh()
	}
}

func (m *Model) filesKey(k widgets.Key, page int, app *lockedApp) {
	switch {
	case k.Rune == 'q' || k.Rune == 'h' || isEsc(k) || k.Name == "left":
		m.view = ViewDetail
	case k.Rune == 'j' || k.Name == "down":
		m.fileSel = min(m.fileSel+1, max(0, len(m.files)-1))
	case k.Rune == 'k' || k.Name == "up":
		m.fileSel = max(m.fileSel-1, 0)
	case k.Rune == 'g' || k.Name == "home":
		m.fileSel = 0
	case k.Rune == 'G' || k.Name == "end":
		m.fileSel = max(0, len(m.files)-1)
	case k.Name == "pgdn" || k.Rune == ' ':
		m.fileSel = min(max(0, len(m.files)-1), m.fileSel+page)
	case k.Name == "pgup":
		m.fileSel = max(0, m.fileSel-page)
	case isEnter(k) || k.Rune == 'l' || k.Name == "right":
		m.openPatch()
	case k.Rune == 'v':
		m.runAction(func() { m.openDiffPager(app) })
	case k.Rune == 'y':
		m.copyURL()
	case k.Rune == 'o':
		m.openInBrowser()
	case k.Rune == 'r':
		m.runAction(func() { m.openFiles(app, true) })
	}
}

func (m *Model) patchKey(k widgets.Key, page int, app *lockedApp) {
	switch {
	case isEsc(k) && m.searchQ != "":
		m.searchQ = ""
	case k.Rune == 'q' || k.Rune == 'h' || isEsc(k) || k.Name == "left":
		m.view = ViewFiles
		m.searchQ = ""
	case k.Rune == 'j' || k.Name == "down":
		m.patchScroll++
	case k.Rune == 'k' || k.Name == "up":
		m.patchScroll = max(0, m.patchScroll-1)
	case k.Name == "pgdn" || k.Rune == ' ':
		m.patchScroll += page
	case k.Name == "pgup":
		m.patchScroll = max(0, m.patchScroll-page)
	case k.Rune == 'g' || k.Name == "home":
		m.patchScroll = 0
	case k.Rune == 'G' || k.Name == "end":
		m.patchScroll = len(m.patchLines)
	case k.Rune == '/':
		m.runAction(func() { m.startSearch(app) })
	case k.Rune == 'n':
		m.searchStep(+1)
	case k.Rune == 'N':
		m.searchStep(-1)
	case k.Rune == 'v':
		m.runAction(func() { m.openDiffPager(app) })
	case k.Rune == 'y':
		m.copyURL()
	case k.Rune == 'o':
		m.openInBrowser()
	case k.Rune == 'r':
		m.manualRefresh()
	}
}
