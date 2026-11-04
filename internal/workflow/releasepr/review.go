package releasepr

import (
	"fmt"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The review and command paths that used to need the web page: take a
// release out before the freeze (request changes), withdraw your own
// approval so a later a queues it again (dismiss), and tell the
// manifest repo about a release-now command it never heard of (one
// posted from the PR page, or whose dispatch failed).

// changesMinReason is the shortest request-changes body: GitHub refuses
// an empty one, and the author needs to know why.
const changesMinReason = 10

// refireAfter is how long a posted command may wait for its run before
// a offers to fire the dispatch for it again: the run usually shows
// within a minute of a dispatch, and a command posted from the PR page
// fires none at all.
const refireAfter = 2 * time.Minute

// requestChanges posts a REQUEST_CHANGES review with a reason: the
// conductor's freeze skips a release whose review decision is not
// approved, so this takes it out of the window.
func (w *Workflow) requestChanges(app workflow.App, pr *model.PR, wh string) {
	reason, ok := app.Prompt(fmt.Sprintf("Why should %s not release? The reason is posted as the review (at least %d characters); Esc cancels:", wh, changesMinReason),
		workflow.PromptOpts{MinLen: changesMinReason, Note: "posted as a request-changes review"})
	if !ok || len([]rune(strings.TrimSpace(reason))) < changesMinReason {
		app.Notice("request changes cancelled")
		return
	}
	if !app.Confirm(fmt.Sprintf("request changes on %s as %s? It leaves the window until someone approves again.\n%s", wh, app.Me(), reason)) {
		app.Notice("request changes cancelled")
		return
	}
	if err := app.GH().RequestChanges(app.Context(), pr.Repo, pr.Number, strings.TrimSpace(reason)); err != nil {
		app.Fail(fmt.Sprintf("request changes on %s failed: %s", wh, err))
		return
	}
	app.UnmarkApproved(pr)
	app.OK(fmt.Sprintf("requested changes on %s as %s — it is out of the window until approved again", wh, app.Me()))
	app.Refresh(true)
}

// myApproval is the database id of the viewer's standing approval of
// pr: from the PR as listed or opened, else read fresh.
func (w *Workflow) myApproval(app workflow.App, pr *model.PR) int64 {
	me := app.Me()
	find := func(p *model.PR) int64 {
		if p == nil {
			return 0
		}
		for _, r := range p.Opinions.Nodes {
			if strings.EqualFold(r.Author.Login, me) && r.State == "APPROVED" && r.ID != 0 {
				return r.ID
			}
		}
		var id int64
		for _, r := range p.Reviews.Nodes { // oldest first: the last one wins
			if strings.EqualFold(r.Author.Login, me) && r.ID != 0 {
				if r.State == "APPROVED" {
					id = r.ID
				} else if r.State == "CHANGES_REQUESTED" || r.State == "DISMISSED" {
					id = 0
				}
			}
		}
		return id
	}
	if id := find(pr); id != 0 {
		return id
	}
	fresh, err := app.GH().PRDetail(app.Context(), pr.Repo, pr.Number)
	if err != nil {
		return 0
	}
	return find(fresh)
}

// dismissMine dismisses the viewer's approval of pr. Withdrawn, the
// release leaves the queue; a approves it again (and so queues it again)
// whenever it should go.
func (w *Workflow) dismissMine(app workflow.App, pr *model.PR, wh string) {
	app.Flash(fmt.Sprintf("reading your review of %s...", wh))
	id := w.myApproval(app, pr)
	if id == 0 {
		app.Notice(fmt.Sprintf("couldn't find your approving review of %s (approved in this session and not refreshed yet? press r and retry)", wh))
		return
	}
	msg := fmt.Sprintf("withdrawn by %s from dillodash", app.Me())
	if !app.Confirm(fmt.Sprintf("dismiss your approval of %s? It leaves the queue; a approves it again to re-queue it.\nmessage: %s", wh, msg)) {
		app.Notice("dismiss cancelled")
		return
	}
	if err := app.GH().DismissReview(app.Context(), pr.Repo, pr.Number, id, msg); err != nil {
		app.Fail(fmt.Sprintf("dismissing your review of %s failed: %s", wh, err))
		return
	}
	app.UnmarkApproved(pr)
	app.OK(fmt.Sprintf("dismissed your approval of %s — a approves it again to re-queue it", wh))
	app.Refresh(true)
}

// unpicked is the release-now command on pr that no run has picked up:
// this session's dispatch failed, or the command has waited refireAfter
// with no run (posted from the PR page, say, which fires no dispatch).
// It returns the command comment's id and when it was posted.
func (w *Workflow) unpicked(pr *model.PR, now time.Time) (id int64, at time.Time, ok bool) {
	if w.win == nil || !w.cfg.ReleaseNowDispatch {
		return 0, time.Time{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t, found := w.progress[pr.Ident()]
	if !found {
		return 0, time.Time{}, false
	}
	if !t.notTold && (t.cur.Phase != PhaseDispatched || t.cur.Source != "command") {
		return 0, time.Time{}, false
	}
	id, at = t.nowCommentID, t.nowPostedAt
	for _, c := range t.comments {
		if c.ID != 0 && w.nowRE != nil && w.nowRE.MatchString(strings.TrimSpace(c.Body)) && (id == 0 || c.CreatedAt.After(at)) {
			id, at = c.ID, c.CreatedAt
		}
	}
	if id == 0 {
		return 0, time.Time{}, false
	}
	if t.notTold || now.Sub(at) >= refireAfter {
		return id, at, true
	}
	return 0, time.Time{}, false
}

// refire fires the release-now dispatch for an existing command comment:
// the payload names the comment, which the receiver re-reads by id, so
// this asks nothing the command did not already ask.
func (w *Workflow) refire(app workflow.App, pr *model.PR, wh string, id int64) {
	target := w.manifestRepo(app)
	if target.IsZero() {
		app.Notice("no manifest repo to tell")
		return
	}
	payload := map[string]any{"repo": pr.Repo.Name, "pr": pr.Number, "comment_id": id}
	if !app.Confirm(fmt.Sprintf("fire %s on %s for the %s comment on %s?\npayload: repo=%s pr=%d comment_id=%d",
		w.cfg.ReleaseNowDispatchEvent, target, w.cfg.ReleaseNowCommand, wh, pr.Repo.Name, pr.Number, id)) {
		app.Notice("dispatch cancelled")
		return
	}
	if !workflow.PostGuard(app, w.cfg.ReleaseNowDispatchEvent) {
		return
	}
	if err := app.GH().Dispatch(app.Context(), target, w.cfg.ReleaseNowDispatchEvent, payload); err != nil {
		app.Fail(fmt.Sprintf("%s: %s was not told (%s): press M to merge by hand", wh, target, err))
		return
	}
	w.noteRefired(pr, app.Now(), id)
	app.OK(fmt.Sprintf("%s: told %s about the posted %s; the release job picks it up within a minute", wh, target, w.cfg.ReleaseNowCommand))
	app.Launch()
	app.RefreshSoon(45 * time.Second)
}

// noteRefired records a dispatch fired for an existing command: the
// clock that decides "unpicked" starts again.
func (w *Workflow) noteRefired(pr *model.PR, now time.Time, id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.trackFor(pr, true, now)
	t.nowPostedAt, t.nowCommentID, t.notTold = now, id, false
	t.set(w.derive(pr, t, now))
}

// RunTargets offers a release's run in the manifest repo (the job that
// merges it at its slot or on release NOW) to x, for the selected
// release row or the open release.
func (w *Workflow) RunTargets(app workflow.App) []workflow.RunTarget {
	var pr *model.PR
	if app.CurrentView() == "detail" {
		pr, _ = app.Detail().(*model.PR)
	} else if row, ok := app.Selected(); ok {
		pr, _ = row.Item.(*model.PR)
	}
	if pr == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.progress[pr.Ident()]
	if !ok || t.run == nil {
		return nil
	}
	repo := w.manifestRepo(app)
	if repo.IsZero() {
		return nil
	}
	return []workflow.RunTarget{{Repo: repo, Run: *t.run, Why: "the release run for " + where(pr)}}
}
