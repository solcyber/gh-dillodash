package releasepr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// ---- go units waiting behind an approved release ----

// TestGoPastCut: the go units on the staging chain newer than the open
// release's head; a head off the walked chain counts nothing.
func TestGoPastCut(t *testing.T) {
	compare := ghapi.CompareResult{AheadBy: 4, Commits: stageCommits()} // units #610 (c1), #611 (c2), #612 (c3)
	tags := []string{"go/610", "go/611", "go/612"}
	if n, ok := GoPastCut(compare, tags, "go/", "c1"); !ok || n != 2 {
		t.Errorf("cut at #610: %d %v, want 2 newer go units", n, ok)
	}
	if n, ok := GoPastCut(compare, []string{"go/612"}, "go/", "c1"); !ok || n != 1 {
		t.Errorf("only go units count: %d %v", n, ok)
	}
	if n, ok := GoPastCut(compare, tags, "go/", "c3"); !ok || n != 0 {
		t.Errorf("cut at the staging head: %d %v", n, ok)
	}
	for _, cut := range []string{"", "nowhere"} {
		if n, ok := GoPastCut(compare, tags, "go/", cut); ok || n != 0 {
			t.Errorf("cut %q is not on the chain: %d %v", cut, n, ok)
		}
	}
}

// TestGoUnitsWaitingOnAnApprovedRelease: an approved release keeps its
// cut, so its row says how many newer go units wait for the next one;
// an unapproved release (the auto-cut re-cuts it) says nothing.
func TestGoUnitsWaitingOnAnApprovedRelease(t *testing.T) {
	patchNow(t, utc(4, 0))
	w, app := newWF(t, false)
	rel := approved(makeRelease(relOpts{number: 44, head: "c1"}), "carol")
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{rel}, ProdOid: "m", StageOid: "s"}}
	app.Fake.Compares = map[string]ghapi.CompareResult{"main...stage": {AheadBy: 4, Commits: stageCommits()}}
	app.Fake.Tags = map[model.Repo][]string{portal: {"go/610", "go/611", "go/612"}}
	s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := s.(*State)
	w.Apply(app, st)
	if len(st.Stages) != 1 || st.Stages[0].Units == nil || st.Stages[0].Units.Cut != "c1" || st.Stages[0].Units.GoPastCut != 2 {
		t.Fatalf("the stage row counts the go units past the cut: %+v", st.Stages)
	}
	listed := st.Find(rel.Ident())
	row := workflow.Row{Kind: RowRelease, Item: listed}
	L := w.releaseLayout(app, []workflow.Row{row}, 160, 4, 10)
	if got := text.SegsText(w.releaseSegs(app, row, L, 160, 0)); !strings.Contains(got, "2 go units waiting") {
		t.Errorf("an approved release names what waits: %q", got)
	}
	listed.ReviewDecision = ""
	if got := text.SegsText(w.releaseSegs(app, row, L, 160, 0)); strings.Contains(got, "waiting") {
		t.Errorf("not approved: the auto-cut takes them, nothing to say: %q", got)
	}
	if goWaitingText(1) != "1 go unit waiting" || goWaitingText(0) != "" {
		t.Errorf("wording: %q %q", goWaitingText(1), goWaitingText(0))
	}
}

// ---- the lease holder ----

// TestLeaseHolderAutomation: a holder may be an automation's name (a
// bot login, a workflow, a run), never a sentence.
func TestLeaseHolderAutomation(t *testing.T) {
	for _, h := range []string{"github-actions[bot]", "workflow:nightly-cut", "nightly-cut/run-19876543210", "cut@runner#2", "carol"} {
		if !leaseHolderRE.MatchString(h) {
			t.Errorf("%q is a holder", h)
		}
	}
	for _, h := range []string{"", "carol\nsafe to merge", "two words", "-dash", strings.Repeat("x", 65)} {
		if leaseHolderRE.MatchString(h) {
			t.Errorf("%q is not a holder", h)
		}
	}
	body := "<!-- release-lease:begin\n{\"leases\":[{\"repo\":\"web-portal\",\"holder\":\"workflow:nightly-cut\",\"since\":\"2026-10-28T03:18:00Z\"}]}\nrelease-lease:end -->"
	got, notes := ParseLeases(body, "acme", 7, utc(4, 0))
	if l := got["acme/web-portal"]; l == nil || l.Holder != "workflow:nightly-cut" || len(notes) != 0 {
		t.Errorf("an automation's lease is read: %+v %v", got, notes)
	}
}

