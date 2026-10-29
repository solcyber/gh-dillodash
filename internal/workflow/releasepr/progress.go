package releasepr

// A release in flight is followed from the moment it is sent on its way
// (a /release-now command, or the window's slot) to its outcome. The
// signals are spread over two repos: the release repo carries the PR
// (its labels, the outcome comment the release job leaves, the merge),
// the manifest repo carries the run that does the work (apply.yml's
// "app release" job, whose steps say where it stands) and, on a window
// night, the manifest issue with the item's status. The tracker keeps
// one record per release on the workflow, under its own lock: the
// refresh goroutine and the poll goroutine hand it new values, the UI
// goroutine swaps them in, and derive turns the pile into one phase.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The phases of a release in flight, in the order they happen; the
// terminal ones end the tracking (a newer run reopens it).
const (
	PhaseScheduled   = "scheduled"        // window:scheduled, the slot ahead
	PhaseWaitingSlot = "waiting for slot" // the manifest item waits (terraform items, /release), or the slot passed with no run
	PhaseDispatched  = "dispatched"       // the command is posted (or the manifest says dispatched), no run seen yet
	PhaseSetup       = "setup"            // the run's steps before the validation
	PhaseValidating  = "validating"
	PhaseMerging     = "merging"
	PhaseApproving   = "approving envs"
	PhaseDeploying   = "deploying"
	PhaseVerifying   = "verifying"
	PhaseFinishing   = "finishing"

	PhaseReleased     = "released"
	PhaseFailed       = "failed"
	PhaseRefused      = "refused"
	PhaseUnverifiable = "unverifiable"
	PhaseNotRun       = "not run"
	PhaseRolled       = "rolled"
	PhaseCancelled    = "cancelled"
	PhaseLost         = "lost track"
)

// runPhases is the order of the phases a run walks through: the rail's
// segments.
var runPhases = []string{PhaseSetup, PhaseValidating, PhaseMerging, PhaseApproving, PhaseDeploying, PhaseVerifying, PhaseFinishing}

// terminalPhases end the tracking.
var terminalPhases = map[string]bool{
	PhaseReleased: true, PhaseFailed: true, PhaseRefused: true, PhaseUnverifiable: true,
	PhaseNotRun: true, PhaseRolled: true, PhaseCancelled: true, PhaseLost: true,
}

// Terminal reports a phase that ends the tracking.
func Terminal(phase string) bool { return terminalPhases[phase] }

// PhaseIndex is a run phase's place on the rail (0..len-1), -1 for the
// phases before the run and the terminal ones.
func PhaseIndex(phase string) int {
	for i, p := range runPhases {
		if p == phase {
			return i
		}
	}
	return -1
}

// stepPhases maps a step's name, by prefix, to the phase it belongs to.
// Anything unmatched is setup. The names are the release job's, as its
// workflow file writes them; the match is a prefix so a wording tweak
// after the first words does not lose the phase.
var stepPhases = []struct{ prefix, phase string }{
	{"re-validate", PhaseValidating},
	{"validate", PhaseValidating},
	{"merge the release pr", PhaseMerging},
	{"merge", PhaseMerging},
	{"approve the environment", PhaseApproving},
	{"wait for the deploy", PhaseDeploying},
	{"verify", PhaseVerifying},
	{"outcome", PhaseFinishing},
	{"write the apply-log", PhaseFinishing},
	{"fail the run unless", PhaseFinishing},
}

// StepPhase is the phase a step name belongs to.
func StepPhase(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, sp := range stepPhases {
		if strings.HasPrefix(n, sp.prefix) {
			return sp.phase
		}
	}
	return PhaseSetup
}

// Step is one step of the release job, with its phase.
type Step struct {
	Name, Phase, Status, Conclusion string
	StartedAt, CompletedAt          time.Time
}

// Progress is where a release stands: what the renderers read.
type Progress struct {
	Phase    string
	Terminal bool
	Since    time.Time // when the current phase began (the chip's elapsed)
	Started  time.Time // when the release was sent on its way (the T+ clock)
	Detail   string    // the step name, the manifest status, the outcome detail, the refusal reason
	SlotAt   time.Time // the window slot, when known

	RunID      int64
	RunAttempt int
	RunURL     string
	Steps      []Step

	Outcome   string // verified | refused | apply_failed | deploy_failed | unverifiable | revert_failed | not_run
	FailPhase string // the run phase a failed run died in, when known
	MergeSHA  string
	Approver  string // from the outcome comment, when it says
	Night     string // the window night, when on one

	DeployDone, DeployTotal int // the app repo's runs on the merge, from the deploy state

	Source    string // comment | label | run | manifest | command | deploy
	PollErr   string // the last poll's failure and what happens next, shown in the card
	UpdatedAt time.Time
}

// Elapsed is how long the current phase has run at now.
func (p Progress) Elapsed(now time.Time) time.Duration {
	if p.Since.IsZero() {
		return 0
	}
	return now.Sub(p.Since)
}

