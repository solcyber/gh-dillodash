package ui

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// tickMsg is the frame clock; gen lets a wake-up (retick) orphan the
// tick that was already scheduled, so a key never waits for a long idle
// sleep to end.
type tickMsg struct{ gen int }

// ErrInterrupted is Run's error when the dashboard was interrupted
// (ctrl-c, or SIGINT): main exits 130 without a message.
var ErrInterrupted = errors.New("interrupted")

// Run starts the dashboard and blocks until it quits. Bubble Tea owns
// the process signals while the TUI is up (SIGTERM quits, SIGINT
// interrupts, both are masked while $PAGER or $EDITOR has the terminal),
// so the context given here is not a signal context: it is the parent
// of the workers' context, which is cancelled when the dashboard ends so
// a refresh or action in flight stops with it.
func Run(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := New(ctx, o)
	p := tea.NewProgram(m)
	m.prog = p
	_, err := p.Run()
	if errors.Is(err, tea.ErrInterrupted) {
		return ErrInterrupted
	}
	return err
}

// quitArmSecs is how long a second q or ctrl-c has to confirm quitting
// while an action is still talking to GitHub.
const quitArmSecs = 3

// requestQuit is q (or ctrl-c): quit, unless an action is mid-flight,
// when the first press warns and the second within quitArmSecs quits
// anyway (a request already sent to GitHub completes regardless; only
// its outcome goes unseen).
func (m *Model) requestQuit(interrupt bool) tea.Cmd {
	armed := m.quitArmedAt > 0 && fx.Now()-m.quitArmedAt <= quitArmSecs
	if m.action && !armed {
		m.quitArmedAt = fx.Now()
		m.notice("an action is still running — press again within 3s to quit anyway (a request already sent completes regardless)")
		return nil
	}
	if interrupt {
		return tea.Interrupt
	}
	return tea.Quit
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
	gen := m.tickGen
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{gen} })
}

// retick wakes the clock now: whatever tick was pending is orphaned and
// a fresh one is scheduled from the current state (lock held).
func (m *Model) retick() tea.Cmd {
	m.tickGen++
	return m.tick()
}

// tickMS: FrameMS while a one-shot effect or a prompt's own effect
// plays, TickMS while a spinner spins, AmbientMS for ambient motion
// (a modal's too), else idleWakeMS: the next instant something on the
// screen changes, which may be a minute away. A static dashboard costs
// nothing in between.
func (m *Model) tickMS() int {
	if m.modal != nil {
		if a, ok := m.modal.(interface{ Active() bool }); ok && a.Active() {
			return fx.FrameMS
		}
		if m.modal.Animated() {
			return fx.AmbientMS
		}
	}
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
	return m.idleWakeMS()
}

// idleMaxMS is the longest the idle loop sleeps: ages and the window
// countdowns are minute-grained.
const idleMaxMS = 60000

// idleWakeMS is how long until the next thing on the screen changes
// when nothing animates: the refresh falling due, the next whole second
// of a countdown (the header's next refresh, a rate-limit hold, the
// retry of a failed load), the toast's fade and expiry; else a minute.
func (m *Model) idleWakeMS() int {
	now := fx.Now()
	next := now + idleMaxMS/1000
	at := func(t float64) {
		if t > now+0.005 && t < next {
			next = t
		}
	}
	// the fraction of a second until a countdown's next whole second
	frac := func(d float64) float64 {
		f := d - math.Floor(d)
		if f < 0.005 {
			f = 1
		}
		return f
	}
	at(m.nextRefreshAt)
	if (m.loaded && !m.refreshing) || (!m.loaded && m.errText != "") {
		at(now + frac(m.nextRefreshAt-now))
	}
	if hold := m.backoff.RateLimitWait(clock.Now()).Seconds(); hold > 0 {
		at(now + frac(hold))
		at(now + hold)
	}
	if m.status != "" && m.statusKind != "spin" {
		at(m.statusAt + fx.ToastSecs - 0.8)
		at(m.statusAt + fx.ToastSecs)
	}
	return max(50, min(int((next-now)*1000)+5, idleMaxMS))
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		if msg.gen != m.tickGen {
			return m, nil // orphaned by a retick
		}
		if m.refreshDue() {
			m.refresh(false) // no-ops while rate-limited
		}
		return m, m.tick()
	case refreshDoneMsg:
		m.applyRefresh(msg.out)
	case showModalMsg:
		m.modal = msg.modal
		m.modalDone = msg.done
		if p, ok := msg.modal.(*widgets.Pick); ok {
			p.Open(m.fx) // the menu pops; prompts and confirms just appear
		}
	case redrawMsg:
	case execMsg:
		return m, m.handleExec(msg)
	case execDoneMsg:
		msg.done <- msg.err
	case actionDoneMsg:
		m.action = false
	case tea.KeyPressMsg:
		return m, tea.Batch(m.handleKey(toKey(msg)), m.retick())
	case tea.PasteMsg:
		return m, tea.Batch(m.handleKey(widgets.T(msg.Content)), m.retick())
	case tea.PasteStartMsg, tea.PasteEndMsg:
		return m, nil
	default:
		return m, nil
	}
	// something changed: wake the clock so the next frame is drawn
	// from the new state, not after an idle sleep
	return m, m.retick()
}

