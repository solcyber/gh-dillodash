package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/external"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The x key: what the Actions page offers on a run, from the dashboard.
// The runs come from the workflows first (a release's run in another
// repo, a waiting run's row), then the runs on the selected or open PR's
// commit (its head, or its merge once merged). Every write names the run
// and asks first.

// maxRunChoices bounds the run menu: the newest runs on a commit.
const maxRunChoices = 12

// runTargets is every run x may act on for the selection or the open PR.
func (m *Model) runTargets(app *lockedApp) []workflow.RunTarget {
	out := m.wf.RunTargets(app)
	seen := map[int64]bool{}
	for _, t := range out {
		seen[t.Run.ID] = true
	}
	var pr *model.PR
	if app.CurrentView() == ViewDetail {
		pr, _ = app.Detail().(*model.PR)
	} else if row, ok := app.Selected(); ok {
		pr, _ = row.Item.(*model.PR)
	}
	if pr == nil {
		return out
	}
	repo := m.repoOf(pr)
	sha, on := pr.HeadRefOid, "head"
	if pr.State == "MERGED" {
		if s := mergeSHA(pr); s != "" {
			sha, on = s, "merge"
		} else if l := app.DetailListed(); l != nil && l.Number == pr.Number && mergeSHA(l) != "" {
			sha, on = mergeSHA(l), "merge"
		}
	}
	if sha == "" {
		return out
	}
	app.Flash(fmt.Sprintf("reading the runs on %s's %s %s...", m.where(repo, pr.Number), on, model.ShortSHA(sha)))
	runs, err := m.gh.WorkflowRuns(m.ctx, repo, sha)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read the runs on %s: %s", model.ShortSHA(sha), err))
		return out
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	for _, r := range runs {
		if seen[r.ID] || len(out) >= maxRunChoices {
			continue
		}
		seen[r.ID] = true
		out = append(out, workflow.RunTarget{Repo: repo, Run: r, Why: "on the " + on + " of " + m.where(repo, pr.Number)})
	}
	return out
}

// runState is a run's state as one word: its conclusion once complete.
func runState(r model.WorkflowRun) string {
	if r.Status == "completed" && r.Conclusion != "" {
		return r.Conclusion
	}
	return text.OrQ(r.Status)
}

// runLabel is a run's line in the menu.
func runLabel(t workflow.RunTarget) string {
	return fmt.Sprintf("%s · %s · run %d in %s — %s", text.Ellip(text.OrQ(t.Run.Name), 30), runState(t.Run), t.Run.ID, t.Repo.Name, t.Why)
}

// Run actions, in the menu's order.
const (
	runRerunFailed = "re-run the failed jobs"
	runRerunAll    = "re-run every job"
	runCancel      = "cancel the run"
	runReject      = "reject the deployment it waits on"
	runLogs        = "read a job's log in $PAGER"
)

// runActions is what x offers on a run in its state.
func runActions(r model.WorkflowRun) []string {
	var out []string
	switch r.Status {
	case "completed":
		switch r.Conclusion {
		case "failure", "cancelled", "timed_out", "startup_failure":
			out = append(out, runRerunFailed)
		}
		out = append(out, runRerunAll)
	case "waiting":
		out = append(out, runReject, runCancel)
	default: // queued, in_progress, requested, pending
		out = append(out, runCancel)
	}
	return append(out, runLogs)
}

// doRuns is the x key.
func (m *Model) doRuns(app *lockedApp) {
	targets := m.runTargets(app)
	if len(targets) == 0 {
		app.Notice("x acts on Actions runs: select or open a PR, a release or a waiting run that has some")
		return
	}
	t := targets[0]
	if len(targets) > 1 {
		labels := make([]string, len(targets))
		for i, tt := range targets {
			labels[i] = runLabel(tt)
		}
		i := app.Pick("which run?", labels)
		if i < 0 || i >= len(targets) {
			app.Notice("cancelled")
			return
		}
		t = targets[i]
	}
	acts := runActions(t.Run)
	i := app.Pick(runLabel(t), acts)
	if i < 0 || i >= len(acts) {
		app.Notice("cancelled")
		return
	}
	what := fmt.Sprintf("run %d (%s) in %s", t.Run.ID, text.OrQ(t.Run.Name), t.Repo)
	switch acts[i] {
	case runLogs:
		m.runLogs(app, t)
		return
	}
	if m.writeRefused(app, t.Repo, acts[i]+" of "+what) {
		return
	}
	switch acts[i] {
	case runReject:
		m.rejectRun(app, t, what)
		return
	}
	if !app.Confirm(fmt.Sprintf("%s: %s as %s?", acts[i], what, m.me)) {
		app.Notice("cancelled")
		return
	}
	var err error
	switch acts[i] {
	case runRerunFailed:
		err = m.gh.RerunFailedJobs(m.ctx, t.Repo, t.Run.ID)
	case runRerunAll:
		err = m.gh.RerunRun(m.ctx, t.Repo, t.Run.ID)
	case runCancel:
		err = m.gh.CancelRun(m.ctx, t.Repo, t.Run.ID)
	}
	if err != nil {
		app.Fail(fmt.Sprintf("%s of %s failed: %s%s", acts[i], what, err, actionsHint(err, t.Repo)))
		return
	}
	done := map[string]string{runRerunFailed: "re-ran the failed jobs of", runRerunAll: "re-ran", runCancel: "cancelled"}[acts[i]]
	app.OK(fmt.Sprintf("%s %s", done, what))
	app.Refresh(true)
	app.RefreshSoon(m.deployRefresh())
}