// track is one followed release; w.mu guards every field. Values that
// goroutines produce (comments, run, job, item) are replaced whole and
// never mutated in place.
type track struct {
	repo   model.Repo
	number int

	firstSeen   time.Time // when the tracker noticed it
	nowPostedAt time.Time // this session posted the command then
	commentsAt  time.Time // pr.UpdatedAt when comments were last read
	comments    []model.Comment

	run      *model.WorkflowRun
	job      *model.WorkflowJob
	runDone  bool // the final jobs read after completion is in
	infraOff bool // the manifest repo's runs are unreadable this session
	item     *workflow.ManifestItem
	pollErr  string
	notTold  bool // this session posted the command but the dispatch to the manifest repo failed

	cur        Progress
	shownPhase string // the phase the row last drew: a change starts its effects
	sawLive    bool   // in flight at some point this session: its outcome is news
}

// set swaps in a fresh derive. A release seen in flight this session
// earns an announcement when it ends; one already finished when the
// tracker first saw it does not.
func (t *track) set(p Progress) {
	t.cur = p
	if p.Phase != "" && !p.Terminal {
		t.sawLive = true
	}
}

// show starts the row's effects when the phase moved since it was last
// drawn; the first phase drawn starts none.
func (t *track) show(f *fx.Fx, pr *model.PR) {
	if t.cur.Phase != t.shownPhase {
		if t.shownPhase != "" {
			notePhaseChange(f, pr, t.shownPhase, t.cur.Phase)
		}
		t.shownPhase = t.cur.Phase
	}
}

// pollPatch is what one poll cycle learned about one release.
type pollPatch struct {
	run      *model.WorkflowRun // nil: unchanged
	job      *model.WorkflowJob
	comments []model.Comment // nil: not read
	infraOff bool
	err      string
}

