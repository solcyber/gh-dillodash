package releasepr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

// The release job's steps, as its workflow file names them; the setup
// ones are generic on purpose.
var jobSteps = []string{
	"Inspect dispatch payload",
	"Checkout main",
	"Re-validate the approval from the release PR",
	"Registry facts",
	"Assume the deploy role",
	"Alarm baseline",
	"Merge the release PR (merge commit, named head)",
	"Approve the environment deployments of that merge",
	"Wait for the deploy",
	"Verify (alarms after the settle)",
	"Outcome, labels, event and comment",
	"Write the apply-log entry as a fragment",
	"Fail the run unless the release verified",
}

// jobAt builds the release job with the first n steps done (step 8
// skipped), step n+1 running since `since`, the rest pending.
func jobAt(n int, start time.Time, attempt int) *model.WorkflowJob {
	j := &model.WorkflowJob{ID: 5, RunID: 91, Name: "app release (merge at the slot / release-now)", Status: "in_progress", RunAttempt: attempt, StartedAt: start}
	t := start
	for i, name := range jobSteps {
		s := model.WorkflowStep{Number: i + 1, Name: name}
		switch {
		case i < n:
			s.Status, s.Conclusion, s.StartedAt, s.CompletedAt = "completed", "success", t, t.Add(3*time.Second)
			if i == 7 {
				s.Conclusion = "skipped"
			}
			t = t.Add(3 * time.Second)
		case i == n:
			s.Status, s.StartedAt = "in_progress", t
		default:
			s.Status = "queued"
		}
		j.Steps = append(j.Steps, s)
	}
	if n >= len(jobSteps) {
		j.Status, j.Conclusion, j.CompletedAt = "completed", "success", t
	}
	return j
}

func runAt(created time.Time, status, conclusion string) *model.WorkflowRun {
	return &model.WorkflowRun{ID: 91, Name: "apply", DisplayTitle: "apply · PR #42 · plan rel:web-portal#42", Status: status, Conclusion: conclusion,
		Event: "repository_dispatch", RunAttempt: 1, HTMLURL: "https://github.com/acme/infra/actions/runs/91", CreatedAt: created, UpdatedAt: created}
}

func utcS(h, m, sec int) time.Time { return time.Date(2026, 10, 28, h, m, sec, 0, time.UTC) }

func comment(at time.Time, body string) model.Comment {
	return model.Comment{Author: model.Actor{Login: "infra-bot"}, CreatedAt: at, Body: body}
}

const releasedBody = "## Released (release-now)\n\nOutcome: `verified` — deploy green, alarms quiet\nMerge: abc123def4567890abc123def4567890abc123de\nApprover: @alice\n/release-now by @alice: customer down\nRun: https://github.com/acme/infra/actions/runs/91\n"

func TestStepPhases(t *testing.T) {
	cases := map[string]string{
		"Re-validate the approval from the release PR (the validator)": PhaseValidating,
		"Merge the release PR (merge commit, named head)":              PhaseMerging,
		"Approve the environment deployments of that merge":            PhaseApproving,
		"Wait for the deploy (the deploy waiter)":                      PhaseDeploying,
		"Verify (alarms over the repo's stacks)":                       PhaseVerifying,
		"Outcome, labels, event and comment":                           PhaseFinishing,
		"Write the apply-log entry as a fragment (via sibling PR)":     PhaseFinishing,
		"Inspect dispatch payload":                                     PhaseSetup,
		"Something new entirely":                                       PhaseSetup,
	}
	for name, want := range cases {
		if got := StepPhase(name); got != want {
			t.Errorf("%q -> %s, want %s", name, got, want)
		}
	}
	if PhaseIndex(PhaseDeploying) != 4 || PhaseIndex(PhaseReleased) != -1 || !Terminal(PhaseLost) || Terminal(PhaseVerifying) {
		t.Error("phase index / terminal")
	}
}

func TestOutcomeComments(t *testing.T) {
	at := utc(5, 0)
	cases := []struct {
		head        string
		phase, id   string
		detail, sha string
	}{
		{"## Released (window)", PhaseReleased, "verified", "deploy green, alarms quiet", "abc123def4567890abc123def4567890abc123de"},
		{"## Release NOT RUN (window)", PhaseNotRun, "not_run", "", ""},
		{"## Release FAILED: merge refused (release-now)", PhaseRefused, "refused", "", ""},
		{"## Release FAILED: the deploy did not succeed (release-now)", PhaseFailed, "deploy_failed", "", ""},
		{"## Release merged; the deploy is WAITING on an environment approval (window)", PhasePending, "deploy_pending", "migrate.yml waits on prod-migrations", ""},
		{"## Release UNVERIFIABLE (window)", PhaseUnverifiable, "unverifiable", "", ""},
		{"## Release FAILED VERIFICATION, NOT reverted (window)", PhaseFailed, "revert_failed", "", ""},
	}
	for _, c := range cases {
		body := c.head + "\n\nOutcome: `" + c.id + "`"
		if c.detail != "" {
			body += " — " + c.detail
		}
		if c.sha != "" {
			body += "\nMerge: " + c.sha
		}
		body += "\nRun: https://x/runs/1\n"
		o, ok := outcomeOf(comment(at, body))
		if !ok || o.phase != c.phase || o.id != c.id || o.detail != c.detail || o.sha != c.sha || o.url != "https://x/runs/1" {
			t.Errorf("%s: %+v %v", c.head, o, ok)
		}
	}
	if _, ok := outcomeOf(comment(at, "## Released elsewhere\nnot ours")); ok {
		t.Error("a heading needs the exact words")
	}
	if _, ok := outcomeOf(comment(at, "chatter\n## Released (window)\nOutcome: `verified`")); !ok {
		t.Error("the heading may follow other lines")
	}
	if !titleNames("apply · PR #42 · plan rel:web-portal#42", "rel:web-portal#42") || titleNames("apply · plan rel:web-portal#420", "rel:web-portal#42") ||
		!titleNames("rel:web-portal#4 ", "rel:web-portal#4") || titleNames("rel:web-portal#4", "rel:web-portal#42") {
		t.Error("the run title match stops at the number")
	}
}

