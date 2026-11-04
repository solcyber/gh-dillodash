package releasepr

// The rail is a release's progress drawn as a meter: one segment per
// run phase (setup … finishing), filling as the release moves, with a
// gold head that breathes while the current phase runs and a sweep of
// light when the phase changes. On the row it sits inside the kind chip;
// in the detail it stretches across the mission-control card, where the
// steps are listed with their clocks and Amos walks the rail (the small
// sprite, in his own colors, where the terminal can show it). Everything
// else here draws from the glyph sets and the theme's ramps, so the ASCII
// set and motion=off get a plain, honest meter.

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// railCells is the rail's width inside the row chip.
const railCells = 7

// phaseBudget is how long each run phase usually takes: the partial fill
// of the running segment is elapsed over this, capped short of full so a
// slow phase never looks done.
var phaseBudget = map[string]time.Duration{
	PhaseSetup: 40 * time.Second, PhaseValidating: 20 * time.Second, PhaseMerging: 15 * time.Second,
	PhaseApproving: 90 * time.Second, PhaseDeploying: 10 * time.Minute, PhaseVerifying: settleSecs * time.Second,
	PhaseFinishing: 60 * time.Second,
}

// settleSecs is the verify step's settle before the alarms are read.
const settleSecs = 300

// railFrac is the rail's fill, 0..1.
func railFrac(p Progress, now time.Time) float64 {
	n := float64(len(runPhases))
	switch {
	case p.Phase == PhaseReleased:
		return 1
	case p.Phase == PhasePending:
		return (float64(PhaseIndex(PhaseDeploying)) + 0.5) / n
	case p.Terminal:
		if i := PhaseIndex(p.FailPhase); i >= 0 {
			return (float64(i) + 0.5) / n
		}
		if p.Phase == PhaseRefused || p.Phase == PhaseNotRun || p.Phase == PhaseRolled || p.Phase == PhaseBumped {
			return 0
		}
		return 0.5
	}
	i := PhaseIndex(p.Phase)
	if i < 0 {
		return 0 // before the run
	}
	el := p.Elapsed(now)
	sub := 0.0
	switch {
	case p.Phase == PhaseDeploying && p.DeployTotal > 0:
		sub = float64(p.DeployDone) / float64(p.DeployTotal)
	default:
		if b := phaseBudget[p.Phase]; b > 0 {
			sub = el.Seconds() / b.Seconds()
		}
	}
	sub = math.Min(0.92, math.Max(0, sub))
	return (float64(i) + sub) / n
}

// railSegs is the rail width cells wide. live says the release moves
// (the head breathes, a sweep plays on "rail:<ident>"); f may be nil.
//
// It is drawn per phase. One cell a phase (the row chip) is a row of
// pips: filled blue for a phase done, gold for the one running, hollow
// for the rest, and a solid gold block where a release failed. Wider (the
// card) it is a stepper: one segment a phase with a gap between them, so
// it lines up with the steps listed under it, the running segment filling
// as its phase goes. Narrower than one cell a phase it is a plain meter.
func railSegs(p Progress, now time.Time, f *fx.Fx, ident string, width int, live bool) []text.Seg {
	if width <= 0 {
		return nil
	}
	n := len(runPhases)
	var segs []text.Seg
	switch {
	case width < n:
		segs = meterRail(p, now, width)
	case width < 2*n:
		segs = pipRail(p, now, width)
	default:
		segs = stepRail(p, now, width)
	}
	ambient := live && fx.Ambient && fx.Motion == "full"
	if !ambient || p.Terminal {
		return segs
	}
	// the running phase breathes gold
	beat := fx.Tri(fx.Wave(1.6, 0))
	head := fx.Ramp("fx.g", 4, 0.35+0.65*beat)
	cells := cellsOf(segs)
	for i := range cells {
		if cells[i].Style == "fx.g2" {
			cells[i].Style = head
		}
	}
	segs = joinCells(cells)
	// and a sweep of light runs the rail when the phase changes
	if f != nil {
		if t, ok := f.T("rail:" + ident); ok {
			segs = sweep(segs, width, t)
		}
	}
	return segs
}