// progressOf is the release's progress for the renderers.
func (w *Workflow) progressOf(pr *model.PR) (Progress, bool) {
	if pr == nil {
		return Progress{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.progress[pr.Ident()]
	if !ok || t.cur.Phase == "" {
		return Progress{}, false
	}
	return t.cur, true
}

// trackFor returns the release's record, making one when make is set.
func (w *Workflow) trackFor(pr *model.PR, mk bool, now time.Time) *track {
	id := pr.Ident()
	t, ok := w.progress[id]
	if !ok && mk {
		t = &track{repo: pr.Repo, number: pr.Number, firstSeen: now}
		w.progress[id] = t
	}
	return t
}

// notePosted records that this session posted the release-now command
// on the PR just now: the release is in flight from here. told is false
// when the dispatch to the manifest repo failed, so the card says
// nothing will pick it up on its own.
func (w *Workflow) notePosted(pr *model.PR, now time.Time, told bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.trackFor(pr, true, now)
	t.nowPostedAt, t.notTold = now, !told
	t.set(w.derive(pr, t, now))
}

// ---- refresh goroutine ----

// noteTracks (worker goroutine) decides which releases of the fresh
// state are followed and reads their comments once per update: an open
// release with an approving review may carry the command, a release
// merged within the last half hour may carry the outcome, a scheduled
// one is on a manifest. Releases that left the state are forgotten.
// Reading comments is the one call here; it fails soft.
func (w *Workflow) noteTracks(ctx context.Context, gh ghapi.Client, st *State, now time.Time) []string {
	var warns []string
	keep := map[model.Ident]bool{}
	for _, pr := range st.Releases {
		cand := w.candidate(pr, now)
		w.mu.Lock()
		t := w.trackFor(pr, cand, now)
		var stale, inRun bool
		if t != nil {
			stale = t.commentsAt.IsZero() || pr.UpdatedAt.After(t.commentsAt)
			inRun = t.run != nil && !t.runDone
		}
		w.mu.Unlock()
		if t == nil {
			continue
		}
		keep[pr.Ident()] = true
		// a run in progress owns the story; the comment lands at its end
		if stale && !inRun {
			cs, err := gh.PRComments(ctx, pr.Repo, pr.Number)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s comments: %s", where(pr), err))
				if ghapi.IsRateLimit(err) {
					return warns
				}
			} else {
				w.mu.Lock()
				t.comments, t.commentsAt = cs, pr.UpdatedAt
				w.mu.Unlock()
			}
		}
		w.mu.Lock()
		t.set(w.derive(pr, t, now)) // Phase "" when nothing is in flight: the record only remembers the comments read
		w.mu.Unlock()
	}
	w.mu.Lock()
	for id := range w.progress {
		if !keep[id] {
			delete(w.progress, id)
		}
	}
	w.mu.Unlock()
	return warns
}

// candidate reports a release worth reading comments for.
func (w *Workflow) candidate(pr *model.PR, now time.Time) bool {
	if w.win != nil {
		for _, l := range []string{w.win.ScheduledLabel, w.win.AppliedLabel, w.win.FailedLabel} {
			if l != "" && pr.Labels.Has(l) {
				return true
			}
		}
	}
	switch pr.State {
	case "OPEN", "":
		return hasApproval(pr)
	case "MERGED":
		return pr.MergedAt != nil && now.Sub(*pr.MergedAt) <= 30*time.Minute
	}
	return false
}

func hasApproval(pr *model.PR) bool {
	if pr.ReviewDecision == "APPROVED" {
		return true
	}
	for _, r := range pr.Opinions.Nodes {
		if r.State == "APPROVED" {
			return true
		}
	}
	return false
}

// ---- the poll (its own goroutine) ----

// PollEvery is the poll cadence from config.
func (w *Workflow) PollEvery() time.Duration {
	if w.cfg.ProgressPollSecs <= 0 {
		return 0
	}
	return time.Duration(w.cfg.ProgressPollSecs) * time.Second
}

// Live reports a release in flight worth polling: anything past
// scheduled that is not terminal, and a scheduled one from ten minutes
// before its slot.
func (w *Workflow) Live(app workflow.App) bool {
	if w.cfg.ProgressPollSecs <= 0 {
		return false
	}
	now := app.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.progress {
		if liveProgress(t.cur, now) {
			return true
		}
	}
	return false
}

func liveProgress(p Progress, now time.Time) bool {
	switch {
	case p.Phase == "" || p.Terminal:
		return false
	case p.Phase == PhaseScheduled:
		return !p.SlotAt.IsZero() && now.After(p.SlotAt.Add(-10*time.Minute))
	}
	return true
}

// Poll reads what moved: one listing of the manifest repo's apply runs
// when a live release has no run yet (or its run is done and nothing
// says why), the jobs of each run in progress, and the comments of each
// release with no run in progress. It reads a snapshot of the records
// and returns new values; nothing it holds is written here.
func (w *Workflow) Poll(ctx context.Context, gh ghapi.Client) (any, error) {
	now := clock.Now() // no app on this goroutine; app.Now reads the same clock
	type job struct {
		id model.Ident
		t  track // a copy
	}
	var jobs []job
	w.mu.Lock()
	for id, t := range w.progress {
		if liveProgress(t.cur, now) {
			jobs = append(jobs, job{id, *t})
		}
	}
	w.mu.Unlock()
	if len(jobs) == 0 {
		return nil, nil
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].id.String() < jobs[j].id.String() })
	// every live release gets a patch, if only to clear the last cycle's error
	patches := map[model.Ident]*pollPatch{}
	for _, j := range jobs {
		patches[j.id] = &pollPatch{}
	}
	patch := func(id model.Ident) *pollPatch { return patches[id] }
	// (a) the runs listing, once, when someone needs a run
	w.mu.Lock()
	target := w.manifestTarget
	w.mu.Unlock()
	var runs []model.WorkflowRun
	listed := false
	for _, j := range jobs {
		if j.t.infraOff || target.IsZero() || w.cfg.ApplyWorkflow == "" {
			continue
		}
		if j.t.run == nil || (j.t.runDone && !j.t.cur.Terminal) {
			if !listed {
				listed = true
				since := time.Time{}
				for _, k := range jobs {
					if s := k.t.cur.Started; !s.IsZero() && (since.IsZero() || s.Before(since)) {
						since = s
					}
				}
				if !since.IsZero() {
					since = since.Add(-5 * time.Minute)
				}
				var err error
				runs, err = gh.WorkflowFileRuns(ctx, target, w.cfg.ApplyWorkflow, "repository_dispatch", since)
				if err != nil {
					if ghapi.IsRateLimit(err) {
						return patches, err
					}
					for _, k := range jobs {
						p := patch(k.id)
						p.err = "runs: " + err.Error()
						if ghapi.IsPermission(err) {
							p.infraOff = true
						}
					}
					runs = nil
				}
			}
			if r := w.matchRun(runs, j.t); r != nil && (j.t.run == nil || r.ID != j.t.run.ID) {
				rr := *r
				patch(j.id).run = &rr
			}
		}
	}
	// (b) the jobs of each run in progress (the fresh one included)
	for _, j := range jobs {
		run := j.t.run
		if p := patches[j.id]; p.run != nil {
			run = p.run
		}
		if run == nil || j.t.infraOff || (j.t.runDone && patches[j.id].run == nil) {
			continue
		}
		js, err := gh.RunJobs(ctx, target, run.ID)
		if err != nil {
			if ghapi.IsRateLimit(err) {
				return patches, err
			}
			patch(j.id).err = "jobs: " + err.Error()
			continue
		}
		if jb := pickJob(js); jb != nil {
			patch(j.id).job = jb
		}
	}
	// (c) comments while no run is in progress: the outcome lands there
	for _, j := range jobs {
		run := j.t.run
		if p := patches[j.id]; p.run != nil {
			run = p.run
		}
		inProgress := run != nil && run.Status != "completed" && !j.t.runDone
		if inProgress {
			continue
		}
		cs, err := gh.PRComments(ctx, j.t.repo, j.t.number)
		if err != nil {
			if ghapi.IsRateLimit(err) {
				return patches, err
			}
			patch(j.id).err = "comments: " + err.Error()
			continue
		}
		patch(j.id).comments = cs
	}
	return patches, nil
}

