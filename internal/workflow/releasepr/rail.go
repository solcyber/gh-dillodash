package releasepr

// The rail is a release's progress drawn as a meter: one segment per
// run phase (setup … finishing), filling as the release moves, with a
// gold head that breathes while the current phase runs and a sweep of
// light when the phase changes. On the row it sits inside the kind chip;
// in the detail it stretches across the mission-control card, where the
// steps are listed with their clocks and Amos walks the rail. Everything
// here draws from the glyph sets and the theme's ramps, so the ASCII set
// and motion=off get a plain, honest meter.

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
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
	case p.Terminal:
		if i := PhaseIndex(p.FailPhase); i >= 0 {
			return (float64(i) + 0.5) / n
		}
		if p.Phase == PhaseRefused || p.Phase == PhaseNotRun || p.Phase == PhaseRolled {
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
func railSegs(p Progress, now time.Time, f *fx.Fx, ident string, width int, live bool) []text.Seg {
	if width <= 0 {
		return nil
	}
	if p.Terminal && p.Phase != PhaseReleased {
		return hazardSegs(p, width)
	}
	frac := railFrac(p, now)
	fill, track := "fx.b5", "meter.track"
	if p.Phase == PhaseReleased {
		fill = "meter.ok"
	}
	segs := fx.MeterSegs(frac, width, fill, track)
	ambient := live && fx.Ambient && fx.Motion == "full"
	if !ambient {
		if p.Phase == PhaseReleased {
			return segs
		}
		// a still frame: the head cell is the one being worked on
		return headAt(segs, frac, width, "fx.g2")
	}
	// the head breathes gold while its phase runs
	beat := fx.Tri(fx.Wave(1.6, 0))
	segs = headAt(segs, frac, width, fx.Ramp("fx.g", 4, 0.35+0.65*beat))
	// and a sweep of light runs the rail when the phase changes
	if f != nil {
		if t, ok := f.T("rail:" + ident); ok {
			segs = sweep(segs, width, t)
		}
	}
	return segs
}

// headAt recolours the cell at the fill's edge.
func headAt(segs []text.Seg, frac float64, width int, style string) []text.Seg {
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
		switch d := math.Abs(float64(i) - head); {
		case d < 1:
			cells[i].Style = "fx.b7"
		case d < 2:
			cells[i].Style = "fx.b6"
		}
	}
	return joinCells(cells)
}

