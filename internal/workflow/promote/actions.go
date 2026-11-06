package promote

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// maxPickBytes bounds the base64 a promotion commit carries.
const maxPickBytes = 20 << 20

func selected(app workflow.App) (*Change, bool) {
	row, ok := app.Selected()
	if !ok || row.Kind != RowChange {
		return nil, false
	}
	c, ok := row.Item.(*Change)
	return c, ok && c != nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// doPromote is p: move the selected change one tier on.
func (w *Workflow) doPromote(app workflow.App) {
	c, ok := selected(app)
	if !ok {
		app.Notice("p promotes a change: select a row in the promote section")
		return
	}
	what := fmt.Sprintf("promoting %s #%d", c.Repo.Name, c.Number())
	if workflow.WriteRefused(app, c.Repo, what) || !workflow.StaleGate(app, "promote") {
		return
	}
	switch c.Where {
	case OnFirst:
		if workflow.PostGuard(app, "the promotion PR") {
			w.openPromotion(app, c)
		}
	case Promoting:
		if workflow.PostGuard(app, "the promotion merge") {
			w.mergePromotion(app, c)
		}
	case OnStage:
		if workflow.PostGuard(app, "the "+w.cfg.Stage()+" tag") {
			w.tagUnit(app, c)
		}
	default:
		app.Notice(fmt.Sprintf("#%d is on %s already", c.Number(), w.cfg.Prod()))
	}
}

// ---- first tier → staging: the promotion PR ----

// pick is a promotion commit's file changes, or why it can't be made.
type pick struct {
	adds      []model.FileAdd
	dels      []string
	conflicts []string
	bytes     int
}

// planPick applies a first-tier commit's change to the staging head file
// by file: a file is taken when staging still has what the first tier had
// before the commit (or already has the result), and is a conflict
// otherwise. Nothing is merged three-way: a file the commit touches that
// staging changed on its own is left for a person.
func planPick(ctx context.Context, gh ghapi.Client, repo model.Repo, ch model.CommitChange, parent, stageHead string) (pick, error) {
	var p pick
	blob := func(path, ref string) (string, bool, error) { return gh.BlobAt(ctx, repo, path, ref) }
	take := func(path, sha string) error {
		b64, err := gh.BlobBase64(ctx, repo, sha)
		if err != nil {
			return err
		}
		p.bytes += len(b64)
		p.adds = append(p.adds, model.FileAdd{Path: path, Base64: b64})
		return nil
	}
	for _, f := range ch.Files {
		switch f.Status {
		case "removed":
			was, _, err := blob(f.Filename, parent)
			if err != nil {
				return p, err
			}
			now, on, err := blob(f.Filename, stageHead)
			if err != nil {
				return p, err
			}
			switch {
			case !on: // gone already
			case now == was:
				p.dels = append(p.dels, f.Filename)
			default:
				p.conflicts = append(p.conflicts, f.Filename+" (changed on the staging tier; the change deletes it)")
			}
		case "added", "copied":
			now, on, err := blob(f.Filename, stageHead)
			if err != nil {
				return p, err
			}
			switch {
			case !on:
				if err := take(f.Filename, f.SHA); err != nil {
					return p, err
				}
			case now == f.SHA: // there already
			default:
				p.conflicts = append(p.conflicts, f.Filename+" (exists on the staging tier with other content)")
			}
		case "modified":
			was, _, err := blob(f.Filename, parent)
			if err != nil {
				return p, err
			}
			now, on, err := blob(f.Filename, stageHead)
			if err != nil {
				return p, err
			}
			switch {
			case on && now == f.SHA: // there already
			case on && now == was:
				if err := take(f.Filename, f.SHA); err != nil {
					return p, err
				}
			case !on:
				p.conflicts = append(p.conflicts, f.Filename+" (missing on the staging tier)")
			default:
				p.conflicts = append(p.conflicts, f.Filename+" (differs on the staging tier from the first tier before the change)")
			}
		case "renamed":
			old := f.PreviousFilename
			was, _, err := blob(old, parent)
			if err != nil {
				return p, err
			}
			oldNow, oldOn, err := blob(old, stageHead)
			if err != nil {
				return p, err
			}
			newNow, newOn, err := blob(f.Filename, stageHead)
			if err != nil {
				return p, err
			}
			switch {
			case !oldOn && newOn && newNow == f.SHA: // there already
			case oldOn && oldNow == was && (!newOn || newNow == f.SHA):
				p.dels = append(p.dels, old)
				if !newOn {
					if err := take(f.Filename, f.SHA); err != nil {
						return p, err
					}
				}
			default:
				p.conflicts = append(p.conflicts, old+" → "+f.Filename+" (the staging tier's copy differs)")
			}
		case "unchanged":
		default: // "changed": a mode change, which a commit made through the API can't carry
			p.conflicts = append(p.conflicts, f.Filename+" ("+f.Status+": a file mode change)")
		}
	}
	return p, nil
}

// openPromotion opens a PR carrying a first-tier PR's change into
// staging: a branch from the staging head, one GitHub-signed commit with
// the change applied file by file, and the PR.
func (w *Workflow) openPromotion(app workflow.App, c *Change) {
	gh, ctx := app.GH(), app.Context()
	f := c.First
	n := f.Number
	sha := mergeOid(f)
	if sha == "" {
		app.Fail(fmt.Sprintf("#%d has no merge commit on %s to promote", n, w.cfg.First()))
		return
	}
	app.Flash(fmt.Sprintf("reading #%d's change...", n))
	ch, err := gh.CommitChange(ctx, c.Repo, sha)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read #%d's merge commit %s: %s", n, short(sha), err))
		return
	}
	if ch.Truncated {
		app.Notice(fmt.Sprintf("#%d changes more than %d files — promote it by hand", n, len(ch.Files)))
		return
	}
	if len(ch.Parents) == 0 {
		app.Fail(fmt.Sprintf("#%d's merge commit %s has no parent", n, short(sha)))
		return
	}
	head, err := gh.BranchHead(ctx, c.Repo, w.cfg.Stage())
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read %s's head: %s", w.cfg.Stage(), err))
		return
	}
	app.Flash(fmt.Sprintf("checking #%d's %d file(s) against %s...", n, len(ch.Files), w.cfg.Stage()))
	p, err := planPick(ctx, gh, c.Repo, ch, ch.Parents[0], head)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't compare #%d's files with %s: %s", n, w.cfg.Stage(), err))
		return
	}
	if len(p.conflicts) > 0 {
		app.Notice(fmt.Sprintf("#%d doesn't apply cleanly to %s: %s — promote what it depends on first, or by hand",
			n, w.cfg.Stage(), strings.Join(firstN(p.conflicts, 3), "; ")))
		return
	}
	if len(p.adds)+len(p.dels) == 0 {
		app.Notice(fmt.Sprintf("#%d's change is on %s already (every file matches) — nothing to promote", n, w.cfg.Stage()))
		return
	}
	if p.bytes > maxPickBytes {
		app.Notice(fmt.Sprintf("#%d's files come to %d MB — promote it by hand", n, p.bytes>>20))
		return
	}
	branch := fmt.Sprintf("%s%d-%s", w.cfg.BranchPrefix, n, w.cfg.Stage())
	title := fmt.Sprintf("Promote #%d to %s: %s", n, w.cfg.Stage(), f.Title)
	files := len(p.adds) + len(p.dels)
	if !app.Confirm(fmt.Sprintf("open a PR promoting #%d to %s as %s?\n%d file(s) of %s applied to %s at %s, on branch %s",
		n, w.cfg.Stage(), app.Me(), files, short(sha), w.cfg.Stage(), short(head), branch)) {
		app.Notice("promotion cancelled")
		return
	}
	body := fmt.Sprintf("Promotes #%d from `%s` to `%s`.\n\nThe change of %s (#%d's merge commit on `%s`), applied to `%s` at %s file by file: every file it touches still matched `%s` before #%d.\n\nOpened with dillodash by %s.",
		n, w.cfg.First(), w.cfg.Stage(), sha, n, w.cfg.First(), w.cfg.Stage(), head, w.cfg.First(), n, app.Me())
	if err := gh.CreateBranch(ctx, c.Repo, branch, head); err != nil {
		if e, ok := ghapi.AsError(err); ok && e.Status == 422 {
			app.Notice(fmt.Sprintf("branch %s exists — a promotion of #%d was started; open it on GitHub, or delete the branch and press p again", branch, n))
			return
		}
		app.Fail(fmt.Sprintf("creating %s failed: %s", branch, err))
		return
	}
	if _, err := gh.CommitOnBranch(ctx, c.Repo, branch, head, title, body, p.adds, p.dels); err != nil {
		cleanup := ""
		if derr := gh.DeleteRef(ctx, c.Repo, "heads/"+branch); derr != nil {
			cleanup = fmt.Sprintf(" (and deleting %s failed: %s)", branch, derr)
		}
		app.Fail(fmt.Sprintf("committing #%d's change to %s failed: %s%s", n, branch, err, cleanup))
		return
	}
	pr, err := gh.CreatePR(ctx, c.Repo, w.cfg.Stage(), branch, title, body)
	if err != nil {
		app.Fail(fmt.Sprintf("the change is on %s, but opening its PR failed: %s", branch, err))
		return
	}
	app.OK(fmt.Sprintf("opened #%d promoting #%d to %s — p merges it once its checks pass", pr, n, w.cfg.Stage()))
	app.Celebrate(false)
	app.Refresh(true)
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string(nil), s[:n]...), fmt.Sprintf("and %d more", len(s)-n))
}