// matchRun is the newest run whose title names the release, created at
// or after the release was sent on its way (less a few minutes' clock
// skew); nil when none.
func (w *Workflow) matchRun(runs []model.WorkflowRun, t track) *model.WorkflowRun {
	match := w.runMatch(t.repo, t.number)
	if match == "" {
		return nil
	}
	floor := t.cur.Started
	if floor.IsZero() {
		floor = t.firstSeen
	}
	floor = floor.Add(-5 * time.Minute)
	var best *model.WorkflowRun
	for i := range runs {
		r := &runs[i]
		if !titleNames(r.DisplayTitle, match) || r.CreatedAt.Before(floor) {
			continue
		}
		if best == nil || r.CreatedAt.After(best.CreatedAt) {
			best = r
		}
	}
	return best
}

// runMatch renders apply_run_match for the release.
func (w *Workflow) runMatch(repo model.Repo, number int) string {
	m := w.cfg.ApplyRunMatch
	if m == "" {
		return ""
	}
	return strings.NewReplacer("{repo}", repo.Name, "{number}", strconv.Itoa(number)).Replace(m)
}

// titleNames reports the match inside title not followed by another
// digit (rel:web#4 is not rel:web#42).
func titleNames(title, match string) bool {
	for start := 0; ; {
		i := strings.Index(title[start:], match)
		if i < 0 {
			return false
		}
		end := start + i + len(match)
		if end >= len(title) || title[end] < '0' || title[end] > '9' {
			return true
		}
		start = end
	}
}

// pickJob is the release job among a run's jobs: the one named "app
// release", else the first.
func pickJob(jobs []model.WorkflowJob) *model.WorkflowJob {
	if len(jobs) == 0 {
		return nil
	}
	for i := range jobs {
		if strings.HasPrefix(strings.ToLower(jobs[i].Name), "app release") {
			j := jobs[i]
			return &j
		}
	}
	j := jobs[0]
	return &j
}

// ApplyPoll (UI goroutine) folds a poll's patches into the records,
// re-derives each release and announces outcomes.
func (w *Workflow) ApplyPoll(app workflow.App, patch any) {
	patches, ok := patch.(map[model.Ident]*pollPatch)
	if !ok {
		return
	}
	st := w.stateOf(app)
	now := app.Now()
	var changed []*model.PR
	w.mu.Lock()
	for id, p := range patches {
		t, ok := w.progress[id]
		if !ok {
			continue
		}
		if p.run != nil {
			if t.run == nil || p.run.ID != t.run.ID {
				t.job, t.runDone = nil, false
			}
			t.run = p.run
		}
		if p.job != nil {
			if t.job != nil && p.job.RunAttempt > t.job.RunAttempt {
				t.runDone = false
			}
			t.job = p.job
			if p.job.Status == "completed" {
				t.runDone = true
				if t.run != nil && t.run.Status != "completed" {
					r := *t.run
					r.Status, r.Conclusion = "completed", p.job.Conclusion
					t.run = &r
				}
			}
		}
		if p.comments != nil {
			t.comments = p.comments
		}
		if p.infraOff {
			t.infraOff = true
		}
		t.pollErr = p.err
		var pr *model.PR
		if st != nil {
			pr = st.Find(id)
		}
		if pr == nil {
			continue // not applied yet: the next AfterApply derives it from the real PR
		}
		t.set(w.derive(pr, t, now))
		t.show(app.Fx(), pr)
		changed = append(changed, pr)
	}
	w.mu.Unlock()
	for _, pr := range changed {
		w.announce(app, pr)
	}
	if pr, ok := app.Detail().(*model.PR); ok && pr != nil {
		for _, c := range changed {
			if c.Ident() == pr.Ident() {
				app.BumpDetail()
				break
			}
		}
	}
	w.noteInfraOff(app)
}

// noteInfraOff says once per session that the manifest repo's runs are
// out of reach: the app-repo half still works.
func (w *Workflow) noteInfraOff(app workflow.App) {
	w.mu.Lock()
	off := false
	for _, t := range w.progress {
		if t.infraOff {
			off = true
		}
	}
	said := w.infraOffSaid
	if off {
		w.infraOffSaid = true
	}
	w.mu.Unlock()
	if off && !said {
		app.Notice(fmt.Sprintf("release progress: %s's Actions are not readable with this token — following the PR's labels and comments only", w.manifestRepo(app)))
	}
}

// ---- announcing ----

