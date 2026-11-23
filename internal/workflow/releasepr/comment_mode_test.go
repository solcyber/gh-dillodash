package releasepr

import (
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// commentMode is a workflow with release_now_dispatch = "comment": the
// app repo's own workflow tells the manifest repo.
func commentMode(t *testing.T) (*Workflow, func() *Workflow) {
	t.Helper()
	mk := func() *Workflow {
		w, _ := newWF(t, true)
		w.cfg.ReleaseNowDispatch, w.cfg.ReleaseNowCommentOnly = true, true
		return w
	}
	return mk(), mk
}

// TestCommentModePostsOnly: release NOW in comment mode posts the
// command and fires nothing; the row is on its way (dispatched) at once.
func TestCommentModePostsOnly(t *testing.T) {
	patchNow(t, outWindow)
	_, app := newWF(t, true)
	w, _ := commentMode(t)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	pr := makeRelease(relOpts{})
	w.Approve(app, pr, nil)
	if len(app.Fake.Dispatches) != 0 {
		t.Fatalf("comment mode fires no dispatch: %+v", app.Fake.Dispatches)
	}
	if got := comments(app); len(got) != 1 || !strings.HasPrefix(got[0], "/release-now \"") {
		t.Fatalf("the command is posted: %q", got)
	}
	if !logHas(app, "ok", "the repo's own workflow tells acme/infra") || app.Launched != 1 {
		t.Errorf("says who tells the manifest repo: %v (launched %d)", app.Log, app.Launched)
	}
	if p, ok := w.progressOf(pr); !ok || p.Phase != PhaseDispatched || p.Source != "command" {
		t.Errorf("on its way from the post: %+v", p)
	}
}

// TestCommentModeRefireWaitsFiveMinutes: the dispatch by hand is offered
// only once no run showed for unpickedAfter, not after refireAfter; it
// fires once, then the wait starts again.
func TestCommentModeRefireWaitsFiveMinutes(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, _ := commentMode(t)
	_, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{number: 43}), "alice")
	posted := utc(13, 57) // three minutes ago: past refireAfter, short of unpickedAfter
	w.mu.Lock()
	tr := w.trackFor(pr, true, posted)
	tr.comments = []model.Comment{{ID: 777, Author: model.Actor{Login: "alice"}, CreatedAt: posted, Body: `/release-now "` + reason + `"`}}
	tr.set(w.derive(pr, tr, utc(14, 0)))
	w.mu.Unlock()
	if _, _, ok := w.unpicked(pr, utc(14, 0)); ok {
		t.Error("three minutes: the repo's own workflow still has time")
	}
	if w.dispatchFailed(pr) {
		t.Error("three minutes: M still points at A")
	}
	app.Picks = nil
	w.Approve(app, pr, nil)
	if len(app.Fake.Dispatches) != 0 || !strings.Contains(app.Last(), "already on its way") {
		t.Errorf("nothing offered yet: %+v %q", app.Fake.Dispatches, app.Last())
	}

	patchNow(t, utc(14, 3)) // six minutes, no run
	id, _, ok := w.unpicked(pr, utc(14, 3))
	if !ok || id != 777 {
		t.Fatalf("six minutes: the fallback is offered for the posted comment: %d %v", id, ok)
	}
	if !w.dispatchFailed(pr) {
		t.Error("six minutes with no run: M may merge by hand")
	}
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, pr, nil)
	d := app.Fake.Dispatches
	if len(d) != 1 || d[0].Payload["comment_id"] != int64(777) || d[0].Event != "release-now" {
		t.Fatalf("one dispatch naming the comment: %+v (%q)", d, app.Last())
	}
	if len(comments(app)) != 0 {
		t.Errorf("no new comment: %q", comments(app))
	}
	if _, _, ok := w.unpicked(pr, utc(14, 4)); ok {
		t.Error("fired once: the wait starts again")
	}
}

// TestCommentModeMergeGate: outside the window M points at A until the
// posted command has gone unpickedAfter with no run.
func TestCommentModeMergeGate(t *testing.T) {
	patchNow(t, outWindow)
	_, mk := commentMode(t)
	w := mk()
	_, app := newWF(t, true)
	if ok, handled := w.MergeConfirm(app, mergeableRelease()); ok || !handled ||
		!logHas(app, "notice", "release NOW posts /release-now, which tells acme/infra") {
		t.Errorf("M points at A: %v %v %q", ok, handled, app.Log)
	}
	w = mk()
	_, app = newWF(t, true)
	w.notePosted(mergeableRelease(), outWindow.Add(-6*time.Minute), true)
	app.Picks = []int{0}
	if w.MergeConfirm(app, mergeableRelease()); len(app.Menus) != 1 {
		t.Errorf("six minutes with no run: M offers the emergency merge: %v %q", app.Menus, app.Log)
	}
}

// TestDispatchFalseIsNotCommentMode: release_now_dispatch = false posts
// the command, never offers a dispatch, and leaves M open.
func TestDispatchFalseIsNotCommentMode(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	w.cfg.ReleaseNowDispatch, w.cfg.ReleaseNowCommentOnly = false, false
	pr := approved(makeRelease(relOpts{number: 44}), "alice")
	posted := utc(13, 0)
	w.mu.Lock()
	tr := w.trackFor(pr, true, posted)
	tr.comments = []model.Comment{{ID: 9, Author: model.Actor{Login: "alice"}, CreatedAt: posted, Body: `/release-now "` + reason + `"`}}
	tr.set(w.derive(pr, tr, utc(14, 0)))
	w.mu.Unlock()
	if _, _, ok := w.unpicked(pr, utc(14, 0)); ok {
		t.Error("false: no refire, however long it waits")
	}
	patchNow(t, outWindow)
	app.Picks = []int{0}
	if w.MergeConfirm(app, mergeableRelease()); len(app.Menus) != 1 {
		t.Errorf("false: M is the way at once: %v %q", app.Menus, app.Log)
	}
}