// phaseStates is each run phase's state on the rail: done, running,
// failed (where a failure stopped it), held (a deploy waits at an
// environment), stopped (where a cancel or a lost track left it) or
// pending.
func phaseStates(p Progress) []string {
	out := make([]string, len(runPhases))
	for i := range out {
		out[i] = "pending"
	}
	fill := func(upTo int, at string) {
		for i := 0; i < upTo && i < len(out); i++ {
			out[i] = "done"
		}
		if upTo >= 0 && upTo < len(out) {
			out[upTo] = at
		}
	}
	switch {
	case p.Phase == PhaseReleased:
		fill(len(out), "")
	case p.Phase == PhasePending:
		fill(PhaseIndex(PhaseDeploying), "held")
	case p.Terminal:
		i := PhaseIndex(p.FailPhase)
		if i < 0 {
			return out // it never ran (refused, rolled, bumped, not run)
		}
		at := "failed"
		if p.Phase == PhaseCancelled || p.Phase == PhaseNotRun || p.Phase == PhaseLost {
			at = "stopped"
		}
		fill(i, at)
	default:
		if i := PhaseIndex(p.Phase); i >= 0 {
			fill(i, "running")
		}
	}
	return out
}

// phaseSub is how far the running phase is, 0..0.92: deploys done over
// deploys, else the elapsed time over the phase's usual length, capped
// short of full so a slow phase never looks done.
func phaseSub(p Progress, now time.Time) float64 {
	sub := 0.0
	switch {
	case p.Phase == PhaseDeploying && p.DeployTotal > 0:
		sub = float64(p.DeployDone) / float64(p.DeployTotal)
	default:
		if b := phaseBudget[p.Phase]; b > 0 {
			sub = p.Elapsed(now).Seconds() / b.Seconds()
		}
	}
	return math.Min(0.92, math.Max(0, sub))
}

// railStyle is a phase state's style on the rail; released fills with
// the meter's own blue.
func railStyle(p Progress, state string) string {
	switch state {
	case "done":
		if p.Phase == PhaseReleased {
			return "meter.ok"
		}
		return "fx.b5"
	case "running", "held":
		return "fx.g2"
	case "failed":
		return "hazard"
	}
	return "meter.track"
}

// pipRail is one pip a phase, the last pip taking the spare cells.
func pipRail(p Progress, now time.Time, width int) []text.Seg {
	on, off := glyphs.G("bar_on"), glyphs.G("bar_off")
	states := phaseStates(p)
	var cells []text.Seg
	for i, st := range states {
		w := 1
		if i == len(states)-1 {
			w = width - (len(states) - 1)
		}
		g := on
		switch st {
		case "pending", "stopped":
			g = off
		case "failed":
			g = glyphs.G("block")
		}
		cells = append(cells, text.S(strings.Repeat(g, w), railStyle(p, st)))
	}
	return joinCells(cells)
}

// stepRail is one segment a phase with a one-cell gap between them.
func stepRail(p Progress, now time.Time, width int) []text.Seg {
	states := phaseStates(p)
	n := len(states)
	segW := (width - (n - 1)) / n
	extra := width - (n - 1) - segW*n
	block, line := glyphs.G("block"), glyphs.G("rule")
	var out []text.Seg
	for i, st := range states {
		w := segW
		if i == n-1 {
			w += extra
		}
		switch st {
		case "done", "failed", "held":
			out = append(out, text.S(strings.Repeat(block, w), railStyle(p, st)))
		case "running":
			// the segment fills as its phase goes; its edge is the gold head
			// blocks for what is done, a gold head, a line for the rest
			edge := min(w-1, int(math.Floor(phaseSub(p, now)*float64(w))))
			out = append(out, text.S(strings.Repeat(block, edge), "fx.b5"), text.S(block, "fx.g2"),
				text.S(strings.Repeat(line, w-edge-1), "meter.track"))
		default:
			out = append(out, text.S(strings.Repeat(line, w), "meter.track"))
		}
		if i < n-1 {
			out = append(out, text.S(" ", "text"))
		}
	}
	return joinCells(out)
}