// deriveWith runs derive on a fresh track with the given pieces.
func deriveWith(w *Workflow, pr *model.PR, mut func(t *track), now time.Time) Progress {
	t := &track{repo: pr.Repo, number: pr.Number, firstSeen: now.Add(-time.Minute)}
	if mut != nil {
		mut(t)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.derive(pr, t, now)
}

func TestDeriveNowPath(t *testing.T) {
	w, _ := newWF(t, true)
	t0 := utc(3, 0)
	pr := approved(makeRelease(relOpts{}), "alice")
	// nothing known: nothing in flight
	if p := deriveWith(w, pr, nil, t0); p.Phase != "" {
		t.Fatalf("idle: %+v", p)
	}
	// this session posted the command: dispatched since then
	p := deriveWith(w, pr, func(tr *track) { tr.nowPostedAt = t0 }, t0.Add(40*time.Second))
	if p.Phase != PhaseDispatched || p.Source != "command" || p.Since != t0 || p.Started != t0 || p.Terminal {
		t.Errorf("posted: %+v", p)
	}
	// the command found in the comments on a restart counts the same
	cmd := comment(t0, `/release-now "`+reason+`"`)
	p = deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd} }, t0.Add(time.Minute))
	if p.Phase != PhaseDispatched || p.Since != t0 {
		t.Errorf("command in comments: %+v", p)
	}
	// the dispatcher refused it
	p = deriveWith(w, pr, func(tr *track) {
		tr.comments = []model.Comment{cmd, comment(t0.Add(20*time.Second), "**/release-now refused:** no standing approval on the head\n\nFix the underlying issue and post a new /release-now.")}
	}, t0.Add(time.Minute))
	if p.Phase != PhaseRefused || !p.Terminal || p.Detail != "no standing approval on the head" {
		t.Errorf("refused: %+v", p)
	}
	// a refusal older than a newer command does not count
	p = deriveWith(w, pr, func(tr *track) {
		tr.comments = []model.Comment{comment(t0.Add(-time.Hour), `/release-now "`+reason+`"`), comment(t0.Add(-50*time.Minute), "**/release-now refused:** old"), cmd}
	}, t0.Add(time.Minute))
	if p.Phase != PhaseDispatched {
		t.Errorf("old refusal: %+v", p)
	}
	// the run: queued, then each step in turn
	run := runAt(t0.Add(30*time.Second), "queued", "")
	p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt, tr.run = t0, run }, t0.Add(time.Minute))
	if p.Phase != PhaseSetup || p.Detail != "run queued" || p.RunID != 91 || p.RunURL == "" || p.Started != t0 {
		t.Errorf("queued run: %+v", p)
	}
	run.Status = "in_progress"
	jobStart := t0.Add(45 * time.Second)
	want := map[int]string{0: PhaseSetup, 2: PhaseValidating, 6: PhaseMerging, 7: PhaseApproving, 8: PhaseDeploying, 9: PhaseVerifying, 10: PhaseFinishing, 12: PhaseFinishing}
	for n, ph := range want {
		job := jobAt(n, jobStart, 1)
		p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt, tr.run, tr.job = t0, run, job }, t0.Add(5*time.Minute))
		if p.Phase != ph || p.Source != "run" || p.Terminal || len(p.Steps) != len(jobSteps) {
			t.Errorf("step %d running: phase %s want %s (%+v)", n, p.Phase, ph, p)
		}
		if p.Since != job.Steps[n].StartedAt || p.Detail != jobSteps[n] {
			t.Errorf("step %d: since %v detail %q", n, p.Since, p.Detail)
		}
	}
	// deploying counts the app repo's runs on the merge
	merged := mergedRelease()
	merged.Repo, merged.Number = portal, 42
	merged.Set(ExtDeploy, deploys.State{Phase: "running", Runs: []deploys.Row{{Style: "chip.approved"}, {Style: "chip.approved"}, {Style: "chip.queued"}}})
	p = deriveWith(w, merged, func(tr *track) { tr.nowPostedAt, tr.run, tr.job = t0, run, jobAt(8, jobStart, 1) }, t0.Add(5*time.Minute))
	if p.Phase != PhaseDeploying || p.DeployDone != 2 || p.DeployTotal != 3 || p.MergeSHA != mergedSHA {
		t.Errorf("deploying: %+v", p)
	}
	// the run failed at verify
	failed := runAt(t0.Add(30*time.Second), "completed", "failure")
	fj := jobAt(9, jobStart, 1)
	fj.Status, fj.Conclusion, fj.CompletedAt = "completed", "failure", jobStart.Add(time.Hour)
	fj.Steps[9].Status, fj.Steps[9].Conclusion, fj.Steps[9].CompletedAt = "completed", "failure", jobStart.Add(time.Hour)
	p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt, tr.run, tr.job = t0, failed, fj }, t0.Add(2*time.Hour))
	if p.Phase != PhaseFailed || !p.Terminal || p.Detail != "at verifying" || p.Since != fj.CompletedAt {
		t.Errorf("failed run: %+v", p)
	}
	// the run succeeded: released even before the comment is read
	ok := runAt(t0.Add(30*time.Second), "completed", "success")
	p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt, tr.run, tr.job = t0, ok, jobAt(13, jobStart, 1) }, t0.Add(time.Hour))
	if p.Phase != PhaseReleased || !p.Terminal || p.Outcome != "verified" || p.Source != "run" {
		t.Errorf("succeeded run: %+v", p)
	}
	// the outcome comment is the richer word, and wins over the run
	out := comment(t0.Add(10*time.Minute), releasedBody)
	p = deriveWith(w, pr, func(tr *track) {
		tr.nowPostedAt, tr.run, tr.job, tr.comments = t0, ok, jobAt(13, jobStart, 1), []model.Comment{cmd, out}
	}, t0.Add(time.Hour))
	if p.Phase != PhaseReleased || p.Source != "comment" || p.Detail != "deploy green, alarms quiet" || p.MergeSHA != mergedSHA || p.Approver != "alice" || p.UpdatedAt != out.CreatedAt {
		t.Errorf("outcome comment: %+v", p)
	}
	// a run newer than the outcome (a re-dispatch) reopens the story
	again := runAt(t0.Add(20*time.Minute), "in_progress", "")
	p = deriveWith(w, pr, func(tr *track) {
		tr.nowPostedAt, tr.run, tr.job, tr.comments = t0, again, jobAt(2, t0.Add(21*time.Minute), 1), []model.Comment{cmd, out}
	}, t0.Add(22*time.Minute))
	if p.Phase != PhaseValidating || p.Terminal {
		t.Errorf("re-dispatch: %+v", p)
	}
	// merged with the run out of sight: the app repo's deploy says
	merged.Set(ExtDeploy, deploys.State{Phase: "done"})
	p = deriveWith(w, merged, func(tr *track) { tr.nowPostedAt = t0 }, t0.Add(5*time.Minute))
	if p.Phase != PhaseVerifying || p.Source != "deploy" {
		t.Errorf("merged, deploy done, no run: %+v", p)
	}
}

