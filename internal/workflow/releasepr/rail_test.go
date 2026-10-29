package releasepr

import (
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// flatLines joins a card's lines for substring checks.
func flatLines(lines []text.Line) string {
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(text.SegsText(ln.Segs))
		b.WriteString("\n")
	}
	return b.String()
}

func TestRailFrac(t *testing.T) {
	now := utc(3, 10)
	cases := []struct {
		name string
		p    Progress
		lo   float64
		hi   float64
	}{
		{"dispatched", Progress{Phase: PhaseDispatched, Since: now}, 0, 0},
		{"setup just started", Progress{Phase: PhaseSetup, Since: now}, 0, 0.01},
		{"merging a while", Progress{Phase: PhaseMerging, Since: now.Add(-time.Hour)}, 2.9 / 7, 2.93 / 7},
		{"deploying 3/9", Progress{Phase: PhaseDeploying, Since: now, DeployDone: 3, DeployTotal: 9}, (4 + 1.0/3) / 7, (4 + 1.0/3) / 7},
		{"verifying halfway", Progress{Phase: PhaseVerifying, Since: now.Add(-150 * time.Second)}, 5.49 / 7, 5.51 / 7},
		{"released", Progress{Phase: PhaseReleased, Terminal: true}, 1, 1},
		{"failed at verifying", Progress{Phase: PhaseFailed, Terminal: true, FailPhase: PhaseVerifying}, 5.5 / 7, 5.5 / 7},
		{"refused", Progress{Phase: PhaseRefused, Terminal: true}, 0, 0},
	}
	for _, c := range cases {
		got := railFrac(c.p, now)
		if got < c.lo-1e-9 || got > c.hi+1e-9 {
			t.Errorf("%s: %.4f, want %.4f..%.4f", c.name, got, c.lo, c.hi)
		}
	}
}

// TestRailSegsWidth: the rail is exactly as wide as asked in every glyph
// set, still or alive; a failure is stripes; the sweep lights the head.
func TestRailSegsWidth(t *testing.T) {
	now := utc(3, 10)
	wasM, wasA, wasN := fx.Motion, fx.Ambient, fx.Now
	t.Cleanup(func() { fx.Motion, fx.Ambient, fx.Now = wasM, wasA, wasN; glyphs.Use(glyphs.Nerd) })
	fx.Now = func() float64 { return 100 }
	ps := []Progress{
		{Phase: PhaseDispatched, Since: now},
		{Phase: PhaseMerging, Since: now.Add(-5 * time.Second)},
		{Phase: PhaseDeploying, Since: now.Add(-time.Minute), DeployDone: 6, DeployTotal: 9},
		{Phase: PhaseReleased, Terminal: true},
		{Phase: PhaseFailed, Terminal: true, FailPhase: PhaseDeploying},
		{Phase: PhaseLost, Terminal: true},
	}
	for _, set := range []glyphs.Set{glyphs.Nerd, glyphs.Unicode, glyphs.ASCII} {
		glyphs.Use(set)
		for _, motion := range []string{"off", "full"} {
			fx.Motion, fx.Ambient = motion, motion == "full"
			f := fx.New()
			f.Start("rail:web-portal#42", 0.9, nil)
			for _, p := range ps {
				for _, w := range []int{7, 24, 60} {
					segs := railSegs(p, now, f, "web-portal#42", w, true)
					if got := text.SegsWidth(segs); got != w {
						t.Errorf("%s/%s %s width %d: got %d %q", set, motion, p.Phase, w, got, text.SegsText(segs))
					}
				}
			}
		}
	}
	glyphs.Use(glyphs.Unicode)
	fx.Motion, fx.Ambient = "off", false
	if s := text.SegsText(railSegs(ps[4], now, nil, "x", 7, false)); s != "▚▞▚▞▚▞▚" {
		t.Errorf("a failure is stripes: %q", s)
	}
	if segs := railSegs(ps[3], now, nil, "x", 7, false); len(segs) != 1 || segs[0].Style != "meter.ok" || segs[0].Text != "███████" {
		t.Errorf("released is a full rail: %+v", segs)
	}
	segs := railSegs(ps[2], now, nil, "x", 7, false)
	if len(segs) < 2 || segs[0].Style != "fx.b5" {
		t.Errorf("a still rail fills blue with a gold head: %+v", segs)
	}
	fx.Motion, fx.Ambient = "full", true
	f := fx.New()
	fx.Now = func() float64 { return 100 }
	f.Start("rail:x", 0.9, nil)
	fx.Now = func() float64 { return 100.45 } // mid-sweep
	bright := false
	for _, s := range railSegs(ps[1], now, f, "x", 24, true) {
		if s.Style == "fx.b7" || s.Style == "fx.b6" {
			bright = true
		}
	}
	if !bright {
		t.Error("the sweep lights cells while it plays")
	}
}