// meterRail is the plain meter of a rail too narrow for its phases.
func meterRail(p Progress, now time.Time, width int) []text.Seg {
	fill := "fx.b5"
	if p.Phase == PhaseReleased {
		fill = "meter.ok"
	}
	return fx.MeterSegs(railFrac(p, now), width, fill, "meter.track")
}

// headAt recolours the cell at the fill's edge (style "" leaves it).
func headAt(segs []text.Seg, frac float64, width int, style string) []text.Seg {
	if style == "" {
		return segs
	}
	cells := cellsOf(segs)
	i := int(math.Floor(frac * float64(width)))
	if i >= width {
		i = width - 1
	}
	if i < 0 || i >= len(cells) || frac <= 0 {
		return segs
	}
	cells[i].Style = style
	return joinCells(cells)
}

// sweep brightens the cells around a head that crosses the rail once
// during the effect (t 0..1).
func sweep(segs []text.Seg, width int, t float64) []text.Seg {
	cells := cellsOf(segs)
	head := t * float64(width+2)
	for i := range cells {
		if strings.TrimSpace(cells[i].Text) == "" {
			continue // the gaps between phases stay dark
		}
		switch d := math.Abs(float64(i) - head); {
		case d < 1:
			cells[i].Style = "fx.b7"
		case d < 2:
			cells[i].Style = "fx.b6"
		}
	}
	return joinCells(cells)
}

// cellsOf splits segments into one-cell segments (every rail glyph is
// one cell wide).
func cellsOf(segs []text.Seg) []text.Seg {
	var out []text.Seg
	for _, s := range segs {
		for _, r := range s.Text {
			out = append(out, text.S(string(r), s.Style))
		}
	}
	return out
}

// joinCells merges neighbouring cells of one style.
func joinCells(cells []text.Seg) []text.Seg {
	var out []text.Seg
	for _, c := range cells {
		if n := len(out); n > 0 && out[n-1].Style == c.Style {
			out[n-1].Text += c.Text
			continue
		}
		out = append(out, c)
	}
	return out
}

// ---- the row chip ----

// progressChipSegs is the kind chip of a release in flight: the caps, the
// rail, a spinner or a state glyph, the phase and its clock. pos -1
// measures: the clock is its widest, so the column holds still as it
// grows. compact drops the rail and the clock, for a narrow list.
func (w *Workflow) progressChipSegs(app workflow.App, pr *model.PR, p Progress, pos int, compact bool) []text.Seg {
	now := app.Now()
	style := progressStyle(p)
	live := liveProgress(p, now) && pos >= 0
	ident := fmt.Sprintf("%s#%d", pr.Repo.Name, pr.Number)
	el := deploys.FmtSecs(p.Elapsed(now))
	if pos < 0 && text.Width(el) < len(widestClock) {
		el = widestClock
	}
	segs := []text.Seg{text.S(glyphs.G("capl"), "cap:"+style)}
	label := w.progressChipText(p, el)
	if left := w.budgetLeft(p, now, pos < 0); left != "" && !compact {
		label += " · " + left
	}
	if compact {
		label = strings.TrimRight(strings.TrimSuffix(strings.TrimSpace(w.progressChipText(p, "")), "·"), " ")
	} else {
		segs = append(segs, railSegs(p, now, app.Fx(), ident, railCells, live)...)
		segs = append(segs, text.S(" ", style))
	}
	if w.nowOffered(app, pr, p) {
		if compact {
			label += " · a: NOW"
		} else {
			label += " — a: release NOW"
		}
	}
	segs = append(segs, text.S(label, style))
	if live && fx.Ambient && fx.Motion == "full" {
		switch p.Phase {
		case PhaseDispatched, PhaseWaitingSlot:
			segs = append(segs, text.S(" "+scenes.RuneAt(glyphs.G("ball"), int(fx.Now()*4)), style))
		default:
			segs = append(segs, text.S(" "+glyphs.SpinnerFrame(), style))
		}
	}
	segs = append(segs, text.S(glyphs.G("capr"), "cap:"+style))
	return segs
}