// announce says how a release ended, once per flight seen this session:
// a re-dispatch that ends the same way is announced again, and an
// outcome that predates the session is not news.
func (w *Workflow) announce(app workflow.App, pr *model.PR) {
	w.mu.Lock()
	t, ok := w.progress[pr.Ident()]
	if !ok || !t.cur.Terminal || !t.sawLive {
		w.mu.Unlock()
		return
	}
	t.sawLive = false
	p := t.cur
	w.mu.Unlock()
	wh := where(pr)
	switch p.Phase {
	case PhaseReleased:
		msg := fmt.Sprintf("%s released", wh)
		if p.MergeSHA != "" {
			msg += " — verified on " + model.ShortSHA(p.MergeSHA)
		}
		if !p.Started.IsZero() && !p.UpdatedAt.IsZero() && p.UpdatedAt.After(p.Started) {
			msg += " after " + deploys.FmtSecs(p.UpdatedAt.Sub(p.Started))
		}
		app.OK(msg)
		app.Launch()
	case PhaseFailed, PhaseUnverifiable:
		msg := fmt.Sprintf("%s release FAILED", wh)
		if p.Phase == PhaseUnverifiable {
			msg = fmt.Sprintf("%s release UNVERIFIABLE", wh)
		}
		switch {
		case strings.HasPrefix(p.Detail, "at "): // the step it died at reads on
			msg += " " + p.Detail
		case p.Detail != "":
			msg += " — " + p.Detail
		}
		if p.RunURL != "" {
			msg += "; run: " + p.RunURL
		}
		app.Fail(msg)
	case PhaseRefused:
		msg := fmt.Sprintf("%s release refused", wh)
		if p.Detail != "" {
			msg += " — " + p.Detail
		}
		app.Fail(msg)
	case PhaseLost:
		app.Notice(fmt.Sprintf("lost track of %s's release after %s — check the manifest repo's Actions", wh, deploys.FmtSecs(w.progressTimeout())))
	default: // rolled, not run, cancelled
		msg := fmt.Sprintf("%s release %s", wh, p.Phase)
		if p.Detail != "" {
			msg += " — " + p.Detail
		}
		app.Notice(msg)
	}
}

func (w *Workflow) progressTimeout() time.Duration {
	if w.cfg.ProgressTimeoutSecs <= 0 {
		return 90 * time.Minute
	}
	return time.Duration(w.cfg.ProgressTimeoutSecs) * time.Second
}

// ---- derivation ----

// Comment grammar: the outcome comment the release job leaves, and the
// refusal the dispatcher posts when the command is not accepted.
var (
	outcomeHeadRE = regexp.MustCompile(`(?m)^## (Released|Release NOT RUN|Release FAILED: merge refused|Release FAILED: the deploy did not succeed|Release UNVERIFIABLE|Release FAILED VERIFICATION, NOT reverted)(?: ?\(|\s*$)`)
	outcomeLineRE = regexp.MustCompile("(?m)^Outcome: `([a-z_]+)`(?: — (.*))?$")
	mergeLineRE   = regexp.MustCompile(`(?m)^Merge: ([0-9a-f]{7,40})`)
	runLineRE     = regexp.MustCompile(`(?m)^Run: (\S+)`)
	approverRE    = regexp.MustCompile(`(?m)^Approver: @?(\S+)`)
)

// outcome is what an outcome comment says.
type outcome struct {
	phase, id, detail, sha, url, approver string
	at                                    time.Time
}

// outcomeOf reads the outcome comment: the terminal phase, the outcome
// id, the detail, the merge sha, the run url, the approver.
func outcomeOf(c model.Comment) (outcome, bool) {
	m := outcomeHeadRE.FindStringSubmatch(c.Body)
	if m == nil {
		return outcome{}, false
	}
	o := outcome{at: c.CreatedAt}
	switch m[1] {
	case "Released":
		o.phase = PhaseReleased
	case "Release NOT RUN":
		o.phase = PhaseNotRun
	case "Release FAILED: merge refused":
		o.phase = PhaseRefused
	case "Release UNVERIFIABLE":
		o.phase = PhaseUnverifiable
	default:
		o.phase = PhaseFailed
	}
	if l := outcomeLineRE.FindStringSubmatch(c.Body); l != nil {
		o.id, o.detail = l[1], strings.TrimSpace(l[2])
	}
	if l := mergeLineRE.FindStringSubmatch(c.Body); l != nil {
		o.sha = l[1]
	}
	if l := runLineRE.FindStringSubmatch(c.Body); l != nil {
		o.url = l[1]
	}
	if l := approverRE.FindStringSubmatch(c.Body); l != nil {
		o.approver = l[1]
	}
	return o, true
}

// refusedREFor is the dispatcher's refusal of the command.
func refusedREFor(cmd string) *regexp.Regexp {
	if cmd == "" {
		cmd = "/release-now"
	}
	return regexp.MustCompile(`^\*\*` + regexp.QuoteMeta(cmd) + ` refused:\*\* (.*)`)
}