func TestDeriveWindowPath(t *testing.T) {
	w, _ := newWF(t, true)
	slot := utc(3, 0) // 23:00 ET
	pr := approved(makeRelease(relOpts{}), "carol")
	pr.Labels.Nodes = []model.Label{{Name: "window:scheduled"}}
	item := func(status string) *workflow.ManifestItem {
		return &workflow.ManifestItem{ID: "rel:web-portal#42", Status: status, Night: "2026-10-28", StartUTC: slot.Format(time.RFC3339), Issue: 4072}
	}
	// scheduled, the slot hours away: no polling yet
	p := deriveWith(w, pr, func(tr *track) { tr.item = item("") }, slot.Add(-3*time.Hour))
	if p.Phase != PhaseScheduled || p.SlotAt != slot || p.Night != "2026-10-28" || liveProgress(p, slot.Add(-3*time.Hour)) {
		t.Errorf("scheduled: %+v", p)
	}
	if !liveProgress(p, slot.Add(-5*time.Minute)) {
		t.Error("live from ten minutes before the slot")
	}
	if got := w.progressChip(p, slot.Add(-3*time.Hour)); got != "scheduled 23:00" {
		t.Errorf("chip: %q", got)
	}
	// the label without an item yet
	p = deriveWith(w, pr, nil, slot.Add(-3*time.Hour))
	if p.Phase != PhaseScheduled || p.Source != "label" || w.progressChip(p, slot) != "scheduled" {
		t.Errorf("label only: %+v", p)
	}
	for status, want := range map[string]string{
		"waiting for terraform item(s) #4080 to be final": PhaseWaitingSlot,
		"waiting for /release until 2026-10-29T03:10:00Z": PhaseWaitingSlot,
		"dispatched":                         PhaseDispatched,
		"cancelled":                          PhaseCancelled,
		"rolled: after the cutoff":           PhaseRolled,
		"verified — run https://x/1":         PhaseReleased,
		"FAILED — run https://x/1":           PhaseFailed,
		"refused (rolled) — run https://x/1": PhaseRefused,
		"run cancelled — nothing merged":     PhaseCancelled,
		"PENDING an environment approval (merged; deploy held) — run https://x/1": PhasePending,
	} {
		p = deriveWith(w, pr, func(tr *track) { tr.item = item(status) }, slot.Add(2*time.Minute))
		if p.Phase != want || p.Source != "manifest" || p.Detail != status {
			t.Errorf("item %q: %+v", status, p)
		}
		if Terminal(want) != p.Terminal {
			t.Errorf("item %q: terminal %v", status, p.Terminal)
		}
	}
	// the slot passed and nothing says why
	p = deriveWith(w, pr, func(tr *track) { tr.item = item("") }, slot.Add(3*time.Minute))
	if p.Phase != PhaseWaitingSlot || p.Detail != "slot passed, no run yet" {
		t.Errorf("slot passed: %+v", p)
	}
	// the conductor's labels at the end
	done := approved(makeRelease(relOpts{}), "carol")
	done.Labels.Nodes = []model.Label{{Name: "window:applied"}}
	if p = deriveWith(w, done, nil, slot); p.Phase != PhaseReleased || p.Source != "label" {
		t.Errorf("applied label: %+v", p)
	}
	done.Labels.Nodes = []model.Label{{Name: "window:failed"}}
	if p = deriveWith(w, done, nil, slot); p.Phase != PhaseFailed {
		t.Errorf("failed label: %+v", p)
	}
	done.Labels.Nodes = []model.Label{{Name: "window:rolled"}}
	if p = deriveWith(w, done, nil, slot); p.Phase != PhaseRolled {
		t.Errorf("rolled label: %+v", p)
	}
	done.Labels.Nodes = []model.Label{{Name: "window:pending"}}
	if p = deriveWith(w, done, nil, slot); p.Phase != PhasePending || !p.Terminal || p.Outcome != "deploy_pending" {
		t.Errorf("pending label: %+v", p)
	}
}