// budgetLeft is the countdown of a release in flight against
// apply_budget_mins ("12m left", "over by 3m"), or "" with no budget, no
// start, or a phase that is not running. measure is the widest it gets.
func (w *Workflow) budgetLeft(p Progress, now time.Time, measure bool) string {
	if w.cfg.ApplyBudgetMins <= 0 || p.Terminal || p.Started.IsZero() {
		return ""
	}
	switch p.Phase {
	case "", PhaseScheduled, PhaseWaitingSlot:
		return ""
	}
	if measure {
		return "over by " + widestClock
	}
	left := time.Duration(w.cfg.ApplyBudgetMins)*time.Minute - now.Sub(p.Started)
	if left < 0 {
		return "over by " + deploys.FmtSecs(-left)
	}
	return deploys.FmtSecs(left) + " left"
}

// nowOffered reports a release the window left behind (bumped or
// rolled, so not tonight) that is still open and approved by me: its
// approval stands, so a offers release NOW, and the chip says so.
func (w *Workflow) nowOffered(app workflow.App, pr *model.PR, p Progress) bool {
	if p.Phase != PhaseBumped && p.Phase != PhaseRolled {
		return false
	}
	if pr.State != "" && pr.State != "OPEN" {
		return false
	}
	return workflow.IApproved(app, pr)
}

// widestClock is the widest clock a chip is measured with (deploys.FmtSecs
// up to 99h59m).
const widestClock = "00h00m"

// kindChip is the kind chip's segments for a release row (pos -1 for a
// width measurement: no live glyphs). compact drops a release in
// flight's rail.
func (w *Workflow) kindChip(app workflow.App, pr *model.PR, pos int, compact bool) []text.Seg {
	if p, ok := w.progressOf(pr); ok {
		segs := w.progressChipSegs(app, pr, p, pos, compact)
		if pos < 0 {
			// the measured width leaves room for the live glyph
			segs = append(segs, text.S("  ", "text"))
		}
		return segs
	}
	ktext := w.kindChipText(pr)
	return scenes.Chip(ktext, w.kindChipStyle(pr, ktext))
}

// notePhaseChange starts the row's effects when a release's phase moved:
// the rail's sweep and the number's pulse.
func notePhaseChange(f *fx.Fx, pr *model.PR, from, to string) {
	if f == nil || from == to || to == "" {
		return
	}
	ident := fmt.Sprintf("%s#%d", pr.Repo.Name, pr.Number)
	f.Start("rail:"+ident, 0.9, nil)
	f.Start("upd:"+ident, 1.2, nil)
}

// ---- the card ----

// phaseRow is one row of the card's timeline.
type phaseRow struct {
	phase    string
	state    string // done | running | pending | failed | skipped
	elapsed  time.Duration
	timed    bool // a step gave the clock (an inferred phase has none)
	tail     []text.Seg
	stepName string
}

