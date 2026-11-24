package releasepr

import (
	"fmt"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/keymap"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// ---- the lane: tonight's order ----
//
// A release on a manifest with its own lane and lane_order (the freeze
// writes them) follows that order; any other release follows the
// config's lane_order, a manual night's order by repo.

// itemOf is the manifest item the tracker holds for pr, or nil.
func (w *Workflow) itemOf(pr *model.PR) *workflow.ManifestItem {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.progress[pr.Ident()]; ok && t.item != nil {
		cp := *t.item
		return &cp
	}
	return nil
}

// manifestLane is pr's manifest item when it has a place in a lane
// (lane and lane_order), or nil.
func (w *Workflow) manifestLane(pr *model.PR) *workflow.ManifestItem {
	if it := w.itemOf(pr); it != nil && it.Lane != "" && it.LaneOrder > 0 {
		return it
	}
	return nil
}

// sameLane reports two manifest items in one lane on one night.
func sameLane(a, b *workflow.ManifestItem) bool {
	return a != nil && b != nil && a.Night == b.Night && a.Lane == b.Lane
}

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
	mine := w.manifestLane(pr)
	if mine == nil && w.laneIndex(pr.Repo) < 0 {
		return nil
	}
	st := w.Current()
	if st == nil {
		return nil
	}
	for _, other := range st.Releases {
		if other.Ident() == pr.Ident() {
			continue
		}
		if mine != nil {
			if !sameLane(mine, w.manifestLane(other)) {
				continue
			}
		} else if w.laneIndex(other.Repo) < 0 {
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
	st := w.Current()
	if st == nil {
		return nil
	}
	if mine := w.manifestLane(pr); mine != nil {
		// the manifest's order: the next place after pr's in its lane
		var best *model.PR
		bestOrder := 0
		for _, cand := range st.Releases {
			it := w.manifestLane(cand)
			if cand.Ident() == pr.Ident() || !sameLane(mine, it) || it.LaneOrder <= mine.LaneOrder || !w.releasable(cand) {
				continue
			}
			if best == nil || it.LaneOrder < bestOrder {
				best, bestOrder = cand, it.LaneOrder
			}
		}
		return best
	}
	i := w.laneIndex(pr.Repo)
	if i < 0 {
		return nil
	}
	for _, name := range w.cfg.LaneOrder[i+1:] {
		for _, cand := range st.Releases {
			if strings.EqualFold(cand.Repo.Name, name) && w.releasable(cand) {
				return cand
			}
		}
	}
	return nil
}

// releasable reports an open, approved, non-draft release not already
// on its way: one NOW can send.
func (w *Workflow) releasable(cand *model.PR) bool {
	if cand.IsDraft || (cand.State != "" && cand.State != "OPEN") {
		return false
	}
	return w.Kind(cand) != KindOther && hasApproval(cand) && !w.inFlight(cand)
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
	return fmt.Sprintf("; next in lane: %s — %s on its row: release NOW", where(next), keymap.S(keymap.Approve))
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
	app.Notice(fmt.Sprintf("approve canceled — %s waits for %s", where(pr), where(busy)))
	return false
}

// ---- a on a release row in the list ----

// Keys binds a in the list: on a release row (under releases or under a
// night) it is the detail's a, without opening the release first.
func (w *Workflow) Keys() []workflow.Key {
	return []workflow.Key{{View: "list", Ch: keymap.S(keymap.Approve), Label: "approve", Priority: 15, Handler: w.listApprove, Offer: w.offerListApprove}}
}

func (w *Workflow) listApprove(app workflow.App) {
	row, ok := app.Selected()
	pr, isPR := row.Item.(*model.PR)
	if !ok || !isPR || pr == nil {
		app.Notice("A in the list approves a release row; enter opens anything else, and A approves there")
		return
	}
	if _, sure := w.classify(pr, pr, w.stateOf(app)); sure == NotRelease {
		app.Notice(fmt.Sprintf("%[1]s in the list approves a release row; enter opens %[2]s, then %[1]s", keymap.S(keymap.Approve), where(pr)))
		return
	}
	if !workflow.StaleGate(app, "approve") { // the detail's a asks the same
		return
	}
	w.Approve(app, pr, pr)
}

// offerListApprove: A in the list acts on a release row that is open.
func (w *Workflow) offerListApprove(f workflow.Focus) (string, bool) {
	if !f.HasRow {
		return "", false
	}
	pr, ok := f.Row.Item.(*model.PR)
	if !ok || pr == nil {
		return "", false
	}
	return w.approveLabel(f, pr, pr)
}

// ApproveOffer: A on an open release says what it does there.
func (w *Workflow) ApproveOffer(f workflow.Focus) (string, bool) {
	pr, ok := f.Item().(*model.PR)
	if !ok || pr == nil {
		return "", false
	}
	listed := f.Listed
	if f.View == "list" {
		listed = pr
	}
	return w.approveLabel(f, pr, listed)
}

// approveLabel is what A does on a release PR (ok false: not a release,
// or nothing to approve on it).
func (w *Workflow) approveLabel(f workflow.Focus, pr, listed *model.PR) (string, bool) {
	var st *State
	if f.State != nil {
		st, _ = f.State(Name).(*State)
	}
	if _, sure := w.classify(pr, listed, st); sure != IsRelease {
		return "", false
	}
	if (pr.State != "" && pr.State != "OPEN") || pr.IsDraft || strings.EqualFold(pr.Author.Login, f.Me) || w.NotARelease(pr) {
		return "", false
	}
	if pr.MyApprovalStands(f.Me) {
		if w.inFlight(pr) {
			// on its way: nothing to post, unless nobody picked the
			// command up, and then A only tells the manifest repo
			if _, _, ok := w.unpicked(pr, f.Now); ok {
				return "fire the dispatch", true
			}
			return "", false
		}
		return "release NOW", true
	}
	return "approve release", true
}