// TestDeployPending: a release merged with its deploy held at an
// environment ends its tracking (no "lost track") on the outcome
// comment, points at D, and follows the labels once someone swaps
// window:pending by hand.
func TestDeployPending(t *testing.T) {
	w, _ := newWF(t, true)
	t0 := utc(3, 0)
	pr := approved(makeRelease(relOpts{}), "alice")
	held := comment(t0.Add(4*time.Minute), "## Release merged; the deploy is WAITING on an environment approval (release-now)\n\n"+
		"Outcome: `deploy_pending` — migrate.yml waits on prod-migrations\nMerge: "+mergedSHA+"\nRun: https://x/runs/91\n")
	mut := func(tr *track) { tr.nowPostedAt, tr.comments = t0, []model.Comment{held} }
	p := deriveWith(w, pr, mut, t0.Add(3*time.Hour))
	if p.Phase != PhasePending || !p.Terminal || p.Outcome != "deploy_pending" || p.Detail != "migrate.yml waits on prod-migrations" || p.MergeSHA != mergedSHA {
		t.Fatalf("held: %+v", p)
	}
	if got := w.progressChip(p, t0); got != "deploy waiting · D" || progressStyle(p) != "chip.rolled" {
		t.Errorf("chip %q %s", got, progressStyle(p))
	}
	if f := railFrac(p, t0); f <= float64(PhaseIndex(PhaseDeploying))/float64(len(runPhases)) || f >= 1 {
		t.Errorf("the rail stops at the deploy: %v", f)
	}
	if segs := railSegs(p, t0, nil, "x", 20, false); strings.Contains(text.SegsText(segs), glyphs.G("hazard")) {
		t.Error("a held deploy is not a hazard")
	}
	for _, r := range w.timelineRows(p, t0) {
		if PhaseIndex(r.phase) < PhaseIndex(PhaseDeploying) && r.state != "done" {
			t.Errorf("%s before the deploy: %s", r.phase, r.state)
		}
	}
	pr.Labels.Nodes = []model.Label{{Name: "window:applied"}}
	if p = deriveWith(w, pr, mut, t0.Add(3*time.Hour)); p.Phase != PhaseReleased || p.Outcome != "verified" {
		t.Errorf("swapped to applied by hand: %+v", p)
	}
	pr.Labels.Nodes = []model.Label{{Name: "window:failed"}}
	if p = deriveWith(w, pr, mut, t0.Add(3*time.Hour)); p.Phase != PhaseFailed {
		t.Errorf("swapped to failed by hand: %+v", p)
	}
}

func TestDeriveLost(t *testing.T) {
	w, _ := newWF(t, true)
	t0 := utc(3, 0)
	pr := approved(makeRelease(relOpts{}), "alice")
	p := deriveWith(w, pr, func(tr *track) { tr.nowPostedAt = t0 }, t0.Add(89*time.Minute))
	if p.Phase != PhaseDispatched {
		t.Errorf("89m: %+v", p)
	}
	p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt = t0 }, t0.Add(91*time.Minute))
	if p.Phase != PhaseLost || !p.Terminal || !strings.HasPrefix(p.Detail, "no outcome after 1h30m") {
		t.Errorf("91m: %+v", p)
	}
	// a scheduled release is never lost: its slot is hours away
	s := approved(makeRelease(relOpts{}), "carol")
	s.Labels.Nodes = []model.Label{{Name: "window:scheduled"}}
	if p = deriveWith(w, s, nil, t0.Add(5*time.Hour)); p.Phase != PhaseScheduled {
		t.Errorf("scheduled is patient: %+v", p)
	}
	w.cfg.ProgressTimeoutSecs = 60
	if p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt = t0 }, t0.Add(2*time.Minute)); p.Phase != PhaseLost {
		t.Errorf("configured timeout: %+v", p)
	}
}

// TestTrackingStartsAndStops: the now path starts a track; Refresh
// reads comments once per update for approved and freshly merged
// releases and drops what left the list; a release merged by hand with
// no command is not followed.
func TestTrackingStartsAndStops(t *testing.T) {
	patchNow(t, utc(3, 0))
	w, app := newWF(t, true)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	pr := makeRelease(relOpts{})
	w.Approve(app, pr, nil)
	p, ok := w.progressOf(pr)
	if !ok || p.Phase != PhaseDispatched || !w.Live(app) {
		t.Fatalf("after the now path: %+v %v live=%v", p, ok, w.Live(app))
	}
	// a refresh with the release still open keeps it, reads its comments
	// once, and re-reads only when the PR's UpdatedAt moves
	open := approved(makeRelease(relOpts{}), "alice")
	byHand := mergedRelease()
	byHand.Repo, byHand.Number = portal, 77
	at := utc(2, 50)
	byHand.MergedAt = &at
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{open, byHand}, ProdOid: "m", StageOid: "s"}}
	app.Fake.Comments = map[model.Ident][]model.Comment{open.Ident(): {comment(utc(2, 59), `/release-now "`+reason+`"`)}}
	refresh := func() *State {
		s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		w.Apply(app, s)
		w.AfterApply(app)
		return s.(*State)
	}
	refresh()
	if app.Fake.Reads["comments"] != 2 {
		t.Errorf("comments read once per candidate (the open one and the fresh merge): %v", app.Fake.Reads)
	}
	if _, ok := w.progressOf(byHand); ok {
		t.Error("a hand merge with no command is not followed")
	}
	if p, ok := w.progressOf(open); !ok || p.Phase != PhaseDispatched || p.Since != utc(3, 0) { // this session's post is the newer command
		t.Errorf("the open one: %+v %v", p, ok)
	}
	refresh()
	if app.Fake.Reads["comments"] != 2 {
		t.Errorf("no re-read without an update: %v", app.Fake.Reads)
	}
	open.UpdatedAt = utc(3, 1)
	refresh()
	if app.Fake.Reads["comments"] != 3 {
		t.Errorf("re-read after an update: %v", app.Fake.Reads)
	}
	// the release leaves the list: forgotten
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{byHand}, ProdOid: "m", StageOid: "s"}}
	refresh()
	if _, ok := w.progressOf(open); ok || w.Live(app) {
		t.Error("gone from the list, gone from the tracker")
	}
}