// hazardSegs is the rail of a failed or refused release: stripes.
func hazardSegs(p Progress, width int) []text.Seg {
	stripe := glyphs.G("hazard")
	if stripe == "" {
		stripe = "/\\"
	}
	var b strings.Builder
	rs := []rune(stripe)
	for i := 0; i < width; i++ {
		b.WriteRune(rs[i%len(rs)])
	}
	style := "hazard"
	if p.Phase == PhaseCancelled || p.Phase == PhaseNotRun || p.Phase == PhaseLost || p.Phase == PhaseRolled {
		style = "meter.track"
	}
	return []text.Seg{text.S(b.String(), style)}
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
// rail, a spinner or a state glyph, the phase and its clock.
func (w *Workflow) progressChipSegs(app workflow.App, pr *model.PR, p Progress, pos int) []text.Seg {
	now := app.Now()
	style := progressStyle(p)
	live := liveProgress(p, now) && pos >= 0
	ident := fmt.Sprintf("%s#%d", pr.Repo.Name, pr.Number)
	var f *fx.Fx
	if app != nil {
		f = app.Fx()
	}
	segs := []text.Seg{text.S(glyphs.G("capl"), "cap:"+style)}
	segs = append(segs, railSegs(p, now, f, ident, railCells, live)...)
	segs = append(segs, text.S(" "+w.progressChip(p, now), style))
	if live && fx.Ambient && fx.Motion == "full" {
		switch p.Phase {
		case PhaseDispatched, PhaseWaitingSlot:
			segs = append(segs, text.S(" "+runeAt(glyphs.G("ball"), int(fx.Now()*4)), style))
		default:
			segs = append(segs, text.S(" "+glyphs.SpinnerFrame(), style))
		}
	}
	segs = append(segs, text.S(glyphs.G("capr"), "cap:"+style))
	return segs
}

// runeAt is the i'th rune of s, cycling; "?" when s is empty.
func runeAt(s string, i int) string {
	rs := []rune(s)
	if len(rs) == 0 {
		return "?"
	}
	if i < 0 {
		i = -i
	}
	return string(rs[i%len(rs)])
}

// kindChip is the kind chip's segments for a release row (pos -1 for a
// width measurement: no live glyphs).
func (w *Workflow) kindChip(app workflow.App, pr *model.PR, pos int) []text.Seg {
	if p, ok := w.progressOf(pr); ok {
		segs := w.progressChipSegs(app, pr, p, pos)
		if pos < 0 {
			// the measured width leaves room for the live glyph
			segs = append(segs, text.S("  ", "text"))
		}
		return segs
	}
	ktext := w.kindChipText(pr, app.Now())
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
	tail     []text.Seg
	stepName string
}

// timelineRows folds the steps into one row per run phase.
func (w *Workflow) timelineRows(pr *model.PR, p Progress, now time.Time) []phaseRow {
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
			switch {
			case s.Status == "in_progress":
				r.state, r.stepName = "running", s.Name
				r.elapsed += now.Sub(s.StartedAt)
				allSkipped = false
			case s.Status == "completed" && (s.Conclusion == "failure" || s.Conclusion == "timed_out"):
				r.state = "failed"
				r.elapsed += s.CompletedAt.Sub(s.StartedAt)
				allSkipped = false
			case s.Status == "completed" && s.Conclusion == "skipped":
				// a step not needed this time
			case s.Status == "completed":
				if r.state == "pending" {
					r.state = "done"
				}
				if !s.CompletedAt.IsZero() {
					r.elapsed += s.CompletedAt.Sub(s.StartedAt)
				}
				allSkipped = false
			}
		}
		if seen && allSkipped {
			r.state = "skipped"
		}
		if !seen && PhaseIndex(p.Phase) > PhaseIndex(ph) && !p.Terminal {
			r.state = "done" // a phase the job ran without a named step
		}
		if !seen && p.Phase == ph && !p.Terminal {
			r.state, r.elapsed = "running", p.Elapsed(now)
		}
		if p.Phase == PhaseReleased && r.state == "pending" {
			r.state = "done"
		}
		if p.Terminal && p.Phase != PhaseReleased && r.state == "pending" {
			r.state = "pending"
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
	if len(shade) == 0 {
		shade = []rune(":|#")
	}
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
		cstyle := "muted"
		if live && fx.Ambient && fx.Motion == "full" {
			cstyle = fx.Ramp("hdr.logo.", 3, fx.Tri(fx.Wave(3.0, 0)))
		}
		if gap := inner - text.SegsWidth(head) - text.Width(clock) - 1; gap > 1 {
			head = append(head, text.S(strings.Repeat(" ", gap), "text"))
		} else {
			head = append(head, text.S("  ", "text"))
		}
		head = append(head, text.S(clock, cstyle))
	}
	out = append(out, scenes.CardLines(head, width, inner)...)
	// Amos walks the rail (two rows above it)
	ident := fmt.Sprintf("%s#%d", pr.Repo.Name, pr.Number)
	railW := max(10, inner-4)
	var f *fx.Fx
	if app != nil {
		f = app.Fx()
	}
	if inner >= 60 && scenes.Mascot != "none" {
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
		line := []text.Seg{text.S("  "+g+" ", st), text.S(text.Pad(p.Phase, 15), "text"), text.S(fmtClock(p.Elapsed(now)), "muted")}
		if p.Detail != "" {
			line = append(line, text.S("  "+p.Detail, "faint"))
		}
		out = append(out, scenes.CardLines(line, width, inner)...)
	}
	// the timeline
	for _, r := range w.timelineRows(pr, p, now) {
		g, st := stateGlyph(r.state)
		name := r.phase
		if r.phase == PhaseApproving {
			name = "approving"
		}
		line := []text.Seg{text.S("  "+g+" ", st), text.S(text.Pad(name, 12), map[bool]string{true: "text", false: "faint"}[r.state != "pending"])}
		if r.state != "pending" && r.state != "skipped" {
			line = append(line, text.S(text.Pad(fmtClock(r.elapsed), 6), "muted"))
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
		if p.MergeSHA != "" {
			out = append(out, scenes.CardLines([]text.Seg{text.S("  merge: ", "faint"), text.S(model.ShortSHA(p.MergeSHA), "md.inline")}, width, inner)...)
		}
	}
	if p.RunURL != "" {
		out = append(out, scenes.CardLines([]text.Seg{text.S("  run  ", "faint"), text.S(p.RunURL, "md.link")}, width, inner)...)
	}
	if p.PollErr != "" {
		out = append(out, scenes.CardLines([]text.Seg{text.S("  last poll failed: "+p.PollErr+" — retrying", "faint")}, width, inner)...)
	}
	return out
}

// Amos, small enough to walk a card: two rows, facing right.
var (
	amosSmall     = []string{`  .-==-.  `, `(__\_\_(o>`}
	amosSmallStep = []string{`  .-==-.  `, `(_/\_/_(o>`}
)

// amosOnRail is Amos at the rail's fill, walking while live, with a
// spark over his head once released and sitting still on a failure.
func amosOnRail(p Progress, frac float64, railW int, live bool) [][]text.Seg {
	art := amosSmall
	style := "art"
	ambient := live && fx.Ambient && fx.Motion == "full"
	if ambient && int(fx.Now()*4)%2 == 1 {
		art = amosSmallStep
	}
	switch {
	case p.Phase == PhaseReleased:
		style = "fx.g2"
	case p.Terminal:
		style = "fx.k2"
	}
	aw := len([]rune(art[0]))
	x := 2 + int(math.Round(frac*float64(max(0, railW-aw))))
	rows := make([][]text.Seg, 0, 3)
	if p.Phase == PhaseReleased {
		spark := runeAt(glyphs.G("spark"), 3+int(fx.Now())%2)
		if fx.Motion != "full" {
			spark = runeAt(glyphs.G("spark"), 3)
		}
		rows = append(rows, []text.Seg{text.S(strings.Repeat(" ", x+aw-3)+spark, "fx.g3")})
	}
	for _, ln := range art {
		rows = append(rows, []text.Seg{text.S(strings.Repeat(" ", x)+ln, style)})
	}
	return rows
}