// timelineRows folds the steps into one row per run phase.
func (w *Workflow) timelineRows(p Progress, now time.Time) []phaseRow {
	var rows []phaseRow
	for _, ph := range runPhases {
		r := phaseRow{phase: ph, state: "pending"}
		seen := false
		allSkipped := true
		for _, s := range p.Steps {
			if s.Phase != ph {
				continue
			}
			seen = true
			r.timed = true
			if s.Conclusion != "skipped" {
				allSkipped = false
			}
			switch {
			case s.Status == "in_progress":
				r.state, r.stepName = "running", s.Name
				r.elapsed += span(s.StartedAt, now)
			case s.Status == "completed" && (s.Conclusion == "failure" || s.Conclusion == "timed_out"):
				r.state = "failed"
				r.elapsed += span(s.StartedAt, s.CompletedAt)
			case s.Status == "completed" && s.Conclusion == "skipped":
				// a step not needed this time
			case s.Status == "completed":
				if r.state == "pending" {
					r.state = "done"
				}
				r.elapsed += span(s.StartedAt, s.CompletedAt)
			}
		}
		if seen && allSkipped {
			r.state = "skipped"
		}
		if !seen && PhaseIndex(p.Phase) > PhaseIndex(ph) && !p.Terminal {
			r.state = "done" // a phase the job ran without a named step
		}
		if !seen && p.Phase == ph && !p.Terminal {
			r.state, r.elapsed, r.timed = "running", p.Elapsed(now), true
		}
		if p.Phase == PhaseReleased && r.state == "pending" {
			r.state = "done"
		}
		// merged, the deploy held at an environment: everything before
		// the deploy happened
		if p.Phase == PhasePending && r.state == "pending" && PhaseIndex(ph) < PhaseIndex(PhaseDeploying) {
			r.state = "done"
		}
		// the technical tail
		switch ph {
		case PhaseMerging:
			if r.state == "done" && p.MergeSHA != "" {
				r.tail = []text.Seg{text.S(model.ShortSHA(p.MergeSHA), "md.inline"), text.S(" on "+w.cfg.ProductionBranch, "faint")}
			}
		case PhaseDeploying:
			if p.DeployTotal > 0 {
				done := fmt.Sprintf("%d/%d runs", p.DeployDone, p.DeployTotal)
				st := "faint"
				if p.DeployDone == p.DeployTotal {
					st, done = "ci.pass", done+" "+glyphs.G("pass")
				}
				r.tail = []text.Seg{text.S(done, st)}
			}
		case PhaseVerifying:
			if r.state == "running" {
				r.tail = settleSegs(r.elapsed)
			} else if r.state == "done" {
				r.tail = []text.Seg{text.S("alarms quiet", "faint")}
			}
		case PhaseValidating:
			if r.state == "done" && p.Approver != "" {
				r.tail = []text.Seg{text.S("approver "+p.Approver, "faint")}
			}
		}
		if r.state == "running" && r.stepName != "" && len(r.tail) == 0 {
			r.tail = []text.Seg{text.S(r.stepName, "faint")}
		}
		rows = append(rows, r)
	}
	return rows
}

// span is b-a, or 0 when either end is unknown (a step not started, or
// one GitHub has not stamped yet).
func span(a, b time.Time) time.Duration {
	if a.IsZero() || b.IsZero() || b.Before(a) {
		return 0
	}
	return b.Sub(a)
}

// settleSegs is the verify step's settle countdown: a shaded meter that
// drains, then the alarms read.
func settleSegs(el time.Duration) []text.Seg {
	left := settleSecs*time.Second - el
	if left <= 0 {
		return []text.Seg{text.S("settled; reading the alarms", "faint")}
	}
	width := 12
	frac := 1 - float64(left)/float64(settleSecs*time.Second)
	shade := []rune(glyphs.G("shade"))
	var b strings.Builder
	filled := int(math.Round(frac * float64(width)))
	for i := 0; i < width; i++ {
		switch {
		case i < filled-1:
			b.WriteRune(shade[len(shade)-1])
		case i < filled:
			b.WriteRune(shade[len(shade)/2])
		default:
			b.WriteRune(shade[0])
		}
	}
	return []text.Seg{text.S("settle ", "faint"), text.S(b.String(), "fx.b4"), text.S(" "+fmtClock(left)+" left", "faint")}
}

// fmtClock is m:ss.
func fmtClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// stateGlyph is the timeline glyph and style for a row state.
func stateGlyph(state string) (string, string) {
	switch state {
	case "done":
		return glyphs.G("pass"), "ci.pass"
	case "running":
		return glyphs.SpinnerFrame(), "ci.run"
	case "failed":
		return glyphs.G("fail"), "ci.fail"
	case "skipped":
		return glyphs.G("skip"), "ci.skip"
	}
	return glyphs.G("wait"), "ci.wait"
}

// amosMinHeight is the screen height from which the card has room for
// Amos above the rail and every step under it.
const amosMinHeight = 32

// nameCol is the card's phase-name column: the longest phase name
// ("waiting for slot") and a gap.
const nameCol = 17