// pollFixture: a release with the command posted, the manifest repo's
// apply runs and jobs scripted.
func pollFixture(t *testing.T) (*Workflow, *wftest.App, *model.PR) {
	t.Helper()
	patchNow(t, utc(3, 0))
	w, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{pr}, ProdOid: "m", StageOid: "s"}}
	app.Fake.Comments = map[model.Ident][]model.Comment{pr.Ident(): {comment(utc(2, 59), `/release-now "`+reason+`"`)}}
	s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(app, s)
	w.AfterApply(app)
	return w, app, pr
}

func poll(t *testing.T, w *Workflow, app *wftest.App) {
	t.Helper()
	patch, err := w.Poll(context.Background(), app.Fake)
	if err != nil {
		t.Fatal(err)
	}
	if patch != nil {
		w.ApplyPoll(app, patch)
	}
}

func TestPollBudget(t *testing.T) {
	w, app, pr := pollFixture(t)
	reads := func() map[string]int {
		return map[string]int{"file_runs": app.Fake.Reads["file_runs"], "jobs": app.Fake.Reads["jobs"], "comments": app.Fake.Reads["comments"]}
	}
	base := reads()
	// cycle 1: no run yet -> one listing (nothing matches) + comments
	poll(t, w, app)
	if r := reads(); r["file_runs"] != base["file_runs"]+1 || r["jobs"] != base["jobs"] || r["comments"] != base["comments"]+1 {
		t.Errorf("cycle 1: %v (base %v)", r, base)
	}
	// the run appears: listing finds it, its jobs are read, no comments
	// while it runs
	app.Fake.FileRuns = map[string][]model.WorkflowRun{"acme/infra:apply.yml": {
		{ID: 90, DisplayTitle: "apply · PR #420 · plan rel:web-portal#420", Event: "repository_dispatch", Status: "in_progress", CreatedAt: utcS(2, 59, 30)},
		*runAt(utcS(2, 59, 40), "in_progress", ""),
	}}
	app.Fake.Jobs = map[int64][]model.WorkflowJob{91: {*jobAt(6, utcS(2, 59, 50), 1)}}
	clock.Now = func() time.Time { return utc(3, 1) }
	poll(t, w, app)
	p, _ := w.progressOf(pr)
	if p.Phase != PhaseMerging || p.RunID != 91 {
		t.Fatalf("after the run appeared: %+v", p)
	}
	before := reads()
	poll(t, w, app)
	if r := reads(); r["file_runs"] != before["file_runs"] || r["jobs"] != before["jobs"]+1 || r["comments"] != before["comments"] {
		t.Errorf("steady state is one jobs read: %v (before %v)", r, before)
	}
	// the job finishes: one last jobs read freezes the steps, then the
	// comments are read for the outcome, and the listing is not repeated
	done := jobAt(13, utcS(2, 59, 50), 1)
	app.Fake.Jobs[91] = []model.WorkflowJob{*done}
	poll(t, w, app)
	if p, _ = w.progressOf(pr); p.Phase != PhaseReleased || p.Source != "run" {
		t.Fatalf("run done: %+v", p)
	}
	if n := len(app.Log); n == 0 || !strings.Contains(app.Log[n-1], "web-portal#42 released") || app.Launched != 1 {
		t.Errorf("released once, with the launch: %v launched=%d", app.Log, app.Launched)
	}
	before = reads()
	poll(t, w, app) // terminal: nothing live, nothing read
	if r := reads(); r != nil && (r["jobs"] != before["jobs"] || r["file_runs"] != before["file_runs"]) {
		t.Errorf("terminal reads nothing: %v (before %v)", r, before)
	}
	if w.Live(app) {
		t.Error("not live once terminal")
	}
}

func TestPollInfraOffKeepsTheAppSide(t *testing.T) {
	w, app, pr := pollFixture(t)
	app.Fake.Fail["file_runs"] = &ghapi.Error{Status: 403, Message: "Resource not accessible by personal access token"}
	poll(t, w, app)
	if !logHas(app, "notice", "Actions are not readable with this token") {
		t.Errorf("one notice: %v", app.Log)
	}
	n := len(app.Log)
	app.Fake.Comments[pr.Ident()] = append(app.Fake.Comments[pr.Ident()], comment(utc(3, 5), releasedBody))
	clock.Now = func() time.Time { return utc(3, 6) }
	poll(t, w, app)
	if p, _ := w.progressOf(pr); p.Phase != PhaseReleased || p.Source != "comment" {
		t.Errorf("the outcome comment still lands: %+v", p)
	}
	if c := 0; func() bool {
		for _, l := range app.Log[n:] {
			if strings.Contains(l, "not readable") {
				c++
			}
		}
		return c != 0
	}() {
		t.Error("the notice is given once")
	}
	if app.Fake.Reads["file_runs"] != 1 {
		t.Errorf("the listing is not retried this session: %v", app.Fake.Reads)
	}
}