// View paints the frame.
func (m *Model) View() tea.View {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, h := max(1, m.w), max(1, m.h)
	if m.canvas == nil || m.canvas.W != w || m.canvas.H != h {
		m.canvas = canvas.New(w, h)
	} else {
		m.canvas.Clear() // one allocation per resize, not per frame
	}
	c := m.canvas
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
		switch {
		case len(r) == 1 && r[0] >= 0x20:
			return widgets.Key{Rune: r[0]}
		case len(r) > 1:
			// an IME commit, an emoji with a modifier: text for a prompt
			return widgets.T(k.Text)
		}
	}
	return widgets.Key{Name: s}
}

// overlayKey: the help and message overlays scroll with j/k and the
// paging keys; any other key closes them.
func (m *Model) overlayKey(k widgets.Key) {
	page := max(1, m.h-6)
	switch {
	case k.Rune == 'j' || k.Name == "down":
		m.helpTop++
	case k.Rune == 'k' || k.Name == "up":
		m.helpTop = max(0, m.helpTop-1)
	case k.Name == "pgdn" || k.Rune == ' ':
		m.helpTop += page
	case k.Name == "pgup":
		m.helpTop = max(0, m.helpTop-page)
	case k.Rune == 'g' || k.Name == "home":
		m.helpTop = 0
	case k.Rune == 'G' || k.Name == "end":
		m.helpTop = 1 << 20 // the drawer clamps it to the end
	default:
		m.overlay, m.helpTop = "", 0
	}
}

func isEnter(k widgets.Key) bool { return k.Name == "enter" }
func isEsc(k widgets.Key) bool   { return k.Name == "esc" }

// handleKey dispatches a key (lock held).
func (m *Model) handleKey(k widgets.Key) tea.Cmd {
	if k.Name == "ctrl+c" && m.modal == nil {
		// raw mode delivers ctrl-c as a key, not a signal; a modal owns
		// it (it cancels the prompt) and everywhere else it quits
		return m.requestQuit(true)
	}
	m.lastKey = fx.Now() // typing in a prompt keeps ambient motion awake too
	if m.modal != nil {
		if m.modal.Handle(k) {
			close(m.modalDone)
			m.modal, m.modalDone = nil, nil
		}
		return nil
	}
	if _, splash := m.fx.Elapsed("splash"); splash {
		m.endSplash() // any key skips the splash, and only that
		return nil
	}
	// any key ends the release launch and still does its own job
	m.fx.Stop("launch")
	if m.overlay != "" {
		m.overlayKey(k)
		return nil
	}
	if m.action {
		if m.view == ViewList && k.Rune == 'q' {
			return m.requestQuit(false) // warns once, quits on the second press
		}
		return nil // an action is mid-flight: its modal owns the keys
	}
	view := m.view
	if view == ViewDetail && m.detailKind == "issue" {
		view = "issue"
	}
	page := max(1, m.h-4)
	app := &lockedApp{m}
	if b := lookup(view, k); b != nil && b.Do != nil {
		return b.Do(m, k, page, app)
	}
	if wk, ok := m.wf.Key(view, string(k.Rune)); ok && k.Rune != 0 {
		m.runAction(func() { wk.Handler(app) })
	}
	return nil
}

// ---- the key table's actions (lock held) ----

