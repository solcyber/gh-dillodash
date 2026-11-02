package releasepr

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// itemsStub is a window state with one manifest item per release.
type itemsStub map[string]workflow.ManifestItem

func (s itemsStub) ManifestItem(repo string, n int) (workflow.ManifestItem, bool) {
	it, ok := s[fmt.Sprintf("%s#%d", repo, n)]
	return it, ok
}

// TestNowOnASlottedReleaseOffersTheCancel (#24): release NOW on a
// release that holds a slot on tonight's manifest offers the
// /window-cancel of its item in the same flow, posted on the manifest.
func TestNowOnASlottedReleaseOffersTheCancel(t *testing.T) {
	patchNow(t, utc(1, 0)) // 21:00 ET: after the freeze
	w, app := newWF(t, true)
	rel := approved(makeRelease(relOpts{number: 158, repo: etl}), "alice")
	app.States = map[string]any{WindowName: itemsStub{"etl-jobs#158": {
		ID: "rel:etl-jobs#158", Flag: "scheduled", Night: "2026-10-27", StartUTC: "2026-10-28T03:12:00Z", Issue: 20}}}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true, true}
	if !w.Approve(app, rel, rel) {
		t.Fatal("handled")
	}
	got := comments(app)
	if len(got) != 2 || got[0] != `/release-now "`+reason+`"` || got[1] != "/window-cancel rel:etl-jobs#158" {
		t.Fatalf("the command, then the cancel: %q", got)
	}
	if c := app.Fake.CommentsPosted[1]; c.Repo != infra || c.Number != 20 {
		t.Errorf("the cancel goes on manifest #20 in the manifest repo: %+v", c)
	}
	q := app.Questions[len(app.Questions)-1]
	if !strings.Contains(q, "23:12 slot on manifest #20") || !strings.Contains(q, "/window-cancel rel:etl-jobs#158") {
		t.Errorf("the question names the slot and the exact comment: %q", q)
	}

	// N keeps the slot and says so
	w, app = newWF(t, true)
	app.States = map[string]any{WindowName: itemsStub{"etl-jobs#158": {
		ID: "rel:etl-jobs#158", Flag: "scheduled", Night: "2026-10-27", Issue: 20}}}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true, false}
	w.Approve(app, approved(makeRelease(relOpts{number: 158, repo: etl}), "alice"), nil)
	if got := comments(app); len(got) != 1 || !logHas(app, "notice", "the window still lists etl-jobs#158") {
		t.Errorf("N posts no cancel: %q %q", got, app.Log)
	}

	// a bumped item holds no slot: nothing more is asked
	w, app = newWF(t, true)
	app.States = map[string]any{WindowName: itemsStub{"etl-jobs#158": {ID: "rel:etl-jobs#158", Flag: "bumped", Status: "bumped", Issue: 20}}}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, approved(makeRelease(relOpts{number: 158, repo: etl}), "alice"), nil)
	if got := comments(app); len(got) != 1 || len(app.Questions) != 1 {
		t.Errorf("a bumped release: the command only: %q %q", got, app.Questions)
	}
}

// laneState is an applied state with the given releases, lane web-portal
// then etl-jobs.
func laneState(t *testing.T, rels ...*model.PR) (*Workflow, *State) {
	t.Helper()
	w, app := newWF(t, true)
	w.cfg.LaneOrder = []string{"web-portal", "etl-jobs"}
	st := &State{w: w, Releases: rels}
	st.index()
	w.Apply(app, st)
	return w, st
}