func TestRedispatchPicksTheNewerRun(t *testing.T) {
	w, app, pr := pollFixture(t)
	old := *runAt(utcS(2, 59, 40), "completed", "failure")
	app.Fake.FileRuns = map[string][]model.WorkflowRun{"acme/infra:apply.yml": {old}}
	fj := jobAt(9, utcS(2, 59, 50), 1)
	fj.Status, fj.Conclusion, fj.CompletedAt = "completed", "failure", utc(3, 2)
	fj.Steps[9].Status, fj.Steps[9].Conclusion = "completed", "failure"
	app.Fake.Jobs = map[int64][]model.WorkflowJob{91: {*fj}}
	clock.Now = func() time.Time { return utc(3, 3) }
	poll(t, w, app)
	if p, _ := w.progressOf(pr); p.Phase != PhaseFailed || !logHas(app, "fail", "release FAILED at verifying") {
		t.Fatalf("first run failed: %+v %v", p, app.Log)
	}
	if w.Live(app) {
		t.Fatal("terminal is not live")
	}
	// the approver posts the command again: the track reopens, the
	// listing finds the newer run
	again := comment(utc(3, 4), `/release-now "`+reason+` again"`)
	app.Fake.Comments[pr.Ident()] = append(app.Fake.Comments[pr.Ident()], again)
	pr.UpdatedAt = utc(3, 4)
	newer := *runAt(utcS(3, 4, 20), "in_progress", "")
	newer.ID = 92
	app.Fake.FileRuns["acme/infra:apply.yml"] = []model.WorkflowRun{old, newer}
	app.Fake.Jobs[92] = []model.WorkflowJob{*jobAt(2, utcS(3, 4, 30), 1)}
	clock.Now = func() time.Time { return utc(3, 5) }
	s, _, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	w.Apply(app, s)
	w.AfterApply(app)
	poll(t, w, app)
	if p, _ := w.progressOf(pr); p.Phase != PhaseValidating || p.RunID != 92 {
		t.Errorf("the newer run: %+v", p)
	}
	// and it ends well: announced again, since the phase differs
	app.Fake.Jobs[92] = []model.WorkflowJob{*jobAt(13, utcS(3, 4, 30), 1)}
	app.Fake.FileRuns["acme/infra:apply.yml"][1].Status = "completed"
	clock.Now = func() time.Time { return utc(3, 20) }
	poll(t, w, app)
	if p, _ := w.progressOf(pr); p.Phase != PhaseReleased || !logHas(app, "ok", "web-portal#42 released") {
		t.Errorf("released after a failure: %+v %v", p, app.Log)
	}
}

func TestProgressChipAndStyle(t *testing.T) {
	w, _ := newWF(t, true)
	now := utc(3, 10)
	since := now.Add(-2*time.Minute - 17*time.Second)
	cases := []struct {
		p           Progress
		text, style string
	}{
		{Progress{Phase: PhaseDispatched, Since: since}, "dispatched 2m", "chip.queued"},
		{Progress{Phase: PhaseValidating, Since: since}, "validating 2m", "chip.queued"},
		{Progress{Phase: PhaseDeploying, Since: since, DeployDone: 3, DeployTotal: 9}, "deploying 3/9 · 2m", "chip.queued"},
		{Progress{Phase: PhaseDeploying, Since: since}, "deploying 2m", "chip.queued"},
		{Progress{Phase: PhaseVerifying, Since: now.Add(-40 * time.Second)}, "verifying 40s", "chip.queued"},
		{Progress{Phase: PhaseWaitingSlot}, "waiting for slot", "chip.sched"},
		{Progress{Phase: PhaseReleased, Terminal: true}, "released", "chip.approved"},
		{Progress{Phase: PhasePending, Terminal: true}, "deploy waiting · D", "chip.rolled"},
		{Progress{Phase: PhaseFailed, Terminal: true}, "release FAILED", "chip.rolled"},
		{Progress{Phase: PhaseRefused, Terminal: true}, "release refused", "chip.rolled"},
		{Progress{Phase: PhaseRolled, Terminal: true}, "rolled", "chip.rolled"},
		{Progress{Phase: PhaseCancelled, Terminal: true}, "release cancelled", "chip.state"},
		{Progress{Phase: PhaseLost, Terminal: true}, "lost track", "chip.state"},
	}
	for _, c := range cases {
		if got := w.progressChip(c.p, now); got != c.text {
			t.Errorf("%s: chip %q want %q", c.p.Phase, got, c.text)
		}
		if got := progressStyle(c.p); got != c.style {
			t.Errorf("%s: style %q want %q", c.p.Phase, got, c.style)
		}
	}
}

// TestProgressDrivesTheRow: a tracked release's row carries the
// progress chip and its style, is live, and filters by phase.
func TestProgressDrivesTheRow(t *testing.T) {
	w, app, pr := pollFixture(t)
	listed := w.Current().Find(pr.Ident())
	chip := w.kindChip(app, listed, 0, false)
	if got := text.SegsText(chip); !strings.Contains(got, "dispatched 1m") {
		t.Errorf("chip %q", got)
	}
	if got := chip[len(chip)-1].Style; got != "cap:chip.queued" {
		t.Errorf("style %q", got)
	}
	if got := text.SegsText(w.kindChip(app, listed, 0, true)); strings.Contains(got, glyphs.G("trough")) || !strings.Contains(got, "dispatched") || strings.Contains(got, "1m") {
		t.Errorf("the compact chip is the phase without the rail or the clock: %q", got)
	}
	row := workflow.Row{Kind: RowRelease, Item: listed}
	if !w.RowKinds()[RowRelease].Live(app, row) {
		t.Error("a release in flight is a live row")
	}
	if got := w.SearchText(row); !strings.Contains(got, "dispatched") {
		t.Errorf("search text: %q", got)
	}
	if rows := w.Sections(app, []string{"dispatched"}, "alice"); len(rows) != 2 {
		t.Errorf("filtering by phase: %d rows", len(rows))
	}
}