// ---- the promotion PR: merge it ----

func (w *Workflow) mergePromotion(app workflow.App, c *Change) {
	gh, ctx := app.GH(), app.Context()
	n := c.Promo.Number
	app.Flash(fmt.Sprintf("reading #%d...", n))
	pr, err := gh.PRDetail(ctx, c.Repo, n)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read #%d: %s", n, err))
		return
	}
	if pr.State != "" && pr.State != "OPEN" {
		app.Notice(fmt.Sprintf("#%d is %s now", n, strings.ToLower(pr.State)))
		app.Refresh(true)
		return
	}
	extra := ""
	switch st := strings.ToUpper(pr.MergeStateStatus); st {
	case "CLEAN", "HAS_HOOKS":
	case "UNSTABLE":
		extra = "\nsome checks that aren't required fail"
	case "BLOCKED":
		app.Notice(fmt.Sprintf("#%d can't merge yet: a required check or review is missing", n))
		return
	case "BEHIND":
		app.Notice(fmt.Sprintf("#%d is behind %s: update its branch first", n, w.cfg.Stage()))
		return
	case "DIRTY":
		app.Notice(fmt.Sprintf("#%d conflicts with %s: resolve it on GitHub", n, w.cfg.Stage()))
		return
	case "DRAFT":
		app.Notice(fmt.Sprintf("#%d is a draft", n))
		return
	default:
		app.Notice(fmt.Sprintf("GitHub hasn't worked out whether #%d can merge (%s) — try again in a moment", n, text.OrQ(st)))
		return
	}
	method := app.Config().Merge.Method(c.Repo, "")
	how := method
	if how == "" {
		how = "merge commit"
	}
	if !app.Confirm(fmt.Sprintf("merge #%d into %s (%s) as %s? Its push deploys %s.%s", n, w.cfg.Stage(), how, app.Me(), w.cfg.Stage(), extra)) {
		app.Notice("merge cancelled")
		return
	}
	res, err := gh.MergePR(ctx, c.Repo, n, pr.HeadRefOid, "", method)
	if err != nil {
		app.Fail(fmt.Sprintf("merging #%d failed: %s", n, err))
		return
	}
	if !res.Merged {
		app.Fail(fmt.Sprintf("GitHub did not merge #%d: %s", n, text.OrQ(res.Message)))
		return
	}
	app.OK(fmt.Sprintf("merged #%d into %s — once its deploy is green, p tags it go", n, w.cfg.Stage()))
	app.Celebrate(false)
	app.Refresh(true)
}