// progressCard is the mission-control block of the detail card: the
// header with the T+ clock, the rail (Amos on it), the timeline, the
// outcome and the run.
func (w *Workflow) progressCard(app workflow.App, pr *model.PR, p Progress, width, inner int) []text.Line {
	now := app.Now()
	live := liveProgress(p, now)
	var out []text.Line
	// header: state chip, run, clock
	head := []text.Seg{text.S(glyphs.Ico("live"), "plan.live")}
	head = append(head, scenes.Chip(w.progressChip(p, now), progressStyle(p))...)
	switch {
	case p.RunID != 0:
		run := fmt.Sprintf("  run %d", p.RunID)
		if p.RunAttempt > 1 {
			run += fmt.Sprintf(" · attempt %d", p.RunAttempt)
		}
		head = append(head, text.S(run, "faint"))
	case p.Night != "":
		head = append(head, text.S("  night "+p.Night, "faint"))
		if !p.SlotAt.IsZero() && w.win != nil {
			head = append(head, text.S(" · slot "+w.win.Local().TAt(p.SlotAt), "faint"))
		}
	}
	if !p.Started.IsZero() {
		at := now
		if p.Terminal {
			at = p.UpdatedAt
			if at.Before(p.Started) {
				at = now
			}
		}
		clock := "T+" + fmtClock(at.Sub(p.Started))
		if p.Phase == PhaseScheduled && !p.SlotAt.IsZero() && now.Before(p.SlotAt) {
			clock = "T-" + fmtClock(p.SlotAt.Sub(now))
		}
		cstyle := "strong"
		if live && fx.Ambient && fx.Motion == "full" {
			cstyle = fx.Ramp("hdr.logo.", 3, fx.Tri(fx.Wave(3.0, 0)))
		}
		// the apply budget's countdown rides after the clock
		var budget []text.Seg
		if left := w.budgetLeft(p, now, false); left != "" {
			bstyle := "faint"
			if strings.HasPrefix(left, "over") {
				bstyle = "budget.low"
			}
			budget = []text.Seg{text.S("  "+glyphs.Ico("budget")+left, bstyle)}
		}
		if gap := inner - 4 - text.SegsWidth(head) - text.Width(clock) - text.SegsWidth(budget); gap > 1 {
			head = append(head, text.S(strings.Repeat(" ", gap), "text"))
		} else {
			head = append(head, text.S("  ", "text"))
		}
		head = append(head, text.S(clock, cstyle))
		head = append(head, budget...)
	}
	out = append(out, scenes.CardLines(head, width, inner)...)
	// Amos walks the rail, just above it
	ident := fmt.Sprintf("%s#%d", pr.Repo.Name, pr.Number)
	railW := max(1, inner-4)
	f := app.Fx()
	// Amos walks the rail only where the card has the rows to spare: on
	// a short screen the steps come first
	if inner >= 60 && scenes.Mascot != "none" && app.Height() >= amosMinHeight {
		for _, ln := range amosOnRail(p, railFrac(p, now), railW, live) {
			out = append(out, scenes.CardLines(ln, width, inner)...)
		}
	}
	rail := []text.Seg{text.S("  ", "text")}
	rail = append(rail, railSegs(p, now, f, ident, railW, live)...)
	out = append(out, scenes.CardLines(rail, width, inner)...)
	// before the run: where it stands
	if PhaseIndex(p.Phase) < 0 && !p.Terminal {
		g, st := stateGlyph("running")
		line := []text.Seg{text.S("  "+g+" ", st), text.S(text.Pad(p.Phase, nameCol), "text"), text.S(fmtClock(p.Elapsed(now)), "muted")}
		if p.Detail != "" {
			line = append(line, text.S("  "+p.Detail, "faint"))
		}
		out = append(out, scenes.CardLines(line, width, inner)...)
	}
	// the timeline
	for _, r := range w.timelineRows(p, now) {
		g, st := stateGlyph(r.state)
		nst, cst := "text", "muted"
		switch r.state {
		case "pending":
			nst = "faint"
		case "running":
			nst, cst = "strong", "ci.run" // the step being worked on
		case "failed":
			nst, cst = "approve.refused", "ci.fail"
		}
		line := []text.Seg{text.S("  "+g+" ", st), text.S(text.Pad(r.phase, nameCol), nst)}
		if r.timed && r.state != "pending" && r.state != "skipped" {
			line = append(line, text.S(text.Pad(fmtClock(r.elapsed), 6), cst))
		} else {
			line = append(line, text.S(strings.Repeat(" ", 6), "text"))
		}
		if r.state == "skipped" {
			line = append(line, text.S("skipped", "faint"))
		}
		line = append(line, r.tail...)
		out = append(out, scenes.CardLines(line, width, inner)...)
	}
	// the outcome
	if p.Terminal {
		st := "toast.warn"
		word := "outcome: " + p.Phase
		if p.Phase == PhaseReleased {
			st, word = "rev.ok", "outcome: verified"
		}
		if p.Outcome != "" && p.Outcome != "verified" {
			word = "outcome: " + strings.ReplaceAll(p.Outcome, "_", " ")
		}
		line := []text.Seg{text.S("  "+word, st)}
		if p.Detail != "" {
			line = append(line, text.S(" — "+p.Detail, "muted"))
		}
		out = append(out, scenes.CardLines(line, width, inner)...)
		if p.Phase == PhasePending {
			hint := fmt.Sprintf("  D approves the waiting environment (if you review it); then swap %s for %s by hand", w.pendingLabel(), w.appliedLabel())
			out = append(out, scenes.CardLines([]text.Seg{text.S(hint, "sum.attn")}, width, inner)...)
		}
		if p.MergeSHA != "" {
			out = append(out, scenes.CardLines([]text.Seg{text.S("  merge: ", "faint"), text.S(model.ShortSHA(p.MergeSHA), "md.inline")}, width, inner)...)
		}
	}
	if p.RunURL != "" {
		out = append(out, scenes.CardLines([]text.Seg{text.S("  run  ", "faint"), text.S(p.RunURL, "md.link")}, width, inner)...)
	}
	if p.PollErr != "" {
		out = append(out, scenes.CardLines([]text.Seg{text.S("  last poll failed: "+p.PollErr, "faint")}, width, inner)...)
	}
	return out
}