func TestAnnounceOnce(t *testing.T) {
	w, app, pr := pollFixture(t)
	app.Fake.Comments[pr.Ident()] = append(app.Fake.Comments[pr.Ident()], comment(utc(3, 5), "## Release FAILED: the deploy did not succeed (release-now)\n\nOutcome: `deploy_failed` — migrate.yml failed\nMerge: "+mergedSHA+"\nRun: https://x/runs/91\n"))
	clock.Now = func() time.Time { return utc(3, 6) }
	poll(t, w, app)
	poll(t, w, app)
	fails := 0
	for _, l := range app.Log {
		if strings.HasPrefix(l, "fail: ") && strings.Contains(l, "release FAILED") {
			fails++
			if !strings.Contains(l, "migrate.yml failed") || !strings.Contains(l, "run: https://x/runs/91") {
				t.Errorf("the message carries the detail and the run: %q", l)
			}
		}
	}
	if fails != 1 || app.Launched != 0 {
		t.Errorf("one failure toast, no launch: %d %d\n%v", fails, app.Launched, app.Log)
	}
}

// TestRedispatchAfterAnOutcomeComment: a release that failed by its
// outcome comment and is sent again is followed again, and its second
// failure is announced like the first.
func TestRedispatchAfterAnOutcomeComment(t *testing.T) {
	w, app, pr := pollFixture(t)
	failed := "## Release FAILED: the deploy did not succeed (release-now)\n\nOutcome: `deploy_failed` — migrate.yml failed\n"
	app.Fake.Comments[pr.Ident()] = append(app.Fake.Comments[pr.Ident()], comment(utc(3, 5), failed))
	clock.Now = func() time.Time { return utc(3, 6) }
	poll(t, w, app)
	if p, _ := w.progressOf(pr); p.Phase != PhaseFailed || w.Live(app) {
		t.Fatalf("first flight failed: %+v", p)
	}
	app.Fake.Comments[pr.Ident()] = append(app.Fake.Comments[pr.Ident()], comment(utc(3, 7), `/release-now "`+reason+` again"`))
	pr.UpdatedAt = utc(3, 7)
	clock.Now = func() time.Time { return utc(3, 8) }
	s, _, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	w.Apply(app, s)
	w.AfterApply(app)
	if p, _ := w.progressOf(pr); p.Phase != PhaseDispatched || !w.Live(app) {
		t.Fatalf("the new command reopens the flight: %+v", p)
	}
	newer := *runAt(utcS(3, 7, 20), "in_progress", "")
	newer.ID = 92
	app.Fake.FileRuns = map[string][]model.WorkflowRun{"acme/infra:apply.yml": {newer}}
	app.Fake.Jobs = map[int64][]model.WorkflowJob{92: {*jobAt(2, utcS(3, 7, 30), 1)}}
	poll(t, w, app)
	if p, _ := w.progressOf(pr); p.RunID != 92 {
		t.Fatalf("the new run is followed: %+v", p)
	}
	app.Fake.FileRuns["acme/infra:apply.yml"][0].Status = "completed"
	fj := jobAt(9, utcS(3, 7, 30), 1)
	fj.Status, fj.Conclusion, fj.CompletedAt = "completed", "failure", utc(3, 12)
	fj.Steps[9].Status, fj.Steps[9].Conclusion = "completed", "failure"
	app.Fake.Jobs[92] = []model.WorkflowJob{*fj}
	clock.Now = func() time.Time { return utc(3, 13) }
	poll(t, w, app)
	fails := 0
	for _, l := range app.Log {
		if strings.HasPrefix(l, "fail: ") && strings.Contains(l, "release FAILED") {
			fails++
		}
	}
	if fails != 2 {
		t.Errorf("each flight's failure is announced: %v", app.Log)
	}
}

// TestStartupIsQuiet: what finished, or was lost, before the session
// began is drawn but not announced.
func TestStartupIsQuiet(t *testing.T) {
	patchNow(t, utc(3, 0))
	w, app := newWF(t, true)
	done := approved(makeRelease(relOpts{}), "alice")
	stale := approved(makeRelease(relOpts{number: 43}), "alice")
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{done, stale}, ProdOid: "m", StageOid: "s"}}
	app.Fake.Comments = map[model.Ident][]model.Comment{
		done.Ident():  {comment(utc(2, 40), `/release-now "`+reason+`"`), comment(utc(2, 50), "## Release FAILED: merge refused (release-now)\n\nOutcome: `refused` — stale\n")},
		stale.Ident(): {comment(utc(3, 0).Add(-48*time.Hour), `/release-now "`+reason+`"`)},
	}
	s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(app, s)
	w.AfterApply(app)
	if p, _ := w.progressOf(done); p.Phase != PhaseRefused {
		t.Errorf("the finished one is drawn: %+v", p)
	}
	if p, _ := w.progressOf(stale); p.Phase != PhaseLost {
		t.Errorf("the stale one is drawn lost: %+v", p)
	}
	for _, l := range app.Log {
		if strings.Contains(l, "refused") || strings.Contains(l, "lost track") {
			t.Errorf("nothing from before the session is announced: %v", app.Log)
		}
	}
	if app.Launched != 0 {
		t.Errorf("no launch: %d", app.Launched)
	}
}

