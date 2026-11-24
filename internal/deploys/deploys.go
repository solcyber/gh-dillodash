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
	"waiting": "chip.await", "failed": "chip.fail", "stuck": "chip.fail",
	"running": "chip.queued", "done": "chip.released", "cancelled": "chip.state",
	"none": "chip.state",
}

// AutoGrace is how long an environment that automation approves may
// wait before it is put to the viewer as theirs to approve.
const AutoGrace = 5 * time.Minute

// GraceOf is the grace for a configured number of seconds: zero is no
// grace at all, so a config can say "never leave a wait to automation".
func GraceOf(secs int) time.Duration {
	if secs <= 0 {
		return -1
	}
	return time.Duration(secs) * time.Second
}

// Wait is one environment a run waits on.
type Wait struct {
	Run     model.WorkflowRun
	EnvID   int64
	EnvName string
	Mine    bool // the viewer can approve it
	// Auto: one of the environment's reviewers is a bot login, so
	// automation is expected to approve it. By names the first one.
	Auto bool
	By   string
	// Left: automation reviews it and still has its grace, so it is
	// left alone, whoever is looking.
	Left bool
	// Due: it is the viewer's to approve now: Mine and not Left.
	Due bool
	// For is how long the run has waited.
	For time.Duration
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

// Due lists the environment names that are the viewer's to approve now
// (see Wait.Due).
func (s State) Due() []string {
	set := map[string]bool{}
	for _, w := range s.Waits {
		if w.Due {
			set[w.EnvName] = true
		}
	}
	return sortedKeys(set)
}

// Left lists the waiting environments left to automation: it reviews
// them and still has its grace.
func (s State) Left() []string {
	set := map[string]bool{}
	for _, w := range s.Waits {
		if w.Left {
			set[w.EnvName] = true
		}
	}
	return sortedKeys(set)
}

// AllLeft reports the deploy waits and every wait is left to automation:
// nothing is anyone's to press yet.
func (s State) AllLeft() bool {
	for _, w := range s.Waits {
		if !w.Left {
			return false
		}
	}
	return len(s.Waits) > 0
}

// LeftBy names who approves the waits left to automation ("" when none).
func (s State) LeftBy() string {
	for _, w := range s.Waits {
		if w.Left && w.By != "" {
			return w.By
		}
	}
	return ""
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

// compute is the pure part: runs on the sha, and the pending deployments
// of the runs that wait. Phase priority: waiting, failed, stuck, running,
// done, cancelled, none (a canceled run beside a successful one is
// usually superseded). A run queued or running longer than StuckAfter
// is stuck; ComputeWith takes the threshold.
func compute(runs []model.WorkflowRun, pending map[int64][]model.PendingDeployment, now time.Time) State {
	return ComputeWith(runs, pending, now, Options{})
}

// Options are what ComputeWith reads besides the runs.
type Options struct {
	// StuckAfter is the stuck threshold (zero or less means StuckAfter).
	StuckAfter time.Duration
	// IsBot says a reviewer login is automation; nil means nobody is,
	// and every wait the viewer can approve is due at once.
	IsBot func(login string) bool
	// AutoGrace is how long automation gets before its wait is put to
	// the viewer (zero means AutoGrace; negative means no grace at all).
	AutoGrace time.Duration
}

// ComputeWith is compute with its options given.
func ComputeWith(runs []model.WorkflowRun, pending map[int64][]model.PendingDeployment, now time.Time, o Options) State {
	stuckAfter := o.StuckAfter
	if stuckAfter <= 0 {
		stuckAfter = StuckAfter
	}
	grace := o.AutoGrace
	if grace == 0 {
		grace = AutoGrace
	}
	sorted := append([]model.WorkflowRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var st State
	phases := map[string]bool{}
	var stuckFor time.Duration
	for _, r := range sorted {
		if r.Event == "dynamic" {
			// GitHub's own runs (code scanning default setup, Dependabot)
			// ride the merge commit but deploy nothing
			continue
		}
		name := r.Name
		if name == "" {
			name = "?"
		}
		// age is this attempt's (a re-run resets it); idle is the time
		// since the run last changed, so a run that waited a night for
		// an approval and started a minute ago is not "stuck"
		start := latest(r.CreatedAt, r.RunStartedAt)
		age, idle := time.Duration(0), time.Duration(0)
		if !start.IsZero() {
			age = now.Sub(start)
			idle = now.Sub(latest(start, r.UpdatedAt))
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
				wt := Wait{Run: r, EnvID: p.Environment.ID, EnvName: en, Mine: p.CurrentUserCanApprove, For: idle}
				if o.IsBot != nil {
					for _, rv := range p.Reviewers {
						if rv.Type != "Team" && rv.Reviewer.Login != "" && o.IsBot(rv.Reviewer.Login) {
							wt.Auto, wt.By = true, rv.Reviewer.Login
							break
						}
					}
				}
				// a run with no times can't show how long it waited:
				// never leave it to anyone on that
				wt.Left = wt.Auto && idle < grace && !start.IsZero()
				wt.Due = wt.Mine && !wt.Left
				st.Waits = append(st.Waits, wt)
			}
			phases["waiting"] = true
			what := "waiting for an approval"
			if len(envs) > 0 {
				what = "waiting for " + strings.Join(envs, ", ")
			}
			st.Runs = append(st.Runs, Row{name, what, "chip.await", r.ID})
		case r.Status == "queued" || r.Status == "in_progress" || r.Status == "requested" || r.Status == "pending":
			status := strings.ReplaceAll(r.Status, "_", " ")
			if idle > stuckAfter {
				phases["stuck"] = true
				if idle > stuckFor {
					stuckFor = idle
				}
				st.Runs = append(st.Runs, Row{name, fmt.Sprintf("%s %s - stuck?", status, FmtSecs(age)), Chip["stuck"], r.ID})
			} else {
				phases["running"] = true
				st.Runs = append(st.Runs, Row{name, fmt.Sprintf("%s %s", status, FmtSecs(age)), "chip.queued", r.ID})
			}
		case r.Conclusion == "success" || r.Conclusion == "skipped" || r.Conclusion == "neutral":
			phases["done"] = true
			st.Runs = append(st.Runs, Row{name, r.Conclusion, Chip["done"], r.ID})
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
			st.Runs = append(st.Runs, Row{name, what, Chip["failed"], r.ID})
		}
	}
	switch {
	case phases["waiting"]:
		st.Phase = "waiting"
		// what is the viewer's comes first; a wait automation takes is
		// said as such, so nobody approves it by reflex
		if due := st.Due(); len(due) > 0 {
			st.Text = "approve: " + strings.Join(due, ", ")
		} else if st.AllLeft() {
			st.Text = "waits: " + strings.Join(st.Left(), ", ") + " · automatic"
		} else {
			st.Text = "waits: " + strings.Join(st.Envs(), ", ")
		}
	case phases["stuck"]:
		st.Phase, st.Text = "stuck", "stuck? "+FmtSecs(stuckFor)
		if phases["failed"] {
			st.Text += " · a run failed"
		}
	case phases["running"]:
		// a run still going outranks a failed one: the release is not
		// over (a failed lint on the merge commit must not read as a
		// failed release while the deploy runs); the text says both
		st.Phase, st.Text = "running", "deploying"
		if phases["failed"] {
			st.Text = "deploying · a run failed"
		}
	case phases["failed"]:
		st.Phase, st.Text = "failed", "deploy failed"
	case phases["done"]:
		st.Phase, st.Text = "done", "deployed"
	case phases["cancelled"]:
		st.Phase, st.Text = "cancelled", "deploy canceled"
	default:
		// repos that deploy outside Actions: nothing to show
		st.Phase, st.Text = "none", "no Actions runs"
	}
	return st
}

// FmtSecs is 12s, 7m, 3h05m: a run's age (the release progress clocks
// use it too).
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
	// IsBot and AutoGrace are Options' (the [bots] logins, and the
	// release config's deploy_auto_grace_secs).
	IsBot     func(login string) bool
	AutoGrace time.Duration

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

func (c *Cache) options() Options {
	if c == nil {
		return Options{}
	}
	return Options{StuckAfter: c.Stuck(), IsBot: c.IsBot, AutoGrace: c.AutoGrace}
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
	st := ComputeWith(runs, pending, now, c.options())
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

// latest is the later of two times (a zero time loses).
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
