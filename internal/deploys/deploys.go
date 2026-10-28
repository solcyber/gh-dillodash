// Package deploys reads what the Actions runs on a merged PR's merge
// commit are doing: which environments wait for an approval (and
// whether the viewer can give it), what is deploying, what finished.
package deploys

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// StuckAfter is how long a queued or running deploy may take before the
// row says "stuck?".
const StuckAfter = 45 * time.Minute

// RefreshEvery is how often the dashboard refreshes while a deploy waits
// or runs, so the next environment's approval shows up by itself.
const RefreshEvery = 30 * time.Second

// Chip is the chip style per phase.
var Chip = map[string]string{
	"waiting": "chip.await", "failed": "chip.rolled", "stuck": "chip.rolled",
	"running": "chip.queued", "done": "chip.approved", "cancelled": "chip.state",
	"none": "chip.state",
}

// Wait is one environment a run waits on.
type Wait struct {
	Run     model.WorkflowRun
	EnvID   int64
	EnvName string
	Mine    bool // the viewer can approve it
}

// Row is one run for the card: its name, what it is doing, a chip style.
type Row struct {
	Name  string
	What  string
	Style string
	RunID int64
}

// State is the merge commit's deploy state.
type State struct {
	SHA   string
	Phase string // waiting | failed | stuck | running | done | cancelled | none
	Text  string // the row's chip
	Waits []Wait
	Runs  []Row
}

// Mine lists the environment names the viewer can approve.
func (s State) Mine() []string {
	set := map[string]bool{}
	for _, w := range s.Waits {
		if w.Mine {
			set[w.EnvName] = true
		}
	}
	return sortedKeys(set)
}