// doNav moves or scrolls in the current view: a row or a line for j/k,
// a page for pgdn/pgup/space, the ends for g/G and home/end.
func doNav(m *Model, k widgets.Key, page int, _ *lockedApp) tea.Cmd {
	step := 0
	switch {
	case k.Rune == 'j' || k.Name == "down":
		step = 1
	case k.Rune == 'k' || k.Name == "up":
		step = -1
	case k.Name == "pgdn" || k.Rune == ' ':
		step = page
	case k.Name == "pgup":
		step = -page
	}
	top := k.Rune == 'g' || k.Name == "home"
	bottom := k.Rune == 'G' || k.Name == "end"
	switch m.view {
	case ViewList:
		switch {
		case top:
			m.selected = 0
			m.cursorOffHeaders(+1)
		case bottom:
			m.selected = max(0, len(m.rows)-1)
			m.cursorOffHeaders(-1)
		case step == 1 || step == -1:
			was := m.selected
			m.moveCursor(step, true)
			m.leaveTrail(was)
		default:
			dir := 1
			if step < 0 {
				dir, step = -1, -step
			}
			for i := 0; i < step; i++ {
				m.moveCursor(dir, false)
			}
		}
	case ViewFiles:
		last := max(0, len(m.files)-1)
		switch {
		case top:
			m.fileSel = 0
		case bottom:
			m.fileSel = last
		default:
			m.fileSel = min(last, max(0, m.fileSel+step))
		}
	case ViewPatch:
		switch {
		case top:
			m.patchScroll = 0
		case bottom:
			m.patchScroll = len(m.patchLines)
		default:
			m.patchScroll = max(0, m.patchScroll+step)
		}
	default: // detail, issue
		switch {
		case top:
			m.scroll = 0
		case bottom:
			m.scroll = len(m.detailLines)
		default:
			m.scroll = max(0, m.scroll+step)
		}
	}
	return nil
}

// doOpen opens the selected row (list) or the selected file's diff.
func doOpen(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	if m.view == ViewFiles {
		m.openPatch()
		return nil
	}
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	if rk, has := m.wf.RowKinds()[row.Kind]; has && rk.Open != nil && rk.Open(m, row) {
		return nil
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
	return nil
}

// doBack leaves the detail, files or diff view; in the detail and the
// diff, Esc first clears a search highlight.
func doBack(m *Model, k widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	if isEsc(k) && m.searchQ != "" && m.view != ViewFiles {
		m.searchQ = ""
		return nil
	}
	switch m.view {
	case ViewFiles:
		m.view = ViewDetail
	case ViewPatch:
		m.view = ViewFiles
		m.searchQ = ""
	default:
		if m.fortress != nil && m.detailFrom == ViewFortress {
			m.view = ViewFortress
		} else {
			m.view = ViewList
		}
		m.detailFrom = ViewList
		m.detail = nil
		m.detailListed = nil
		m.searchQ = ""
	}
	return nil
}

func doFilter(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.startFilter(app) })
	return nil
}

func doClearFilter(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	if m.filter != "" {
		m.filter = ""
		m.refilter()
	}
	return nil
}

func doShowAll(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.showAll = !m.showAll
	m.refilter()
	return nil
}

func doDeploys(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doDeployApprove(app) })
	return nil
}

func doFortress(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.openFortress()
	return nil
}

func doApprove(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doApprove(app) })
	return nil
}

func doMerge(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doMerge(app) })
	return nil
}

func doComment(m *Model, k widgets.Key, _ int, app *lockedApp) tea.Cmd {
	editor := k.Rune == 'C'
	m.runAction(func() { m.doComment(app, editor, "") })
	return nil
}

func doTag(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doTag(app) })
	return nil
}

func doFiles(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	if m.detailKind == "issue" {
		m.notice("issues have no changed files")
		return nil
	}
	m.runAction(func() { m.openFiles(app, false) })
	return nil
}

func doFilesReload(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.openFiles(app, true) })
	return nil
}

func doDiff(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	if m.detailKind == "issue" {
		m.notice("issues have no diff")
		return nil
	}
	m.runAction(func() { m.openDiffPager(app) })
	return nil
}

func doSearch(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.startSearch(app) })
	return nil
}

func doSearchStep(m *Model, k widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	if k.Rune == 'N' {
		m.searchStep(-1)
	} else {
		m.searchStep(+1)
	}
	return nil
}

func doRaw(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.raw = !m.raw
	if m.raw {
		m.setStatus("raw markdown")
	} else {
		m.setStatus("rendered markdown")
	}
	return nil
}

func doFortressKey(m *Model, k widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	if !m.fortressKey(k) {
		m.view = ViewList
	}
	return nil
}

func doCopyURL(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.copyURL()
	return nil
}

func doOpenURL(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.openInBrowser()
	return nil
}

func doRefresh(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.manualRefresh()
	return nil
}

func doQuit(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	return m.requestQuit(false)
}

func doInterrupt(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	return m.requestQuit(true)
}

func doGoto(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	if m.view == ViewList || m.view == ViewFortress {
		m.detailFrom = m.view
	}
	m.runAction(func() { m.doGoto(app) })
	return nil
}

func doHelp(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	m.overlay, m.helpTop = "help", 0
	m.fx.Start("help", 0.2, nil)
	return nil
}

func doMessage(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	if m.lastMsg == "" {
		m.notice("no message yet")
	} else {
		m.overlay, m.helpTop = "message", 0
	}
	return nil
}
