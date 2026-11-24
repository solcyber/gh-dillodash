package ui

import (
	"fmt"
	"math"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
)

// Attention is whether anybody is watching: the dashboard spends
// GitHub's budget on a screen someone reads, and while nobody does it
// slows down, catching up the moment they are back (a key, or the
// terminal focused again) so the screen is never seen stale.
type attention int

const (
	attended attention = iota // focused, and a key within idle_after_secs
	idle                      // focused (or focus unknown), no key since
	away                      // the terminal lost focus
)

func (a attention) String() string {
	switch a {
	case idle:
		return "idle"
	case away:
		return "away"
	}
	return "attended"
}

// The cadence while nobody watches. A refresh stretches to twice its
// interval idle and three times away, never past awayRefreshCap; a
// poll (waiting runs, a release in flight) to idlePoll and awayPoll.
const (
	idlePoll       = 2 * time.Minute
	awayPoll       = 5 * time.Minute
	awayRefreshCap = 15 * time.Minute
	// catchUpAfter: a return within this long of the last refresh does
	// not refresh again (the screen is that fresh already)
	catchUpAfter = 20.0
)

// attention reads the state now (lock held).
func (m *Model) attention() attention {
	if m.focusKnown && !m.focused {
		return away
	}
	secs := m.cfg.Display.IdleAfterSecs
	if secs <= 0 {
		return attended
	}
	if fx.Now()-m.lastInput > float64(secs) {
		return idle
	}
	return attended
}

// noteInput is a key or a paste: the viewer is here. Coming back from
// idle or away catches up at once.
func (m *Model) noteInput() {
	was := m.attention()
	m.lastInput = fx.Now()
	if was != attended {
		m.catchUp()
	}
}

// noteFocus is the terminal's focus report (a terminal that never sends
// one leaves the idle timer alone in charge).
func (m *Model) noteFocus(focused bool) {
	was := m.attention()
	m.focusKnown, m.focused = true, focused
	if focused {
		m.lastInput = fx.Now() // looking is attending, until idle_after_secs
		if was != attended {
			m.catchUp()
		}
	}
}

// catchUp brings a screen nobody watched up to date: a refresh now (when
// the last one is older than catchUpAfter) and the poll due at once.
func (m *Model) catchUp() {
	if !m.loaded {
		return
	}
	if fx.Now()-m.lastRefresh > catchUpAfter {
		m.caughtUp = true
		m.refresh(false)
	}
	if m.nextPollAt > 0 {
		m.nextPollAt = fx.Now()
	}
}

// refreshEvery is the interval to the next scheduled refresh, in
// seconds: refresh_secs, stretched while nobody watches and while the
// budget runs ahead of the hour.
func (m *Model) refreshEvery() float64 {
	base := float64(m.cfg.Display.RefreshSecs)
	d := base
	switch m.attention() {
	case idle:
		d = 2 * base
	case away:
		d = 3 * base
	}
	if d > base {
		d = max(base, min(d, awayRefreshCap.Seconds()))
	}
	g := m.governor()
	d *= g.factor
	return max(d, g.refreshFloor.Seconds())
}

// pollEvery stretches a workflow's poll cadence while nobody watches
// and as the budget runs low; 0 when the budget says not to poll at all
// (the refresh still runs, and re-arms the poll when it recovers).
func (m *Model) pollEvery(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	switch m.attention() {
	case idle:
		d = max(d, idlePoll)
	case away:
		d = max(d, awayPoll)
	}
	g := m.governor()
	if g.pollsOff {
		return 0
	}
	return time.Duration(float64(d) * g.factor)
}

// soonEvery is how soon a workflow's pull-in (RefreshSoon) may bring the
// next refresh: as asked while someone watches, at the poll's idle or
// away cadence otherwise, and never sooner than the budget allows. An
// action's own follow-up comes from a key, so it is attended.
func (m *Model) soonEvery(d time.Duration) time.Duration {
	switch m.attention() {
	case idle:
		d = max(d, idlePoll)
	case away:
		d = max(d, awayPoll)
	}
	g := m.governor()
	d = time.Duration(float64(d) * g.factor)
	if g.pollsOff {
		d = max(d, time.Duration(m.cfg.Display.RefreshSecs)*time.Second)
	}
	return max(d, g.refreshFloor)
}

// govern is what the budget allows now.
type govern struct {
	factor       float64       // >= 1: every interval is stretched by it
	pollsOff     bool          // below pollsOffBelow: only the refresh reads
	refreshFloor time.Duration // below refreshFloorBelow: the refresh's least interval
	resource     string        // "rest" or "gql": the budget that set it
	until        time.Time     // the budget's reset
}

const (
	// paceSlack is how far the budget may run ahead of the hour before
	// anything slows: a burst at start (the first load) is not a trend.
	paceSlack         = 0.10
	maxGovernFactor   = 8.0
	pollsOffBelow     = 0.15
	refreshFloorBelow = 0.05
	lowRefreshFloor   = 10 * time.Minute
)

// governor reads GitHub's budget: left alone while the budget keeps
// pace with its hour (as much left as the hour has left, less
// paceSlack); else the intervals stretch by how far ahead it runs.
func (m *Model) governor() govern {
	g := govern{factor: 1}
	if m.usage == nil || !m.cfg.GitHub.BudgetGovernor {
		return g
	}
	now := clock.Now()
	b, ok := m.usage.Snapshot().Tightest(now)
	if !ok || b.Limit <= 0 {
		return g
	}
	g.resource, g.until = "gql", b.Reset
	if b.Resource == "core" {
		g.resource = "rest"
	}
	left := b.Fraction()
	hour := 1.0
	if !b.Reset.IsZero() {
		hour = math.Max(0, math.Min(1, b.Reset.Sub(now).Hours()))
	}
	if left < hour-paceSlack {
		g.factor = math.Min(maxGovernFactor, hour/math.Max(left, 0.01))
	}
	if left < pollsOffBelow {
		g.pollsOff = true
	}
	if left < refreshFloorBelow {
		g.refreshFloor = lowRefreshFloor
	}
	return g
}

// governNote is the warn line's note while the governor holds the
// dashboard back ("" when it does not), said once per budget window.
func (m *Model) governNote() string {
	g := m.governor()
	if g.factor <= 1 && !g.pollsOff {
		return ""
	}
	every := time.Duration(m.refreshEvery() * float64(time.Second)).Round(time.Second)
	what := "refreshing every " + shortDur(every)
	if g.pollsOff {
		what += ", live polls paused"
	}
	until := ""
	if !g.until.IsZero() {
		until = " until " + g.until.Local().Format("15:04")
	}
	return fmt.Sprintf("GitHub %s budget running low — %s%s (actions are never held)", g.resource, what, until)
}

func shortDur(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
