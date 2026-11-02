package releasepr

import (
	"fmt"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// ---- the lane: a manual night's order (lane_order) ----

// laneIndex is repo's place in lane_order, or -1.
func (w *Workflow) laneIndex(repo model.Repo) int {
	for i, name := range w.cfg.LaneOrder {
		if strings.EqualFold(name, repo.Name) {
			return i
		}
	}
	return -1
}

// laneBusy is another release in pr's lane that is still on its way
// (posted, running or deploying), or nil: the lane runs one release at
// a time, so NOW on the next one waits for it.
func (w *Workflow) laneBusy(pr *model.PR) *model.PR {
	if w.laneIndex(pr.Repo) < 0 {
		return nil
	}
	st := w.Current()
	if st == nil {
		return nil
	}
	for _, other := range st.Releases {
		if other.Ident() == pr.Ident() || w.laneIndex(other.Repo) < 0 {
			continue
		}
		if w.inFlight(other) || deploying(other) {
			return other
		}
	}
	return nil
}

// deploying reports a merged release whose deploy runs on its merge
// commit are not settled yet.
func deploying(pr *model.PR) bool {
	if pr.State != "MERGED" {
		return false
	}
	dep, ok := pr.Get(ExtDeploy).(deploys.State)
	return ok && (dep.Phase == "running" || dep.Phase == "waiting" || dep.Phase == "stuck")
}

// nextInLane is the open, approved, non-draft release of the first repo
// after pr's in lane_order that has one, or nil.
func (w *Workflow) nextInLane(pr *model.PR) *model.PR {
	i := w.laneIndex(pr.Repo)
	st := w.Current()
	if i < 0 || st == nil {
		return nil
	}
	for _, name := range w.cfg.LaneOrder[i+1:] {
		for _, cand := range st.Releases {
			if !strings.EqualFold(cand.Repo.Name, name) || cand.IsDraft || (cand.State != "" && cand.State != "OPEN") {
				continue
			}
			if w.Kind(cand) == KindOther || !hasApproval(cand) || w.inFlight(cand) {
				continue
			}
			return cand
		}
	}
	return nil
}

// laneNote is what a released release's message adds: the next release
// in the lane and how to send it, or "".
func (w *Workflow) laneNote(pr *model.PR) string {
	next := w.nextInLane(pr)
	if next == nil {
		return ""
	}
	if deploying(pr) {
		return fmt.Sprintf("; next in lane: %s, once %s's deploys are green", where(next), where(pr))
	}
	return fmt.Sprintf("; next in lane: %s — a on its row: release NOW", where(next))
}

// laneWait asks before NOW on a release while another in its lane is
// still on its way; true goes ahead.
func (w *Workflow) laneWait(app workflow.App, pr *model.PR) bool {
	busy := w.laneBusy(pr)
	if busy == nil {
		return true
	}
	if app.Confirm(fmt.Sprintf("%s is still on its way — the lane releases one at a time. Release %s NOW anyway?", where(busy), where(pr))) {
		return true
	}
	app.Notice(fmt.Sprintf("approve cancelled — %s waits for %s", where(pr), where(busy)))
	return false
}

// ---- a on a release row in the list ----

// Keys binds a in the list: on a release row (under releases or under a
// night) it is the detail's a, without opening the release first.
func (w *Workflow) Keys() []workflow.Key {
	return []workflow.Key{{View: "list", Ch: "a", Label: "approve", Priority: 15, Handler: w.listApprove}}
}

func (w *Workflow) listApprove(app workflow.App) {
	row, ok := app.Selected()
	pr, isPR := row.Item.(*model.PR)
	if !ok || !isPR || pr == nil {
		app.Notice("a in the list approves a release row; enter opens anything else")
		return
	}
	if _, sure := w.classify(pr, pr, w.stateOf(app)); sure == NotRelease {
		app.Notice(fmt.Sprintf("a in the list approves a release row; enter opens %s, then a", where(pr)))
		return
	}
	if !workflow.StaleGate(app, "approve") { // the detail's a asks the same
		return
	}
	w.Approve(app, pr, pr)
}