// derive turns everything known about a release into its progress.
// The newest signal wins: an outcome comment, unless a command or a run
// came after it (a re-dispatch); the run, by its steps; the labels; the
// manifest item; the command. A finished release's UpdatedAt is when it
// finished, so its clocks stop. w.mu is held by the caller.
func (w *Workflow) derive(pr *model.PR, t *track, now time.Time) Progress {
	p := w.deriveAt(pr, t, now)
	if p.Terminal && !p.Since.IsZero() {
		p.UpdatedAt = p.Since
	}
	return p
}

func (w *Workflow) deriveAt(pr *model.PR, t *track, now time.Time) Progress {
	p := Progress{UpdatedAt: now}
	var cmdAt, refusedAt time.Time
	var refusedWhy string
	var out *outcome
	refRE := w.refusedRE
	for _, c := range t.comments {
		if o, ok := outcomeOf(c); ok && (out == nil || o.at.After(out.at)) {
			oc := o
			out = &oc
		}
		if w.nowRE != nil && w.nowRE.MatchString(strings.TrimSpace(c.Body)) && c.CreatedAt.After(cmdAt) {
			cmdAt = c.CreatedAt
		}
		if m := refRE.FindStringSubmatch(c.Body); m != nil && c.CreatedAt.After(refusedAt) {
			refusedAt, refusedWhy = c.CreatedAt, strings.TrimSpace(m[1])
		}
	}
	if !t.nowPostedAt.IsZero() && t.nowPostedAt.After(cmdAt) {
		cmdAt = t.nowPostedAt
	}
	// a command posted after the outcome asks for another flight: the old
	// outcome is history
	if out != nil && cmdAt.After(out.at) {
		out = nil
	}
	// when the release was sent on its way
	if !cmdAt.IsZero() {
		p.Started = cmdAt
	}
	// a command posted after a finished run asks for another run: the old
	// one is history, and the listing will find the new one
	run := t.run
	if run != nil && run.Status == "completed" && cmdAt.After(run.CreatedAt) {
		run = nil
	}
	if run != nil && p.Started.IsZero() {
		p.Started = run.CreatedAt
	}
	if t.item != nil {
		p.Night = t.item.Night
		if st, err := time.Parse(time.RFC3339, t.item.StartUTC); err == nil {
			p.SlotAt = st
		}
	}
	if run != nil {
		p.RunID, p.RunAttempt, p.RunURL = run.ID, run.RunAttempt, run.HTMLURL
		if t.job != nil {
			p.RunAttempt = t.job.RunAttempt
			for _, s := range t.job.Steps {
				p.Steps = append(p.Steps, Step{Name: s.Name, Phase: StepPhase(s.Name), Status: s.Status, Conclusion: s.Conclusion, StartedAt: s.StartedAt, CompletedAt: s.CompletedAt})
			}
		}
	}
	p.MergeSHA = MergeSHA(pr)
	if dep, ok := pr.Get(ExtDeploy).(deploys.State); ok {
		p.DeployTotal = len(dep.Runs)
		for _, r := range dep.Runs {
			if r.Style == "chip.approved" {
				p.DeployDone++
			}
		}
	}
	if t.pollErr != "" {
		p.PollErr = t.pollErr + " — retrying"
		if t.infraOff {
			p.PollErr = t.pollErr + " — following the PR's labels and comments only"
		}
	}

	runNewer := run != nil && (out == nil || run.CreatedAt.After(out.at))
	switch {
	case out != nil && !runNewer:
		p.Phase, p.Terminal, p.Source = out.phase, true, "comment"
		p.Outcome, p.Detail, p.Since = out.id, out.detail, out.at
		if out.sha != "" {
			p.MergeSHA = out.sha
		}
		if out.url != "" && p.RunURL == "" {
			p.RunURL = out.url
		}
		p.Approver = out.approver
		if p.Started.IsZero() {
			p.Started = out.at
		}
		return p
	case run != nil:
		w.fromRun(&p, run, t.job)
		if p.Terminal {
			return p
		}
		return w.timeout(p, now)
	}
	// no run to speak of
	if !refusedAt.IsZero() && refusedAt.After(cmdAt) {
		p.Phase, p.Terminal, p.Source, p.Detail, p.Since = PhaseRefused, true, "comment", refusedWhy, refusedAt
		p.Outcome = "refused"
		return p
	}
	if w.win != nil {
		switch {
		case w.win.AppliedLabel != "" && pr.Labels.Has(w.win.AppliedLabel):
			p.Phase, p.Terminal, p.Source, p.Outcome = PhaseReleased, true, "label", "verified"
			return p
		case w.win.FailedLabel != "" && pr.Labels.Has(w.win.FailedLabel):
			p.Phase, p.Terminal, p.Source = PhaseFailed, true, "label"
			return p
		}
	}
	if t.item != nil {
		if ph, ok := w.fromItem(&p, t.item, now); ok {
			if !p.Terminal && ph != PhaseScheduled && ph != PhaseWaitingSlot {
				return w.timeout(p, now)
			}
			return p
		}
	}
	if w.win != nil && w.win.RolledLabel != "" && pr.Labels.Has(w.win.RolledLabel) && cmdAt.IsZero() {
		p.Phase, p.Terminal, p.Source = PhaseRolled, true, "label"
		return p
	}
	if w.win != nil && w.win.ScheduledLabel != "" && pr.Labels.Has(w.win.ScheduledLabel) {
		p.Phase, p.Source, p.Since = PhaseScheduled, "label", t.firstSeen
		if p.Started.IsZero() {
			p.Started = t.firstSeen
		}
		return p
	}
	if pr.State == "MERGED" && !cmdAt.IsZero() {
		// the run is out of sight (or merged by hand after a dispatch
		// failed): the app repo's own runs say what the deploy does
		if dep, ok := pr.Get(ExtDeploy).(deploys.State); ok {
			p.Source = "deploy"
			switch dep.Phase {
			case "waiting", "running", "stuck":
				p.Phase = PhaseDeploying
			case "failed":
				p.Phase, p.Terminal, p.Detail = PhaseFailed, true, "a deploy run failed"
			case "done":
				p.Phase, p.Detail = PhaseVerifying, "deploy runs done; waiting for the outcome"
			default:
				p.Phase = PhaseMerging
			}
			if pr.MergedAt != nil {
				p.Since = *pr.MergedAt
			}
			if p.Terminal {
				return p
			}
			return w.timeout(p, now)
		}
	}
	if !cmdAt.IsZero() {
		p.Phase, p.Source, p.Since = PhaseDispatched, "command", cmdAt
		if t.notTold && cmdAt.Equal(t.nowPostedAt) {
			p.Detail = "the manifest repo was not told — M merges it by hand"
		}
		return w.timeout(p, now)
	}
	return Progress{} // nothing in flight
}