// ---- the window's estimates ----

func estItem(status string) *workflow.ManifestItem {
	return &workflow.ManifestItem{ID: "rel:web-portal#42", Status: status, Night: "2026-10-27", Flag: "scheduled",
		StartUTC: utc(3, 20).Format(time.RFC3339), Issue: 4072, StartIsEstimate: true, Lane: "app", LaneOrder: 2,
		TSecs: 1500, SettleSecs: 240}
}

// TestEstimatedStart: an item whose start is an estimate says "est.",
// never "slot passed" while the item ahead runs, reads its waiting
// status as a place in the lane, and counts a dispatch from
// dispatched_utc.
func TestEstimatedStart(t *testing.T) {
	w, _ := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "carol")
	pr.Labels.Nodes = []model.Label{{Name: "window:scheduled"}}
	est := utc(3, 20)

	p := deriveWith(w, pr, func(tr *track) { tr.item = estItem("") }, est.Add(-2*time.Hour))
	if p.Phase != PhaseScheduled || !p.SlotEstimate || w.progressChip(p, est) != "scheduled est. 23:20" {
		t.Errorf("estimate: %+v %q", p, w.progressChip(p, est))
	}
	if !liveProgress(p, est.Add(-80*time.Minute)) || liveProgress(p, est.Add(-2*time.Hour)) {
		t.Error("an estimate is followed from well before it: it may start at the window's start")
	}
	// the estimate passed while the item ahead still runs: still after it
	ahead := estItem("")
	ahead.Ahead = "rel:etl-jobs#158"
	p = deriveWith(w, pr, func(tr *track) { tr.item = ahead }, est.Add(15*time.Minute))
	if p.Phase != PhaseScheduled || strings.Contains(p.Detail, "passed") || p.Detail != "after rel:etl-jobs#158" {
		t.Errorf("an item ahead runs: no 'slot passed': %+v", p)
	}
	// nothing ahead and the estimate passed
	p = deriveWith(w, pr, func(tr *track) { tr.item = estItem("") }, est.Add(15*time.Minute))
	if p.Phase != PhaseWaitingSlot || p.Detail != "estimated start passed, no run yet" {
		t.Errorf("nothing ahead: %+v", p)
	}
	// the conductor's own word for the lane
	p = deriveWith(w, pr, func(tr *track) { tr.item = estItem("waiting for rel:etl-jobs#158") }, est.Add(-30*time.Minute))
	if p.Phase != PhaseWaitingSlot || w.progressChip(p, est) != "waiting in lane" || p.Detail != "waiting for rel:etl-jobs#158" {
		t.Errorf("waiting for the one ahead: %+v %q", p, w.progressChip(p, est))
	}
	// dispatched before the estimate: the clock starts at dispatched_utc
	sent := utc(3, 4)
	disp := estItem("dispatched")
	disp.DispatchedUTC = sent.Format(time.RFC3339)
	p = deriveWith(w, pr, func(tr *track) { tr.item = disp }, utc(3, 6))
	if p.Phase != PhaseDispatched || !p.Started.Equal(sent) || !p.Since.Equal(sent) {
		t.Errorf("dispatched at %s: %+v", sent, p)
	}
	// an old manifest: a fixed slot, as before
	old := estItem("")
	old.StartIsEstimate, old.Ahead = false, "rel:etl-jobs#158"
	p = deriveWith(w, pr, func(tr *track) { tr.item = old }, est.Add(15*time.Minute))
	if p.Detail != "slot passed, no run yet" || w.progressChip(p, est) != "waiting for slot" {
		t.Errorf("a fixed slot: %+v", p)
	}
}