// TestKindChipWidthMatchesLayout: the measured chip is at least as wide
// as the drawn one, in flight or not, so the column never clips.
func TestKindChipWidthMatchesLayout(t *testing.T) {
	w, app, pr := pollFixture(t)
	listed := w.Current().Find(pr.Ident())
	wasM, wasA := fx.Motion, fx.Ambient
	t.Cleanup(func() { fx.Motion, fx.Ambient = wasM, wasA })
	for _, motion := range []string{"off", "full"} {
		fx.Motion, fx.Ambient = motion, motion == "full"
		measured := text.SegsWidth(w.kindChip(app, listed, -1))
		drawn := text.SegsWidth(w.kindChip(app, listed, 3))
		if drawn > measured || measured == 0 {
			t.Errorf("%s: drawn %d measured %d", motion, drawn, measured)
		}
		if s := text.SegsText(w.kindChip(app, listed, 3)); !strings.Contains(s, "dispatched") {
			t.Errorf("%s: chip text %q", motion, s)
		}
	}
	plain := makeRelease(relOpts{number: 999})
	if s := text.SegsText(w.kindChip(app, plain, 0)); !strings.Contains(s, "release") {
		t.Errorf("a quiet release keeps its kind chip: %q", s)
	}
}

// TestProgressCardTimeline: the mission-control block lists one row per
// run phase with its clock and glyph, keeps every line exactly as wide as
// the card, shows the run and the outcome, and seats Amos on wide cards.
func TestProgressCardTimeline(t *testing.T) {
	w, app, pr := pollFixture(t)
	wasMascot := scenes.Mascot
	t.Cleanup(func() { scenes.Mascot = wasMascot })
	scenes.Mascot = "sprite"
	run := runAt(utcS(2, 59, 40), "in_progress", "")
	job := jobAt(9, utcS(2, 59, 50), 1) // verifying
	merged := w.Current().Find(pr.Ident())
	w.mu.Lock()
	tr := w.progress[pr.Ident()]
	tr.run, tr.job = run, job
	merged.State = "MERGED"
	merged.MergeCommit = &model.MergeCommit{Oid: mergedSHA}
	merged.Set(ExtDeploy, deploys.State{Phase: "done", Runs: []deploys.Row{{Style: "chip.approved"}, {Style: "chip.approved"}}})
	clock.Now = func() time.Time { return utcS(3, 2, 30) }
	tr.cur = w.derive(merged, tr, clock.Now())
	w.mu.Unlock()
	widths := func(lines []text.Line, width int) {
		t.Helper()
		for _, ln := range lines {
			if got := text.SegsWidth(ln.Segs); got != width {
				t.Errorf("line width %d, want %d: %q", got, width, text.SegsText(ln.Segs))
			}
		}
	}
	lines := w.DetailCards(app, merged, merged, 90)
	widths(lines, 90)
	card := flatLines(lines)
	for _, want := range []string{"verifying 2m", "run 91", "T+3:30", "setup", "validating", "merging", "approving", "deploying", "2/2 runs", "settle", "left", "finishing", "https://github.com/acme/infra/actions/runs/91", "(o>", "skipped"} {
		if !strings.Contains(card, want) {
			t.Errorf("missing %q in:\n%s", want, card)
		}
	}
	if strings.Count(card, "skipped") != 1 {
		t.Errorf("only the approving phase is skipped; queued phases are pending:\n%s", card)
	}
	if strings.Count(card, "setup") != 1 {
		t.Errorf("the setup steps fold into one row:\n%s", card)
	}
	// a narrow card has no room for Amos
	narrow := flatLines(w.DetailCards(app, merged, merged, 50))
	widths(w.DetailCards(app, merged, merged, 50), 50)
	if strings.Contains(narrow, "(o>") {
		t.Errorf("no Amos under 60 columns:\n%s", narrow)
	}
	// motion off and ASCII glyphs still draw a complete card
	wasM := fx.Motion
	t.Cleanup(func() { fx.Motion = wasM; glyphs.Use(glyphs.Nerd) })
	fx.Motion = "off"
	glyphs.Use(glyphs.ASCII)
	widths(w.DetailCards(app, merged, merged, 72), 72)
	glyphs.Use(glyphs.Nerd)
	// terminal: the outcome and the merge, Amos glowing at the end
	w.mu.Lock()
	tr.comments = append(tr.comments, comment(utc(3, 5), releasedBody))
	clock.Now = func() time.Time { return utc(3, 6) }
	tr.cur = w.derive(merged, tr, clock.Now())
	w.mu.Unlock()
	done := w.DetailCards(app, merged, merged, 90)
	widths(done, 90)
	card = flatLines(done)
	for _, want := range []string{"released", "outcome: verified", "deploy green, alarms quiet", "merge: abc123d", "T+6:00"} {
		if !strings.Contains(card, want) {
			t.Errorf("missing %q in:\n%s", want, card)
		}
	}
	// before any run: the pre-run line says where it stands
	fresh := approved(makeRelease(relOpts{number: 77}), "alice")
	w.mu.Lock()
	w.progress[fresh.Ident()] = &track{repo: portal, number: 77, firstSeen: utc(3, 0), nowPostedAt: utc(3, 0)}
	w.progress[fresh.Ident()].cur = w.derive(fresh, w.progress[fresh.Ident()], utc(3, 1))
	w.mu.Unlock()
	fresh.Set(ExtRel, &Info{Kind: KindRelease, Repo: portal.Name})
	pre := flatLines(w.DetailCards(app, fresh, nil, 80))
	if !strings.Contains(pre, "dispatched") || !strings.Contains(pre, "T+6:00") || strings.Count(pre, "T+6:00") != 1 {
		t.Errorf("the pre-run row:\n%s", pre)
	}
	_ = workflow.Row{}
}