// TestTerminalClocksStop: a finished release's UpdatedAt is when it
// finished, whenever it is derived; a lost one is frozen at its timeout.
func TestTerminalClocksStop(t *testing.T) {
	w, _ := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	job := jobAt(13, utcS(3, 0, 30), 1)
	mut := func(tr *track) { tr.run, tr.job = runAt(utc(3, 0), "completed", "success"), job }
	a := deriveWith(w, pr, mut, utc(3, 10))
	b := deriveWith(w, pr, mut, utc(4, 0))
	if a.Phase != PhaseReleased || !a.UpdatedAt.Equal(job.CompletedAt) || !b.UpdatedAt.Equal(a.UpdatedAt) {
		t.Errorf("released at the job's end: %v %v (job %v)", a.UpdatedAt, b.UpdatedAt, job.CompletedAt)
	}
	posted := func(tr *track) { tr.nowPostedAt = utc(3, 0) }
	l1 := deriveWith(w, pr, posted, utc(5, 0))
	l2 := deriveWith(w, pr, posted, utc(9, 0))
	if l1.Phase != PhaseLost || l1.Detail != l2.Detail || !l1.UpdatedAt.Equal(l2.UpdatedAt) {
		t.Errorf("lost is frozen: %+v / %+v", l1, l2)
	}
}

// TestProgressRace: the refresh goroutine, the poll goroutine and the
// renderers run at once (one refresh at a time, as the dashboard runs
// them; the fake hands out the same PR objects every time, so two
// refreshes at once would race on the PRs' own maps, which GraphQL never
// shares between refreshes).
func TestProgressRace(t *testing.T) {
	w, app, pr := pollFixture(t)
	app.Fake.FileRuns = map[string][]model.WorkflowRun{"acme/infra:apply.yml": {*runAt(utcS(2, 59, 40), "in_progress", "")}}
	app.Fake.Jobs = map[int64][]model.WorkflowJob{91: {*jobAt(6, utcS(2, 59, 50), 1)}}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			if s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil); err == nil {
				_ = s
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			if patch, err := w.Poll(context.Background(), app.Fake); err == nil && patch != nil {
				_ = fmt.Sprint(len(patch.(map[model.Ident]*pollPatch)))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			w.progressOf(pr)
			w.progressChip(Progress{Phase: PhaseMerging, Since: utc(3, 0)}, utc(3, 1))
			w.Live(app)
		}
	}()
	wg.Wait()
}

// TestQueuedByApproval: with approval_queues_for_window the approving
// review queues a release (the conductor labels nothing before the
// freeze); a hotfix, a draft, a stale approval or a release in flight
// is not queued, and without the window nothing is.
func TestQueuedByApproval(t *testing.T) {
	patchNow(t, utc(3, 0))
	w, app := newWF(t, true)
	rel := approved(makeRelease(relOpts{}), "carol")
	rel.Opinions.Nodes[0].SubmittedAt = utc(1, 0)
	hot := approved(makeRelease(relOpts{kind: KindHotfix, number: 45}), "carol")
	draft := approved(makeRelease(relOpts{number: 46, draft: true}), "carol")
	stale := approved(makeRelease(relOpts{number: 47}), "carol")
	stale.Opinions.Nodes[0].SubmittedAt = utc(3, 0).Add(-4 * 24 * time.Hour)
	plain := makeRelease(relOpts{number: 48})
	if !w.queued(rel) || w.queued(hot) || w.queued(draft) || w.queued(stale) || w.queued(plain) {
		t.Errorf("queued: rel=%v hot=%v draft=%v stale=%v plain=%v", w.queued(rel), w.queued(hot), w.queued(draft), w.queued(stale), w.queued(plain))
	}
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{rel, hot, draft, stale, plain}, ProdOid: "m", StageOid: "s"}}
	s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := s.(*State)
	w.Apply(app, st)
	if q := st.QueuedForWindow(); len(q) != 1 || q[0].Number != 42 {
		t.Errorf("QueuedForWindow lists the approved release only: %v", q)
	}
	rows := w.Sections(app, nil, "alice")
	for _, r := range rows {
		if p, ok := r.Item.(*model.PR); ok && p.Number == 42 {
			t.Error("a queued release leaves the releases section for the night's")
		}
	}
	// released NOW: in flight, back in the releases section with its rail
	w.notePosted(st.Find(rel.Ident()), utc(3, 0), true)
	if w.queued(st.Find(rel.Ident())) || len(st.QueuedForWindow()) != 0 {
		t.Error("a release in flight is not queued")
	}
	// the window off: approvals queue nothing
	w2, _ := newWF(t, false)
	if w2.queued(rel) {
		t.Error("no window, no queue")
	}
	w3, _ := newWF(t, true)
	w3.cfg.ApprovalQueuesForWindow = false
	if w3.queued(rel) {
		t.Error("approval_queues_for_window = false: the label alone queues")
	}
}

// TestAnnouncePending: a held deploy is a notice pointing at D, not a
// failure and not a launch.
func TestAnnouncePending(t *testing.T) {
	w, app, pr := pollFixture(t)
	app.Fake.Comments[pr.Ident()] = append(app.Fake.Comments[pr.Ident()], comment(utc(3, 5), "## Release merged; the deploy is WAITING on an environment approval (release-now)\n\nOutcome: `deploy_pending` — migrate.yml waits on prod-migrations\nMerge: "+mergedSHA+"\nRun: https://x/runs/91\n"))
	clock.Now = func() time.Time { return utc(3, 6) }
	poll(t, w, app)
	poll(t, w, app)
	notes := 0
	for _, l := range app.Log {
		if strings.HasPrefix(l, "fail: ") {
			t.Errorf("a held deploy is not a failure: %q", l)
		}
		if strings.Contains(l, "waits on an environment approval") {
			notes++
			if !strings.Contains(l, "prod-migrations") || !strings.Contains(l, "press D") {
				t.Errorf("the notice names the environment and D: %q", l)
			}
		}
	}
	if notes != 1 || app.Launched != 0 {
		t.Errorf("one notice, no launch: %d %d\n%v", notes, app.Launched, app.Log)
	}
}