// actionsHint names the token permission a refused Actions write needs.
func actionsHint(err error, repo model.Repo) string {
	if strings.Contains(err.Error(), "not accessible by personal access token") || strings.Contains(err.Error(), "403") {
		return " — your token needs Actions: Read and write on " + repo.Name
	}
	return ""
}

// rejectRun rejects the environments a waiting run waits on that the
// viewer reviews: the run fails there and deploys nothing.
func (m *Model) rejectRun(app *lockedApp, t workflow.RunTarget, what string) {
	pend, err := m.gh.PendingDeployments(m.ctx, t.Repo, t.Run.ID)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read the environments of %s: %s", what, err))
		return
	}
	var ids []int64
	var names []string
	for _, p := range pend {
		if p.CurrentUserCanApprove {
			ids = append(ids, p.Environment.ID)
			names = append(names, text.OrQ(p.Environment.Name))
		}
	}
	if len(ids) == 0 {
		app.Notice(fmt.Sprintf("%s waits on nothing %s reviews", what, m.me))
		return
	}
	reason, ok := app.Prompt(fmt.Sprintf("Reject %s of %s? The reason goes on the run (Esc cancels):", strings.Join(names, ", "), what), workflow.PromptOpts{})
	if !ok {
		app.Notice("cancelled")
		return
	}
	comment := strings.TrimSpace(reason)
	if comment == "" {
		comment = "dillodash: rejected by " + m.me
	}
	if !app.Confirm(fmt.Sprintf("reject %s of %s as %s? The run fails there and deploys nothing.", strings.Join(names, ", "), what, m.me)) {
		app.Notice("cancelled")
		return
	}
	if err := m.gh.RejectDeployments(m.ctx, t.Repo, t.Run.ID, ids, comment); err != nil {
		app.Fail(fmt.Sprintf("rejecting %s failed: %s", what, err))
		return
	}
	app.OK(fmt.Sprintf("rejected %s of %s", strings.Join(names, ", "), what))
	app.Refresh(true)
}

// runLogs shows one job's log in $PAGER: the failed job when there is
// exactly one, else the person picks.
func (m *Model) runLogs(app *lockedApp, t workflow.RunTarget) {
	app.Flash(fmt.Sprintf("reading the jobs of run %d...", t.Run.ID))
	jobs, err := m.gh.RunJobs(m.ctx, t.Repo, t.Run.ID)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read the jobs of run %d: %s", t.Run.ID, err))
		return
	}
	if len(jobs) == 0 {
		app.Notice(fmt.Sprintf("run %d has no jobs yet", t.Run.ID))
		return
	}
	// failed jobs first
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Conclusion == "failure" && jobs[j].Conclusion != "failure" })
	job := jobs[0]
	failed := 0
	for _, j := range jobs {
		if j.Conclusion == "failure" {
			failed++
		}
	}
	if failed != 1 && len(jobs) > 1 {
		labels := make([]string, len(jobs))
		for i, j := range jobs {
			st := j.Status
			if j.Status == "completed" {
				st = j.Conclusion
			}
			labels[i] = fmt.Sprintf("%s · %s", text.Ellip(text.OrQ(j.Name), 50), st)
		}
		i := app.Pick(fmt.Sprintf("which job of run %d?", t.Run.ID), labels)
		if i < 0 || i >= len(jobs) {
			app.Notice("cancelled")
			return
		}
		job = jobs[i]
	}
	app.Flash(fmt.Sprintf("fetching the log of %s...", text.OrQ(job.Name)))
	log, err := m.gh.JobLogs(m.ctx, t.Repo, job.ID)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't fetch the log of %s: %s", text.OrQ(job.Name), err))
		return
	}
	f, err := os.CreateTemp("", fmt.Sprintf("dillodash-run-%d-*.log", t.Run.ID))
	if err != nil {
		app.Fail("couldn't write the log: " + err.Error())
		return
	}
	path := f.Name()
	defer os.Remove(path)
	_, werr := f.WriteString(strings.ToValidUTF8(log, "�"))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		app.Fail("couldn't write the log: " + werr.Error())
		return
	}
	pager := external.PagerCommand()
	if err := app.RunExternal(append(pager, path)); err != nil {
		app.Fail(fmt.Sprintf("couldn't start the pager (%s): %s", pager[0], err))
		return
	}
	m.mu.Lock()
	if m.statusKind == "spin" {
		m.status = ""
	}
	m.mu.Unlock()
}
