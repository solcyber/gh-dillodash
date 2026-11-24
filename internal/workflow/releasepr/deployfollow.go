package releasepr

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/keymap"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// A merged release's deploy moves without anyone touching the
// dashboard: its runs start, automation approves an environment it
// reviews, a run finishes. Those moves are followed by the poll, which
// reads only the runs on the merge commit (and the pending deployments
// of a run that waits): conditional REST reads that cost nothing while
// nothing changed. A whole refresh (every list, search and manifest
// read) follows only when the deploy's phase changes.
//
// What is followed is what moves on its own: a deploy that runs, and a
// wait automation still has in its grace. A wait that is the viewer's
// moves when the viewer presses D (which reads it again); a wait on
// somebody else, or a deploy stuck or finished, is left to the refresh.

// followDeploy reports whether a merged release's deploy is worth the
// poll's attention.
func followDeploy(dep deploys.State) bool {
	switch dep.Phase {
	case "running":
		return true
	case "waiting":
		return len(dep.Due()) == 0 && len(dep.Left()) > 0
	}
	return false
}

type followJob struct {
	id   model.Ident
	repo model.Repo
	sha  string
	old  deploys.State
}

// noteFollow (UI goroutine) takes the snapshot of what the poll follows
// from the applied state: the poll goroutine reads the snapshot, never
// the PRs, whose annotations the UI goroutine writes.
func (w *Workflow) noteFollow(st *State) {
	var jobs []followJob
	if st != nil {
		for _, pr := range st.Releases {
			dep, ok := pr.Get(ExtDeploy).(deploys.State)
			if !ok || pr.State != "MERGED" || MergeSHA(pr) == "" || !followDeploy(dep) {
				continue
			}
			jobs = append(jobs, followJob{id: pr.Ident(), repo: pr.Repo, sha: MergeSHA(pr), old: dep})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].id.String() < jobs[j].id.String() })
	w.mu.Lock()
	w.follow = jobs
	w.mu.Unlock()
}

// deployJobs are the releases whose deploy the poll follows now (a copy
// of the snapshot).
func (w *Workflow) deployJobs() []followJob {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]followJob(nil), w.follow...)
}

// followsDeploys reports whether any release's deploy is followed.
func (w *Workflow) followsDeploys() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.follow) > 0
}

// pollDeploys reads each followed deploy again into its patch.
func (w *Workflow) pollDeploys(ctx context.Context, gh ghapi.Client, patches map[model.Ident]*pollPatch) error {
	now := clock.Now()
	for _, j := range w.deployJobs() {
		st, err := w.deploy.Fetch(ctx, gh, j.repo, j.sha, true, now)
		if err != nil {
			if ghapi.IsRateLimit(err) {
				return err
			}
			continue // the refresh says what is wrong; the poll keeps the last state
		}
		p := patches[j.id]
		if p == nil {
			p = &pollPatch{}
			patches[j.id] = p
		}
		p.deploy = &st
	}
	return nil
}

// applyDeploy lays a polled deploy state on its release (lock not held)
// and reports whether its phase moved.
func applyDeploy(pr *model.PR, st deploys.State) bool {
	old, _ := pr.Get(ExtDeploy).(deploys.State)
	pr.Set(ExtDeploy, st)
	return old.Phase != st.Phase || len(old.Due()) != len(st.Due())
}

// deployMovedRefresh is how soon a refresh follows a deploy whose phase
// moved: the release is done, failed or someone's turn, and the rest of
// the screen (the release's chips, the summary) follows it.
const deployMovedRefresh = 2 * time.Second

// announceDeploys says once per environment when a merged release's
// deploy starts waiting for the viewer (an environment automation
// approves is not announced until automation has left it past the grace).
func (w *Workflow) announceDeploys(app workflow.App, st *State) {
	for _, pr := range st.Releases {
		dep, ok := pr.Get(ExtDeploy).(deploys.State)
		if !ok {
			continue
		}
		for _, wt := range dep.Waits {
			if !wt.Due {
				continue
			}
			key := seenKey{pr.Repo.Name, dep.SHA, wt.EnvName}
			w.mu.Lock()
			seen := w.seen[key]
			w.seen[key] = true
			w.mu.Unlock()
			if !seen {
				app.Next(fmt.Sprintf("%s#%d: %s now waits for your approval — press %s on its row", pr.Repo.Name, pr.Number, wt.EnvName, keymap.S(keymap.Deploys)))
			}
		}
	}
}
