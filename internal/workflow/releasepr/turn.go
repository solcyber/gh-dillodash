package releasepr

import (
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/keymap"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// turn is a release row's move (workflow.Turn). It is the viewer's only
// when the release stands still until they act: a deploy waiting on
// them (and not on automation), a release that needs their approval
// (not their own), or a command whose dispatch never ran. A release the
// next window will take, one in flight, one waiting on somebody else's
// environment are not.
func (w *Workflow) turn(app workflow.App, pr *model.PR) workflow.Turn {
	if pr == nil {
		return workflow.Turn{}
	}
	me := app.Me()
	dep, hasDep := pr.Get(ExtDeploy).(deploys.State)
	if pr.State == "MERGED" && hasDep {
		if due := dep.Due(); len(due) > 0 {
			t := workflow.You(keymap.S(keymap.Deploys), "approve "+strings.Join(due, ", "), "deploy", workflow.PrioDeploy)
			t.What = "its deploy"
			for _, wt := range dep.Waits {
				if wt.Due && !wt.Run.CreatedAt.IsZero() {
					t.Since = wt.Run.UpdatedAt
					break
				}
			}
			return t
		}
	}
	if p, ok := w.progressOf(pr); ok {
		switch p.Phase {
		case PhaseNotTold:
			if p.MergeSHA == "" {
				return workflow.You(keymap.S(keymap.Merge), "merge it by hand (its dispatch never ran)", "release", workflow.PrioMergeHand)
			}
			return workflow.Turn{Kind: workflow.TurnDone, What: "merged by hand"}
		case PhasePending:
			return depWaitTurn(dep, hasDep)
		case PhaseReleased:
			return workflow.Turn{Kind: workflow.TurnDone, What: "released"}
		case PhaseFailed, PhaseRefused, PhaseUnverifiable:
			return workflow.Turn{Kind: workflow.TurnFailed, What: w.progressChipText(p, "")}
		case PhaseScheduled, PhaseWaitingSlot:
			t := workflow.Others("the window")
			t.ETA = p.SlotAt
			return t
		case PhaseBumped, PhaseRolled:
			return workflow.Others("the next window")
		case PhaseHeld:
			return workflow.You(keymap.S(keymap.Approve), "release it again (a release ahead did not verify)", "release", workflow.PrioRelease)
		case PhaseLaneQueued:
			return workflow.Others("the release lane")
		case PhaseCancelled, PhaseNotRun, PhaseLost:
			return workflow.Turn{Kind: workflow.TurnDone, What: p.Phase}
		case "":
		default:
			var eta time.Time
			if b := w.budgetOf(p); b > 0 && !p.Started.IsZero() {
				eta = p.Started.Add(b)
			}
			return workflow.Running(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(w.progressChipText(p, "")), "·")), eta)
		}
	}
	if pr.State == "MERGED" {
		if !hasDep {
			return workflow.Turn{Kind: workflow.TurnDone, What: "merged"}
		}
		switch dep.Phase {
		case "running":
			return workflow.Running(dep.Text, time.Time{})
		case "waiting":
			return depWaitTurn(dep, true)
		case "stuck", "failed":
			return workflow.Turn{Kind: workflow.TurnFailed, What: dep.Text}
		}
		return workflow.Turn{Kind: workflow.TurnDone, What: dep.Text}
	}
	if w.NotARelease(pr) {
		return workflow.Turn{}
	}
	label := w.Label(pr, me, workflow.IApproved(app, pr))
	mine := me != "" && strings.EqualFold(pr.Author.Login, me)
	switch {
	case label == LabelNeeds && !mine:
		return workflow.You(keymap.S(keymap.Approve), "approve the release", "release", workflow.PrioRelease)
	case label == LabelNeeds:
		return workflow.Others("an approver (it is yours)")
	case label == model.LabelApprovedMe:
		return workflow.Others("the window (approved)")
	case pr.IsDraft, label == model.LabelChanges:
		return workflow.Others(pr.Author.Login)
	}
	return workflow.Others("the window")
}

// depWaitTurn is a merged release's deploy waiting on someone who is not
// the viewer: automation in its grace, or the environments' reviewers.
func depWaitTurn(dep deploys.State, has bool) workflow.Turn {
	switch {
	case !has || len(dep.Waits) == 0:
		return workflow.Others("an environment's reviewers")
	case dep.AllLeft():
		return workflow.Others("automation (" + dep.LeftBy() + ")")
	}
	return workflow.Others("the reviewers of " + strings.Join(dep.Envs(), ", "))
}

// nextSegs is the card's "next" line: whose move the release is and the
// key that makes it, or what happens and that nothing is the viewer's.
func (w *Workflow) nextSegs(app workflow.App, pr *model.PR) []text.Seg {
	t := w.turn(app, pr)
	dot := " " + glyphs.G("sep") + " "
	switch t.Kind {
	case workflow.TurnYou:
		return []text.Seg{text.S(glyphs.G("arrow")+" next: your move  ", "sum.attn"),
			text.S(" "+t.Key+" ", "chip.key.act"), text.S(" "+t.Verb, "keylabel.act")}
	case workflow.TurnRunning:
		what := t.What + w.lands(t.ETA)
		return []text.Seg{text.S(glyphs.G("sync")+" next: "+what+dot+"nothing for you to do", "sum.info")}
	case workflow.TurnOthers:
		return []text.Seg{text.S(glyphs.G("clock")+" next: it waits on "+t.What+dot+"not yours", "muted")}
	case workflow.TurnFailed:
		return []text.Seg{text.S(glyphs.G("err")+" "+t.What+dot+"x reads the log, X re-runs or cancels", "sum.fail")}
	case workflow.TurnDone:
		if t.What != "" {
			return []text.Seg{text.S(glyphs.G("pass")+" "+t.What, "muted")}
		}
	}
	return nil
}

// lands is ", lands ~23:20" in the window's zone ("" when unknown).
func (w *Workflow) lands(eta time.Time) string {
	if eta.IsZero() {
		return ""
	}
	at := eta.Local()
	if w.win != nil {
		at = w.win.Local().In(eta)
	}
	return ", lands ~" + at.Format("15:04")
}