// ---- on staging: tag the unit go or hold ----

func (w *Workflow) tagUnit(app workflow.App, c *Change) {
	gh, ctx := app.GH(), app.Context()
	n := c.Promo.Number
	sha := mergeOid(c.Promo)
	if sha == "" {
		app.Fail(fmt.Sprintf("#%d has no merge commit on %s to tag", n, w.cfg.Stage()))
		return
	}
	d, run := deployOf(c.Runs)
	deploy := map[Deploy]string{DeployOK: "its deploy passed", DeployRunning: "its deploy is running",
		DeployWaiting: "its deploy waits on an environment", DeployFailed: "its deploy failed (" + run + ")", DeployNone: "no deploy run on it"}[d]
	type opt struct{ kind, prefix, other, label string }
	var opts []opt
	if c.Tag != "go" {
		opts = append(opts, opt{"go", w.cfg.GoTagPrefix, w.cfg.HoldTagPrefix, fmt.Sprintf("%s%d — release it with the next cut", w.cfg.GoTagPrefix, n)})
	}
	if c.Tag != "hold" {
		opts = append(opts, opt{"hold", w.cfg.HoldTagPrefix, w.cfg.GoTagPrefix, fmt.Sprintf("%s%d — keep it on %s", w.cfg.HoldTagPrefix, n, w.cfg.Stage())})
	}
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.label
	}
	tag := c.Tag
	if tag == "" {
		tag = "untagged"
	}
	head := fmt.Sprintf("#%d on %s: %s · %s", n, w.cfg.Stage(), tag, deploy)
	if c.Tag == "go" {
		head += " · " + w.releaseNote(app, c)
	}
	i := app.Pick(head, labels)
	if i < 0 || i >= len(opts) {
		app.Notice("cancelled")
		return
	}
	o := opts[i]
	if o.kind == "go" && d != DeployOK {
		if !app.Confirm(fmt.Sprintf("%s's deploy of #%d: %s. Tag it go anyway?", w.cfg.Stage(), n, deploy)) {
			app.Notice("cancelled")
			return
		}
	}
	msg, ok := app.Prompt("Tag message (Esc cancels):", workflow.PromptOpts{Initial: fmt.Sprintf("%s: %s", o.kind, c.Promo.Title)})
	if !ok {
		app.Notice("cancelled")
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = o.kind
	}
	msg += "\n\nTagged with dillodash by " + app.Me() + "."
	name := fmt.Sprintf("%s%d", o.prefix, n)
	flip := ""
	if c.Tag != "" {
		flip = fmt.Sprintf(" (deletes %s%d first)", o.other, n)
	}
	if !app.Confirm(fmt.Sprintf("tag %s on %s as %s?%s", name, short(sha), app.Me(), flip)) {
		app.Notice("cancelled")
		return
	}
	if c.Tag != "" {
		if err := gh.DeleteRef(ctx, c.Repo, fmt.Sprintf("tags/%s%d", o.other, n)); err != nil {
			var e *ghapi.Error
			if !errors.As(err, &e) || e.Status != 422 && e.Status != 404 {
				app.Fail(fmt.Sprintf("deleting %s%d failed: %s", o.other, n, err))
				return
			}
		}
	}
	if err := gh.CreateTag(ctx, c.Repo, name, msg, sha); err != nil {
		app.Fail(fmt.Sprintf("tagging %s failed: %s", name, err))
		return
	}
	done := fmt.Sprintf("tagged %s", name)
	if o.kind == "go" {
		done += " — " + w.releaseNote(app, c)
	}
	app.OK(done)
	app.Celebrate(o.kind == "go")
	app.Refresh(true)
}