// fromRun fills the phase from the run and its job.
func (w *Workflow) fromRun(p *Progress, run *model.WorkflowRun, job *model.WorkflowJob) {
	p.Source = "run"
	if run.Status == "completed" && (job == nil || job.Status == "completed") {
		p.Terminal = true
		p.Since = run.UpdatedAt
		if job != nil && !job.CompletedAt.IsZero() {
			p.Since = job.CompletedAt
		}
		switch run.Conclusion {
		case "success":
			p.Phase, p.Outcome = PhaseReleased, "verified"
		case "cancelled":
			p.Phase = PhaseCancelled
		default:
			p.Phase = PhaseFailed
			if s := failedStep(p.Steps); s != nil {
				p.Detail, p.FailPhase = "at "+s.Phase, s.Phase
			}
		}
		return
	}
	// in progress: the first running step says the phase
	var cur *Step
	for i := range p.Steps {
		if p.Steps[i].Status == "in_progress" {
			cur = &p.Steps[i]
			break
		}
	}
	if cur == nil {
		// between steps, or the job has not started: the last done one
		for i := len(p.Steps) - 1; i >= 0; i-- {
			if p.Steps[i].Status == "completed" && p.Steps[i].Conclusion != "skipped" {
				cur = &p.Steps[i]
				break
			}
		}
	}
	if cur == nil {
		p.Phase, p.Detail, p.Since = PhaseSetup, "run "+statusWords(run.Status, "queued"), run.CreatedAt
		return
	}
	p.Phase, p.Detail, p.Since = cur.Phase, cur.Name, cur.StartedAt
}

// statusWords is an API status in words (in_progress → in progress), or
// def when empty.
func statusWords(s, def string) string {
	if s == "" {
		return def
	}
	return strings.ReplaceAll(s, "_", " ")
}

func failedStep(steps []Step) *Step {
	for i := range steps {
		if steps[i].Conclusion == "failure" || steps[i].Conclusion == "timed_out" {
			return &steps[i]
		}
	}
	return nil
}

