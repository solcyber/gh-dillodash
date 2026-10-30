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
// first: it is an emergency by definition. A release you already
// approved for this head is queued already, so a offers only NOW, and
// posts the command without a second review (a release bumped from the
// window, say). Every question shows exactly what will be posted; the
// review goes first, and a failed comment after it says so. Returns
// false for anything that is not a release PR.
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
	queues := w.cfg.ApprovalQueuesForWindow && w.win != nil && kind != KindHotfix // a hotfix never queues
	night := w.NightPhrase(app.Now())
	nowLabel := fmt.Sprintf("release NOW (%s) — real emergencies only", cmd)
	if workflow.IApproved(app, pr) {
		return w.approvedNow(app, pr, wh, me, nowLabel, queues)
	}
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
		var ok bool
		if body, ok = w.nowBody(app, fmt.Sprintf("approve %s and post that as %s?", wh, me)); !ok {
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
		lead := "approved " + wh
		if queues {
			lead += " (it is queued for the window)"
		}
		w.postNow(app, pr, body, lead+", but", "approved "+wh+" and", "approved "+wh+",")
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

// approvedNow is a on a release you approved for this head: the review
// stands (and queues it, where approvals queue), so the only thing left
// to offer is NOW, which posts the command and no second review. A
// release already on its way has nothing left to post.
func (w *Workflow) approvedNow(app workflow.App, pr *model.PR, wh, me, nowLabel string, queues bool) bool {
	if w.inFlight(pr) {
		app.Notice(fmt.Sprintf("%s is approved by you and already on its way — nothing to post.", wh))
		return true
	}
	stands := "your approval of this head stands"
	if queues {
		stands += " and queues it"
	}
	title := fmt.Sprintf("%s is already approved by you — %s; NOW posts %s without a second review", wh, stands, w.cfg.ReleaseNowCommand)
	if app.Pick(title, []string{nowLabel}) != 0 {
		app.Notice("approve cancelled")
		return true
	}
	body, ok := w.nowBody(app, fmt.Sprintf("post that on %s as %s? (no second review: yours stands)", wh, me))
	if !ok {
		return true
	}
	w.postNow(app, pr, body, wh+":", wh+":", wh+":")
	app.Refresh(true)
	return true
}

// nowBody asks for the release-now reason and confirms the exact
// comment with question; ok false (and the status line says why) on a
// cancel or a body the grammar refuses.
func (w *Workflow) nowBody(app workflow.App, question string) (string, bool) {
	cmd, minLen := w.cfg.ReleaseNowCommand, w.cfg.ReleaseNowMinReason
	reason, ok := app.Prompt(fmt.Sprintf("Why can't this wait for the window? The reason is posted on the PR with the command (at least %d characters, no double quotes); Esc cancels:", minLen),
		workflow.PromptOpts{MinLen: minLen, Forbid: `"`, Note: "the reason is posted with " + cmd})
	if !ok {
		app.Notice("approve cancelled")
		return "", false
	}
	if len([]rune(reason)) < minLen || strings.Contains(reason, `"`) {
		app.Fail(fmt.Sprintf("approve cancelled — the reason needs at least %d characters and no double quotes", minLen))
		return "", false
	}
	body := fmt.Sprintf(`%s "%s"`, cmd, reason)
	if !w.nowRE.MatchString(body) {
		app.Fail(fmt.Sprintf("approve cancelled — %q does not match the %s grammar", body, cmd))
		return "", false
	}
	if !app.Confirm(body + "\n" + question) {
		app.Notice("approve cancelled")
		return "", false
	}
	return body, true
}

// postNow posts the release-now body on pr and tells the manifest repo.
// The leads open each status line: failLead before "<cmd> NOT posted",
// toldLead before "posted <cmd>, but … was not told", okLead before
// "posted <cmd> as …", so a status line says whether this keystroke
// also approved the release.
func (w *Workflow) postNow(app workflow.App, pr *model.PR, body, failLead, toldLead, okLead string) {
	cmd, me := w.cfg.ReleaseNowCommand, app.Me()
	id, err := app.GH().PostComment(app.Context(), pr.Repo, pr.Number, body)
	if err != nil {
		app.Fail(fmt.Sprintf("%s %s NOT posted: %s", failLead, cmd, err))
		return
	}
	// The comment lives in the release repo, whose events never reach
	// the manifest repo's workflows, so tell them: a repository_dispatch
	// naming the comment. The receiving side re-derives everything from
	// the comment by id; the payload is only a pointer to it.
	if target := w.manifestRepo(app); w.cfg.ReleaseNowDispatch && !target.IsZero() {
		payload := map[string]any{"repo": pr.Repo.Name, "pr": pr.Number, "comment_id": id}
		if err := app.GH().Dispatch(app.Context(), target, w.cfg.ReleaseNowDispatchEvent, payload); err != nil {
			w.notePosted(pr, app.Now(), false)
			app.Fail(fmt.Sprintf("%s posted %s, but %s was not told (%s): press M to merge by hand",
				toldLead, cmd, target, err))
			return
		}
		w.notePosted(pr, app.Now(), true) // the release is in flight from here
		app.OK(fmt.Sprintf("%s posted %s as %s and told %s", okLead, cmd, me, target))
	} else {
		w.notePosted(pr, app.Now(), true)
		app.OK(fmt.Sprintf("%s posted %s as %s", okLead, cmd, me))
	}
	app.Launch()
}

// manifestRepo is where the release-now dispatch goes: the window
// workflow's manifest repo, else the first review repo (the loader's own
// default), and nothing when there is no window workflow to tell.
func (w *Workflow) manifestRepo(app workflow.App) model.Repo {
	if w.win == nil {
		return model.Repo{}
	}
	if !w.win.ManifestRepo.IsZero() {
		return w.win.ManifestRepo
	}
	if repos := app.Repos(); len(repos) > 0 {
		return repos[0]
	}
	return model.Repo{}
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
	what := fmt.Sprintf("merge %s (%s -> %s at %s) %s? This DEPLOYS production.", wh, head, base, model.ShortSHA(pr.HeadRefOid), how)
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