// releaseNote is the last hop for a go unit: the open release PR that
// carries it, the unit ahead of it holding the cut, or the next cut.
func (w *Workflow) releaseNote(app workflow.App, c *Change) string {
	if c.BlockedBy > 0 {
		return fmt.Sprintf("it releases once #%d ahead of it is go", c.BlockedBy)
	}
	if p, ok := app.State(releaseName).(workflow.CIProvider); ok && p != nil {
		var open []*model.PR
		for _, r := range p.CIPRs() {
			if r != nil && r.Repo == c.Repo && r.BaseRefName == w.cfg.Prod() && (r.State == "" || r.State == "OPEN") {
				open = append(open, r)
			}
		}
		sort.Slice(open, func(i, j int) bool { return open[i].Number < open[j].Number })
		if len(open) > 0 {
			return fmt.Sprintf("release PR #%d into %s is open (a approves it there)", open[0].Number, w.cfg.Prod())
		}
	}
	return "it rides the next release cut into " + w.cfg.Prod()
}

// ---- D: the tier deploy ----

// DeployRow is D on a change row: approve the environments the change's
// tier deploy waits on that the viewer reviews.
func (w *Workflow) DeployRow(app workflow.App, row workflow.Row) bool {
	if row.Kind != RowChange {
		return false
	}
	c, ok := row.Item.(*Change)
	if !ok {
		return false
	}
	tier := w.tierName(c)
	sha := c.SHA()
	if sha == "" {
		app.Notice(fmt.Sprintf("#%d is being promoted: its PR's checks are on the PR, and %s deploys once it merges", c.Number(), tier))
		return true
	}
	what := fmt.Sprintf("%s #%d's %s deploy", c.Repo.Name, c.Number(), tier)
	if workflow.WriteRefused(app, c.Repo, "approving "+what) {
		return true
	}
	gh, ctx := app.GH(), app.Context()
	app.Flash(fmt.Sprintf("reading %s...", what))
	runs, err := gh.WaitingRuns(ctx, c.Repo, sha)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read %s's runs: %s", what, err))
		return true
	}
	if len(runs) == 0 {
		app.Notice(fmt.Sprintf("nothing in %s waits on an environment now", what))
		app.Refresh(true)
		return true
	}
	run := runs[0]
	if len(runs) > 1 {
		labels := make([]string, len(runs))
		for i, r := range runs {
			labels[i] = fmt.Sprintf("%s run %d", text.Ellip(r.Name, 40), r.ID)
		}
		i := app.Pick("which run?", labels)
		if i < 0 {
			app.Notice("cancelled")
			return true
		}
		run = runs[i]
	}
	pend, err := gh.PendingDeployments(ctx, c.Repo, run.ID)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't read run %d's environments: %s", run.ID, err))
		return true
	}
	var ids []int64
	var mine, all []string
	for _, p := range pend {
		all = append(all, text.OrQ(p.Environment.Name))
		if p.CurrentUserCanApprove {
			ids = append(ids, p.Environment.ID)
			mine = append(mine, text.OrQ(p.Environment.Name))
		}
	}
	if len(ids) == 0 {
		app.Notice(fmt.Sprintf("%s run %d waits on %s, but %s can't approve it", run.Name, run.ID, strings.Join(all, ", "), app.Me()))
		return true
	}
	if !app.Confirm(fmt.Sprintf("approve %s (%s run %d) of %s as %s?", strings.Join(mine, ", "), run.Name, run.ID, what, app.Me())) {
		app.Notice("deployment approval cancelled")
		return true
	}
	if err := gh.ApproveDeployments(ctx, c.Repo, run.ID, ids, w.cfg.Comment(c.Repo, run.Name, run.ID)); err != nil {
		app.Fail(fmt.Sprintf("approving %s failed: %s", what, err))
		return true
	}
	app.OK(fmt.Sprintf("approved %s (%s)", what, strings.Join(mine, ", ")))
	app.Celebrate(false)
	app.Refresh(true)
	return true
}