// fromItem fills the phase from the manifest item's status on a window
// night. ok is false when the status says nothing useful.
func (w *Workflow) fromItem(p *Progress, it *workflow.ManifestItem, now time.Time) (string, bool) {
	s := strings.TrimSpace(it.Status)
	ls := strings.ToLower(s)
	p.Source, p.Detail = "manifest", s
	switch {
	case strings.HasPrefix(ls, "verified"):
		p.Phase, p.Terminal, p.Outcome = PhaseReleased, true, "verified"
	case strings.HasPrefix(ls, "failed"):
		p.Phase, p.Terminal = PhaseFailed, true
	case strings.HasPrefix(ls, "refused"):
		p.Phase, p.Terminal = PhaseRefused, true
	case strings.HasPrefix(ls, "cancelled"):
		p.Phase, p.Terminal = PhaseCancelled, true
	case strings.HasPrefix(ls, "rolled"):
		p.Phase, p.Terminal = PhaseRolled, true
	case strings.HasPrefix(ls, "unfinished"):
		p.Phase, p.Terminal = PhaseLost, true
	case strings.HasPrefix(ls, "dispatched"):
		p.Phase = PhaseDispatched
		if p.Started.IsZero() || p.SlotAt.After(p.Started) {
			p.Started = p.SlotAt
		}
		p.Since = p.SlotAt
	case strings.HasPrefix(ls, "run cancelled"):
		p.Phase, p.Terminal = PhaseCancelled, true
	case strings.HasPrefix(ls, "run "):
		p.Phase, p.Terminal = PhaseFailed, true // "run <conclusion> — …"
	case strings.HasPrefix(ls, "waiting"):
		p.Phase, p.Since = PhaseWaitingSlot, p.SlotAt
	case s == "":
		if !p.SlotAt.IsZero() && now.After(p.SlotAt) {
			p.Phase, p.Detail, p.Since = PhaseWaitingSlot, "slot passed, no run yet", p.SlotAt
		} else {
			p.Phase, p.Detail = PhaseScheduled, ""
			if !p.SlotAt.IsZero() {
				p.Since = p.SlotAt
			}
		}
	default:
		return "", false
	}
	if p.Started.IsZero() {
		p.Started = p.SlotAt
	}
	return p.Phase, true
}

// timeout turns a release that has been in flight too long into lost,
// as of the moment the timeout ran out.
func (w *Workflow) timeout(p Progress, now time.Time) Progress {
	limit := w.progressTimeout()
	if !p.Started.IsZero() && now.Sub(p.Started) > limit {
		p.Phase, p.Terminal = PhaseLost, true
		p.Detail = "no outcome after " + deploys.FmtSecs(limit)
		p.Since = p.Started.Add(limit)
	}
	return p
}

// ---- the window's manifest item ----

// noteItems (UI goroutine, from AfterApply) copies the window's manifest
// item onto each tracked release and re-derives it.
func (w *Workflow) noteItems(app workflow.App, st *State) {
	prov, _ := app.State(WindowName).(workflow.ManifestItemProvider)
	now := app.Now()
	target := w.manifestRepo(app) // the poll has no app; it reads this
	w.mu.Lock()
	defer w.mu.Unlock()
	w.manifestTarget = target
	for id, t := range w.progress {
		pr := st.Find(id)
		if pr == nil {
			continue
		}
		if prov != nil {
			if it, ok := prov.ManifestItem(t.repo.Name, t.number); ok {
				cp := it
				t.item = &cp
			}
		}
		t.set(w.derive(pr, t, now))
		t.show(app.Fx(), pr)
	}
}

// ScheduledForWindow lists the releases on a manifest (the scheduled
// label), so the window workflow keeps reading manifests for them.
func (s *State) ScheduledForWindow() []*model.PR {
	if s == nil || s.scheduledLabel == "" {
		return nil
	}
	var out []*model.PR
	for _, p := range s.Releases {
		if p.Labels.Has(s.scheduledLabel) {
			out = append(out, p)
		}
	}
	return out
}

// ---- chip text ----

// progressChip is the kind chip of a release in flight.
func (w *Workflow) progressChip(p Progress, now time.Time) string {
	el := deploys.FmtSecs(p.Elapsed(now))
	switch p.Phase {
	case PhaseScheduled:
		if !p.SlotAt.IsZero() && w.win != nil {
			return "scheduled " + w.win.Local().In(p.SlotAt).Format("15:04")
		}
		return "scheduled"
	case PhaseWaitingSlot:
		return "waiting for slot"
	case PhaseDispatched:
		return "dispatched " + el
	case PhaseDeploying:
		if p.DeployTotal > 0 {
			return fmt.Sprintf("deploying %d/%d · %s", p.DeployDone, p.DeployTotal, el)
		}
		return "deploying " + el
	case PhaseReleased:
		return "released"
	case PhaseFailed:
		return "release FAILED"
	case PhaseRefused:
		return "release refused"
	case PhaseCancelled:
		return "release cancelled"
	case PhaseUnverifiable, PhaseNotRun, PhaseRolled, PhaseLost:
		return p.Phase
	}
	return p.Phase + " " + el
}

// progressStyle is the chip style of a phase.
func progressStyle(p Progress) string {
	switch p.Phase {
	case PhaseReleased:
		return "chip.approved"
	case PhaseFailed, PhaseRefused, PhaseUnverifiable, PhaseRolled:
		return "chip.rolled"
	case PhaseNotRun, PhaseCancelled, PhaseLost:
		return "chip.state"
	case PhaseScheduled, PhaseWaitingSlot:
		return "chip.sched"
	}
	return "chip.queued"
}

// rowLive reports a release row whose chip moves between refreshes.
func (w *Workflow) rowLive(app workflow.App, row workflow.Row) bool {
	pr, ok := row.Item.(*model.PR)
	if !ok {
		return false
	}
	p, ok := w.progressOf(pr)
	return ok && liveProgress(p, app.Now())
}

var _ workflow.Poller = (*Workflow)(nil)