// TestManifestSettleAndBudget: the item's settle_s and T_s drive the
// settle meter and the countdown; without them the defaults hold.
func TestManifestSettleAndBudget(t *testing.T) {
	w, _ := newWF(t, true)
	start := utc(3, 0)
	if settleOf(Progress{}) != 300*time.Second || settleOf(Progress{SettleSecs: 240}) != 240*time.Second {
		t.Error("settle: the item's, else 300 s")
	}
	p := Progress{Phase: PhaseDeploying, Started: start, BudgetSecs: 1500}
	if got := w.budgetLeft(p, start.Add(5*time.Minute), false); got != "20m left" {
		t.Errorf("T_s 1500: %q", got)
	}
	w.cfg.ApplyBudgetMins = 35
	if got := w.budgetLeft(p, start.Add(5*time.Minute), false); got != "20m left" {
		t.Errorf("the item's estimate beats apply_budget_mins: %q", got)
	}
	p.BudgetSecs = 0
	if got := w.budgetLeft(p, start.Add(5*time.Minute), false); got != "30m left" {
		t.Errorf("no T_s: apply_budget_mins: %q", got)
	}
	if got := text.SegsText(settleSegs(200*time.Second, 240*time.Second)); !strings.Contains(got, "0:40 left") {
		t.Errorf("a 240 s settle: %q", got)
	}
	if got := text.SegsText(settleSegs(250*time.Second, 240*time.Second)); got != "settled; reading the alarms" {
		t.Errorf("settled: %q", got)
	}
	// the item reaches the progress
	pr := approved(makeRelease(relOpts{}), "carol")
	got := deriveWith(w, pr, func(tr *track) { tr.item = estItem("dispatched") }, utc(3, 30))
	if got.SettleSecs != 240 || got.BudgetSecs != 1500 {
		t.Errorf("the item's numbers: %+v", got)
	}
}

// ---- "Deployed — verifying" ----

const verifyingBody = "## Deployed — verifying\n\nDeploy succeeded: [run 7](https://github.com/acme/web-portal/actions/runs/7) (merge `abc123def456`); alarm check in ~5 min. [apply.yml run](https://github.com/acme/infra/actions/runs/91)"

// TestDeployedVerifyingIsNotAnOutcome: the early comment is no outcome
// heading; from the release job it starts the settle meter, and with no
// run in sight it says the release is verifying; the outcome after it
// still ends the flight.
func TestDeployedVerifyingIsNotAnOutcome(t *testing.T) {
	if _, ok := outcomeOf(comment(utc(3, 10), verifyingBody)); ok {
		t.Fatal("the heading regex ignores the early comment")
	}
	w, _ := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	cmd := comment(utc(3, 0), `/release-now "`+reason+`"`)
	cmd.Author.Login = "alice"
	early := comment(utc(3, 10), verifyingBody)
	p := deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd, early} }, utc(3, 12))
	if p.Phase != PhaseVerifying || p.Terminal || !p.VerifyingAt.Equal(utc(3, 10)) || !p.Since.Equal(utc(3, 10)) {
		t.Errorf("no run in sight: verifying from the comment: %+v", p)
	}
	// someone else's words in its shape are not believed
	fake := early
	fake.Author.Login = "mallory"
	if p := deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd, fake} }, utc(3, 12)); p.Phase != PhaseDispatched || !p.VerifyingAt.IsZero() {
		t.Errorf("a person's comment: %+v", p)
	}
	// from before the command (an earlier flight): history
	if p := deriveWith(w, pr, func(tr *track) {
		old := early
		old.CreatedAt = utc(2, 0)
		tr.comments = []model.Comment{old, cmd}
	}, utc(3, 12)); !p.VerifyingAt.IsZero() {
		t.Errorf("an earlier flight's comment: %+v", p)
	}
	// the outcome after it ends the flight
	done := comment(utc(3, 16), releasedBody)
	if p := deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd, early, done} }, utc(3, 17)); p.Phase != PhaseReleased || !p.Terminal {
		t.Errorf("released after: %+v", p)
	}
	// with the run in sight, the run's steps lead and the settle counts
	// from the comment
	run := runAt(utc(3, 0), "in_progress", "")
	job := &model.WorkflowJob{Status: "in_progress", RunAttempt: 1, Steps: []model.WorkflowStep{
		{Name: "Validate", Status: "completed", Conclusion: "success", StartedAt: utc(3, 1), CompletedAt: utc(3, 1)},
		{Name: "Wait for the deploy", Status: "completed", Conclusion: "success", StartedAt: utc(3, 2), CompletedAt: utc(3, 10)},
		{Name: "Verify: post Deployed comment", Status: "completed", Conclusion: "success", StartedAt: utc(3, 10), CompletedAt: utc(3, 10)},
		{Name: "Verify (bin/window-verify check-app over the repo's alarm stacks, after the settle)", Status: "in_progress", StartedAt: utc(3, 11)},
	}}
	p = deriveWith(w, pr, func(tr *track) { tr.comments, tr.run, tr.job = []model.Comment{cmd, early}, run, job }, utc(3, 12))
	if p.Phase != PhaseVerifying || p.Source != "run" || !p.VerifyingAt.Equal(utc(3, 10)) {
		t.Fatalf("the run leads: %+v", p)
	}
	var tail string
	for _, r := range w.timelineRows(p, utc(3, 12)) {
		if r.phase == PhaseVerifying {
			tail = text.SegsText(r.tail)
		}
	}
	if !strings.Contains(tail, "3:00 left") {
		t.Errorf("the settle counts from the comment at 03:10 (two minutes gone): %q", tail)
	}
}