// TestLaneOrder (#24): after a release in the lane is released, the
// next repo's approved release is named; while one in the lane is on its
// way, NOW on another asks first.
func TestLaneOrder(t *testing.T) {
	patchNow(t, utc(3, 30))
	first := mergedRelease()
	first.Repo, first.Number = portal, 743
	next := approved(makeRelease(relOpts{number: 158, repo: etl}), "carol")
	w, _ := laneState(t, first, next)
	if got := w.laneNote(first); got != "; next in lane: etl-jobs#158 — a on its row: release NOW" {
		t.Errorf("next in lane: %q", got)
	}
	first.Set(ExtDeploy, deploys.State{Phase: "running"})
	if got := w.laneNote(first); !strings.Contains(got, "once web-portal#743's deploys are green") {
		t.Errorf("deploys still running: %q", got)
	}
	if busy := w.laneBusy(next); busy != first {
		t.Errorf("the lane is busy while the first deploys: %v", busy)
	}
	first.Set(ExtDeploy, deploys.State{Phase: "done"})
	if busy := w.laneBusy(next); busy != nil {
		t.Errorf("done deploying: the lane is free: %v", busy)
	}

	// the last in the lane, an unapproved next, or a repo not in the lane:
	// no note
	if got := w.laneNote(next); got != "" {
		t.Errorf("the last in the lane names nothing: %q", got)
	}
	unapproved := makeRelease(relOpts{number: 159, repo: etl})
	w, _ = laneState(t, first, unapproved)
	if got := w.laneNote(first); got != "" {
		t.Errorf("an unapproved next is not offered: %q", got)
	}
	w.cfg.LaneOrder = nil
	if got := w.laneNote(first); got != "" || w.laneBusy(unapproved) != nil {
		t.Errorf("no lane, no note: %q", got)
	}
}

// TestNowWaitsForTheLane: NOW on a lane release while another is still
// deploying asks first; N posts nothing.
func TestNowWaitsForTheLane(t *testing.T) {
	patchNow(t, utc(3, 30))
	first := mergedRelease()
	first.Repo, first.Number = portal, 743
	first.Set(ExtDeploy, deploys.State{Phase: "running"})
	next := approved(makeRelease(relOpts{number: 158, repo: etl}), "alice")
	w, st := laneState(t, first, next)
	_, app := newWF(t, true)
	app.SetState(Name, st)
	app.Picks, app.Confirms = []int{0}, []bool{false}
	w.Approve(app, next, next)
	if len(comments(app)) != 0 || !strings.Contains(app.Questions[0], "web-portal#743 is still on its way") ||
		!logHas(app, "notice", "etl-jobs#158 waits for web-portal#743") {
		t.Errorf("asks, N waits: %q %q", app.Questions, app.Log)
	}
}

// TestListApprove: a on a release row in the list is the detail's a;
// on any other row it says what it does.
func TestListApprove(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	rel := approved(makeRelease(relOpts{number: 743}), "alice")
	app.ViewName = "list"
	app.RowList = []workflow.Row{{Kind: "header", Item: "releases (1)"}, {Kind: RowRelease, Item: rel}, {Kind: "pr", Item: &model.PR{Repo: infra, Number: 7, HeadRefName: "feat/x", BaseRefName: "main"}}}
	app.Sel = 1
	w.listApprove(app)
	if len(app.Menus) != 1 || !strings.HasPrefix(app.Menus[0][1], "release NOW") {
		t.Errorf("a release row: NOW: %v %q", app.Menus, app.Log)
	}
	app.Sel = 2
	w.listApprove(app)
	if !logHas(app, "notice", "enter opens infra#7, then a") {
		t.Errorf("any other row: %q", app.Log)
	}
	keys := w.Keys()
	if len(keys) != 1 || keys[0].View != "list" || keys[0].Ch != "a" {
		t.Errorf("binds list a: %+v", keys)
	}
}

// TestBudgetCountdown: with apply_budget_mins a release in flight counts
// down on its row; over the budget it says by how much.
func TestBudgetCountdown(t *testing.T) {
	w, _ := newWF(t, true)
	start := utc(3, 0)
	p := Progress{Phase: PhaseDeploying, Started: start}
	if got := w.budgetLeft(p, start.Add(5*time.Minute), false); got != "" {
		t.Errorf("no budget, no countdown: %q", got)
	}
	w.cfg.ApplyBudgetMins = 35
	if got := w.budgetLeft(p, start.Add(5*time.Minute), false); got != "30m left" {
		t.Errorf("30 minutes left: %q", got)
	}
	if got := w.budgetLeft(p, start.Add(38*time.Minute), false); got != "over by 3m" {
		t.Errorf("over: %q", got)
	}
	for _, ph := range []string{PhaseScheduled, PhaseWaitingSlot} {
		if got := w.budgetLeft(Progress{Phase: ph, Started: start}, start, false); got != "" {
			t.Errorf("%s: no countdown: %q", ph, got)
		}
	}
	if got := w.budgetLeft(Progress{Phase: PhaseReleased, Terminal: true, Started: start}, start, false); got != "" {
		t.Errorf("ended: no countdown: %q", got)
	}
}
