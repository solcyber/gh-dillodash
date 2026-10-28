package releasepr

import (
	"context"
	"fmt"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// EnrichDetail carries the release annotations from the listed row (or
// the state's row with the same identity) onto the freshly fetched
// detail PR, so the hooks that get only the detail still know it is a
// release.
func (w *Workflow) EnrichDetail(_ context.Context, _ ghapi.Client, pr, listed *model.PR) {
	if pr == nil {
		return
	}
	src := listed
	if InfoOf(src) == nil {
		src = w.Current().Find(pr.Ident())
	}
	if src == nil || InfoOf(src) == nil {
		return
	}
	for _, k := range []string{ExtRel, ExtDeploy, ExtCI, ExtIcon} {
		if v := src.Get(k); v != nil {
			pr.Set(k, v)
		}
	}
}

// Approve is the a key on a release PR: an approving review queues it
// for the maintenance window; the NOW path also posts the release-now
// command with a reason (an approver's emergency). A hotfix lists NOW
// first: it is an emergency by definition. Every question shows exactly
// what will be posted; the review goes first, and a failed comment after
// it says so. Returns false for anything that is not a release PR.
func (w *Workflow) Approve(app workflow.App, pr, listed *model.PR) bool {
	st := w.stateOf(app)
	info := w.infoFor(pr, listed, st)
	if info == nil {
		return false
	}
	wh := where(pr)
	state := pr.State
	if state == "" {
		state = "OPEN"
	}
	if state != "OPEN" {
		app.Notice(fmt.Sprintf("%s is %s — nothing to approve.", wh, strings.ToLower(state)))
		return true
	}
	if w.NotARelease(pr) {
		app.Notice(fmt.Sprintf("%s is %s — nothing to approve here", wh, w.NotAReleaseWhy(pr)))
		return true
	}
	if pr.IsDraft {
		app.Notice(fmt.Sprintf("%s is a draft — GitHub refuses reviews on drafts.", wh))
		return true
	}
	me := app.Me()
	if strings.EqualFold(pr.Author.Login, me) {
		app.Notice(fmt.Sprintf("you authored %s — a release needs another approver's review.", wh))
		return true
	}
	kind := info.Kind
	if kind == "" {
		kind = w.Kind(pr)
	}
	cmd := w.cfg.ReleaseNowCommand
	queues := w.cfg.ApprovalQueuesForWindow && w.win != nil
	night := w.NightPhrase(app.Now())
	nowLabel := fmt.Sprintf("release NOW (%s) — real emergencies only", cmd)
	queueLabel := fmt.Sprintf("queue it for %s window (approve review) — the normal path", night)
	if !queues {
		queueLabel = "approve review only — the release waits for its merge (M)"
	}
	var labels, paths []string
	var title string
	if kind == KindHotfix {
		labels, paths = []string{nowLabel, queueLabel}, []string{"now", "queue"}
		title = fmt.Sprintf("%s is a hotfix — an emergency by definition: approve and post %s with a reason", wh, cmd)
	} else {
		labels, paths = []string{queueLabel, nowLabel}, []string{"queue", "now"}
		if queues {
			title = fmt.Sprintf("%s is a release — an approving review QUEUES it for %s maintenance window", wh, night)
		} else {
			title = fmt.Sprintf("%s is a release — an approving review lets M merge it", wh)
		}
	}
	i := app.Pick(title, labels)
	if i < 0 || i >= len(paths) {
		app.Notice("approve cancelled")
		return true
	}
	path := paths[i]
	body := ""
	if path == "now" {
		minLen := w.cfg.ReleaseNowMinReason
		reason, ok := app.Prompt(fmt.Sprintf("Why can't this wait for the window? The reason is posted on the PR with the command (at least %d characters, no double quotes); Esc cancels:", minLen),
			workflow.PromptOpts{MinLen: minLen, Forbid: `"`, Note: "the reason is posted with " + cmd})
		if !ok {
			app.Notice("approve cancelled")
			return true
		}
		if len([]rune(reason)) < minLen || strings.Contains(reason, `"`) {
			app.Fail(fmt.Sprintf("approve cancelled — the reason needs at least %d characters and no double quotes", minLen))
			return true
		}
		body = fmt.Sprintf(`%s "%s"`, cmd, reason)
		if !w.nowRE.MatchString(body) {
			app.Fail(fmt.Sprintf("approve cancelled — %q does not match the %s grammar", body, cmd))
			return true
		}
		if !app.Confirm(fmt.Sprintf("%s\napprove %s and post that as %s?", body, wh, me)) {
			app.Notice("approve cancelled")
			return true
		}
	} else {
		q := fmt.Sprintf("approve %s as %s?", wh, me)
		if queues {
			q = fmt.Sprintf("approve %s as %s? (queues the release for %s window)", wh, me, night)
		}
		if !app.Confirm(q) {
			app.Notice("approve cancelled")
			return true
		}
	}
	if err := app.GH().Approve(app.Context(), pr.Repo, pr.Number, ""); err != nil {
		app.Fail(fmt.Sprintf("review approval of %s failed: %s", wh, err))
		return true
	}
	app.MarkApproved(pr)
	if body != "" {
		if err := app.GH().Comment(app.Context(), pr.Repo, pr.Number, body); err != nil {
			if queues {
				app.Fail(fmt.Sprintf("approved %s (it is queued for the window), but %s NOT posted: %s", wh, cmd, err))
			} else {
				app.Fail(fmt.Sprintf("approved %s, but %s NOT posted: %s", wh, cmd, err))
			}
			app.Refresh(true)
			return true
		}
		app.OK(fmt.Sprintf("approved %s and posted %s as %s", wh, cmd, me))
		app.Launch()
	} else {
		if queues {
			app.OK(fmt.Sprintf("approved %s as %s — queued for %s window", wh, me, night))
		} else {
			app.OK(fmt.Sprintf("approved %s as %s — M merges it", wh, me))
		}
		app.Celebrate(true)
	}
	app.Refresh(true)
	return true
}

// MergeBlock is why M must refuse a release PR, or "" (also "" for a
// PR that is not a release: the core's own rules apply then).
func (w *Workflow) MergeBlock(pr *model.PR, me string) string {
	if pr == nil || InfoOf(pr) == nil {
		return ""
	}
	state := pr.State
	if state == "" {
		state = "OPEN"
	}
	if state == "MERGED" {
		return "already merged (D approves its waiting deployments)"
	}
	if state != "OPEN" {
		return "the PR is " + strings.ToLower(state)
	}
	if w.NotARelease(pr) {
		return w.NotAReleaseWhy(pr)
	}
	if pr.IsDraft {
		return "a draft cannot be merged"
	}
	switch w.Label(pr, me, false) {
	case LabelNeeds, model.LabelChanges, model.LabelDraft:
		return "needs an approving review first (a)"
	}
	mss := pr.MergeStateStatus
	if mss == "" {
		mss = "UNKNOWN"
	}
	if !mergeable[mss] {
		return fmt.Sprintf("GitHub says the PR is %s; merge it on GitHub if that is wrong", mss)
	}
	return ""
}

// MergeConfirm asks before M merges a release: inside the release
// window once; outside it the merge is an emergency and asks for that
// explicitly (the release-now reason belongs on the PR first). Not
// handled for other PRs.
func (w *Workflow) MergeConfirm(app workflow.App, pr *model.PR) (ok bool, handled bool) {
	if pr == nil || w.infoFor(pr, app.DetailListed(), w.stateOf(app)) == nil {
		return false, false
	}
	wh := where(pr)
	head := pr.HeadRefName
	if head == "" {
		head = "?"
	}
	base := pr.BaseRefName
	if base == "" {
		base = w.cfg.ProductionBranch
	}
	how := "as a merge commit"
	switch w.cfg.MergeMethod {
	case "squash", "rebase":
		how = "with a " + w.cfg.MergeMethod
	}
	what := fmt.Sprintf("merge %s (%s -> %s at %s) %s? This DEPLOYS production.", wh, head, base, short(pr.HeadRefOid), how)
	if w.InReleaseWindow(app.Now()) {
		return app.Confirm(what), true
	}
	slot := "the window"
	if w.win != nil {
		slot = "the " + w.win.Local().T(w.win.Start) + " window"
	}
	i := app.Pick(fmt.Sprintf("%s: it is outside %s - merging now is an emergency release", wh, slot),
		[]string{"cancel - wait for the window",
			fmt.Sprintf("merge NOW anyway (the %s reason must already be on the PR)", w.cfg.ReleaseNowCommand)})
	if i != 1 {
		return false, true
	}
	return app.Confirm(what), true
}

// DeployTarget is the selected merged release row in the list view, for
// D; nil otherwise (the core then looks at the selection itself).
func (w *Workflow) DeployTarget(app workflow.App) *model.PR {
	if app.CurrentView() != "list" {
		return nil
	}
	row, ok := app.Selected()
	if !ok || row.Kind != RowRelease {
		return nil
	}
	pr, ok := row.Item.(*model.PR)
	if !ok || pr.State != "MERGED" || w.NotARelease(pr) {
		return nil
	}
	return pr
}

// DeployComment is the comment on a release's deployment approvals.
func (w *Workflow) DeployComment(pr *model.PR, sha string) string {
	if pr == nil || InfoOf(pr) == nil {
		return ""
	}
	return w.cfg.DeployComment(pr.Repo, pr.Number, sha)
}

// deployOf is a release's deploy state, from the listed row first (the
// core updates it after D) then the PR itself.
func deployOf(pr, listed *model.PR) (deploys.State, bool) {
	if listed != nil {
		if d, ok := listed.Get(ExtDeploy).(deploys.State); ok {
			return d, true
		}
	}
	if pr != nil {
		if d, ok := pr.Get(ExtDeploy).(deploys.State); ok {
			return d, true
		}
	}
	return deploys.State{}, false
}