// Amos in line art, for where the small sprite cannot draw (the ASCII
// glyphs, mascot = "ascii", 16 colors or none): two rows, facing right.
var (
	amosSmall     = []string{`  .-==-.  `, `(__\_\_(o>`}
	amosSmallStep = []string{`  .-==-.  `, `(_/\_/_(o>`}
)

// amosOnRail is Amos at the rail's fill, walking while live, with a
// spark over his head once released and sitting still once it ends. The
// small sprite keeps his colors through a failure (the striped rail says
// it); the line art goes gold for a release and gray for the rest.
func amosOnRail(p Progress, frac float64, railW int, live bool) [][]text.Seg {
	ambient := live && fx.Ambient && fx.Motion == "full"
	step := ambient && int(fx.Now()*4)%2 == 1
	var rows [][]text.Seg
	var aw int
	if scenes.MiniAmos(theme.Current()) {
		o := amos.Options{Mini: true}
		if step {
			o.Frame = 1
		}
		aw, _ = amos.Size(o)
		rows = amos.Segs(o)
	} else {
		art := amosSmall
		if step {
			art = amosSmallStep
		}
		style := "art"
		switch {
		case p.Phase == PhaseReleased:
			style = "fx.g2"
		case p.Terminal:
			style = "fx.k2"
		}
		aw = len([]rune(art[0]))
		for _, ln := range art {
			rows = append(rows, []text.Seg{text.S(ln, style)})
		}
	}
	x := 2 + int(math.Round(frac*float64(max(0, railW-aw))))
	pad := text.S(strings.Repeat(" ", x), "text")
	out := make([][]text.Seg, 0, len(rows)+1)
	if p.Phase == PhaseReleased {
		spark := scenes.RuneAt(glyphs.G("spark"), 3+int(fx.Now())%2)
		if fx.Motion != "full" {
			spark = scenes.RuneAt(glyphs.G("spark"), 3)
		}
		out = append(out, []text.Seg{text.S(strings.Repeat(" ", x+aw-3)+spark, "fx.g3")})
	}
	for _, r := range rows {
		out = append(out, append([]text.Seg{pad}, r...))
	}
	return out
}