// Envs lists every waiting environment name.
func (s State) Envs() []string {
	set := map[string]bool{}
	for _, w := range s.Waits {
		set[w.EnvName] = true
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RefreshInterval is the deploy watch interval for a configured number
// of seconds (deploy_refresh_secs); zero or less means RefreshEvery.
func RefreshInterval(secs int) time.Duration {
	if secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return RefreshEvery
}

// Compute is the pure part: runs on the sha, and the pending deployments
// of the runs that wait. Phase priority: waiting, failed, stuck, running,
// done, cancelled, none (a cancelled run beside a successful one is
// usually superseded). A run queued or running longer than StuckAfter
// is stuck; ComputeWith takes the threshold.
func Compute(runs []model.WorkflowRun, pending map[int64][]model.PendingDeployment, now time.Time) State {
	return ComputeWith(runs, pending, now, StuckAfter)
}

// ComputeWith is Compute with the stuck threshold given (zero or less
// means StuckAfter).
func ComputeWith(runs []model.WorkflowRun, pending map[int64][]model.PendingDeployment, now time.Time, stuckAfter time.Duration) State {
	if stuckAfter <= 0 {
		stuckAfter = StuckAfter
	}
	sorted := append([]model.WorkflowRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var st State
	phases := map[string]bool{}
	var stuckFor time.Duration
	for _, r := range sorted {
		name := r.Name
		if name == "" {
			name = "?"
		}
		age := time.Duration(0)
		if !r.CreatedAt.IsZero() {
			age = now.Sub(r.CreatedAt)
		}
		switch {
		case r.Status == "waiting":
			var envs []string
			for _, p := range pending[r.ID] {
				en := p.Environment.Name
				if en == "" {
					en = "?"
				}
				envs = append(envs, en)
				st.Waits = append(st.Waits, Wait{Run: r, EnvID: p.Environment.ID, EnvName: en, Mine: p.CurrentUserCanApprove})
			}
			phases["waiting"] = true
			what := "waiting for an approval"
			if len(envs) > 0 {
				what = "waiting for " + strings.Join(envs, ", ")
			}
			st.Runs = append(st.Runs, Row{name, what, "chip.await", r.ID})
		case r.Status == "queued" || r.Status == "in_progress" || r.Status == "requested" || r.Status == "pending":
			status := strings.ReplaceAll(r.Status, "_", " ")
			if age > stuckAfter {
				phases["stuck"] = true
				if age > stuckFor {
					stuckFor = age
				}
				st.Runs = append(st.Runs, Row{name, fmt.Sprintf("%s %s - stuck?", status, FmtSecs(age)), "chip.rolled", r.ID})
			} else {
				phases["running"] = true
				st.Runs = append(st.Runs, Row{name, fmt.Sprintf("%s %s", status, FmtSecs(age)), "chip.queued", r.ID})
			}
		case r.Conclusion == "success" || r.Conclusion == "skipped" || r.Conclusion == "neutral":
			phases["done"] = true
			st.Runs = append(st.Runs, Row{name, r.Conclusion, "chip.approved", r.ID})
		case r.Conclusion == "cancelled":
			phases["cancelled"] = true
			st.Runs = append(st.Runs, Row{name, r.Conclusion, "chip.state", r.ID})
		default:
			phases["failed"] = true
			what := r.Conclusion
			if what == "" {
				what = r.Status
			}
			if what == "" {
				what = "?"
			}
			st.Runs = append(st.Runs, Row{name, what, "chip.rolled", r.ID})
		}
	}
	switch {
	case phases["waiting"]:
		st.Phase = "waiting"
		if mine := st.Mine(); len(mine) > 0 {
			st.Text = "approve: " + strings.Join(mine, ", ")
		} else {
			st.Text = "waits: " + strings.Join(st.Envs(), ", ")
		}
	case phases["failed"]:
		st.Phase, st.Text = "failed", "deploy failed"
	case phases["stuck"]:
		st.Phase, st.Text = "stuck", "stuck? "+FmtSecs(stuckFor)
	case phases["running"]:
		st.Phase, st.Text = "running", "deploying"
	case phases["done"]:
		st.Phase, st.Text = "done", "deployed"
	case phases["cancelled"]:
		st.Phase, st.Text = "cancelled", "deploy cancelled"
	default:
		// repos that deploy outside Actions: nothing to show
		st.Phase, st.Text = "none", "no Actions runs"
	}
	return st
}

// FmtSecs is 12s, 7m, 3h05m: a run's age.
func FmtSecs(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	}
	return fmt.Sprintf("%dh%02dm", secs/3600, (secs%3600)/60)
}

// Cache remembers settled deploy states briefly: a deploy that has
// settled (done or failed) is re-read only every two minutes (a re-run
// brings it back); Fetch with fresh=true always reads (the D key).
type Cache struct {
	// StuckAfter is the stuck threshold Fetch computes with; zero means
	// the package default. The release config's deploy_stuck_secs.
	StuckAfter time.Duration

	mu    sync.Mutex
	items map[string]cached
}

// Stuck is the threshold in effect.
func (c *Cache) Stuck() time.Duration {
	if c == nil || c.StuckAfter <= 0 {
		return StuckAfter
	}
	return c.StuckAfter
}

type cached struct {
	at    time.Time
	state State
}

// Fetch reads the runs on sha and the pending deployments of the ones
// that wait.
func (c *Cache) Fetch(ctx context.Context, gh ghapi.Client, repo model.Repo, sha string, fresh bool, now time.Time) (State, error) {
	if c != nil && !fresh {
		c.mu.Lock()
		hit, ok := c.items[sha]
		c.mu.Unlock()
		if ok && (hit.state.Phase == "done" || hit.state.Phase == "failed" || hit.state.Phase == "cancelled") && now.Sub(hit.at) < 2*time.Minute {
			return hit.state, nil
		}
	}
	runs, err := gh.WorkflowRuns(ctx, repo, sha)
	if err != nil {
		return State{}, err
	}
	pending := map[int64][]model.PendingDeployment{}
	for _, r := range runs {
		if r.Status == "waiting" {
			p, err := gh.PendingDeployments(ctx, repo, r.ID)
			if err != nil {
				return State{}, err
			}
			pending[r.ID] = p
		}
	}
	st := ComputeWith(runs, pending, now, c.Stuck())
	st.SHA = sha
	if c != nil {
		c.mu.Lock()
		if c.items == nil {
			c.items = map[string]cached{}
		}
		c.items[sha] = cached{now, st}
		c.mu.Unlock()
	}
	return st, nil
}

// Forget drops a sha from the cache (after an approval).
func (c *Cache) Forget(sha string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.items, sha)
	c.mu.Unlock()
}

// TokenHint names the permission a 403 points at.
func TokenHint(err error, perm string, repo model.Repo) string {
	if e, ok := ghapi.AsError(err); ok && e.Status == 403 {
		return fmt.Sprintf(" - your token needs %s: Read and write on %s", perm, repo.Name)
	}
	return ""
}