// ---- the lane's order from the manifest ----

// TestManifestLaneOrder: a release with a place in tonight's lane names
// the next place's release, whatever the config's lane_order says; one
// without falls back to the config.
func TestManifestLaneOrder(t *testing.T) {
	patchNow(t, utc(3, 30))
	first := mergedRelease()
	first.Repo, first.Number = portal, 743
	second := approved(makeRelease(relOpts{number: 158, repo: etl}), "carol")
	third := approved(makeRelease(relOpts{number: 9, repo: model.Repo{Owner: "acme", Name: "db-migrations"}}), "carol")
	w, st := laneState(t, first, second, third)
	w.cfg.LaneOrder = []string{"web-portal", "db-migrations", "etl-jobs"} // the config says db next
	place := func(pr *model.PR, order int) {
		w.mu.Lock()
		tr := w.trackFor(pr, true, utc(3, 0))
		tr.item = &workflow.ManifestItem{ID: "rel:" + pr.Repo.Name, Night: "2026-10-27", Lane: "app", LaneOrder: order, Flag: "scheduled"}
		w.mu.Unlock()
	}
	if got := w.nextInLane(first); got != third {
		t.Fatalf("no manifest places: the config's order: %v", got)
	}
	place(first, 1)
	place(second, 2)
	place(third, 3)
	if got := w.nextInLane(first); got != second {
		t.Errorf("the manifest's order: %v", got)
	}
	if got := w.nextInLane(second); got != third {
		t.Errorf("then the third: %v", got)
	}
	if got := w.nextInLane(third); got != nil {
		t.Errorf("the last place names nothing: %v", got)
	}
	_ = st
}

// TestOfferCancelNamesTheEstimate: release NOW on a release with an
// estimated place offers the cancel and says "est.", not a slot.
func TestOfferCancelNamesTheEstimate(t *testing.T) {
	patchNow(t, utc(1, 0))
	w, app := newWF(t, true)
	app.States = map[string]any{WindowName: itemsStub{"etl-jobs#158": {
		ID: "rel:etl-jobs#158", Flag: "scheduled", Night: "2026-10-27", StartUTC: "2026-10-28T03:12:00Z", Issue: 20, StartIsEstimate: true}}}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true, true}
	w.Approve(app, approved(makeRelease(relOpts{number: 158, repo: etl}), "alice"), nil)
	q := app.Questions[len(app.Questions)-1]
	if !strings.Contains(q, "holds a place in the lane (est. 23:12) on manifest #20") || strings.Contains(q, "slot") {
		t.Errorf("the question: %q", q)
	}
}
