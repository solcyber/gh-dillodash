package releasepr

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// Request changes takes a release out before the freeze: from the
// approve menu, with a reason posted as the review, and no approval.
func TestRequestChangesFromTheMenu(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	pr := makeRelease(relOpts{})
	app.Picks, app.Prompts, app.Confirms = []int{2}, []string{"the cut carries an unfinished feature"}, []bool{true}
	if !w.Approve(app, pr, nil) {
		t.Fatal("handled")
	}
	got := app.Fake.CallsFor("request_changes")
	if len(got) != 1 || got[0].Number != 42 || got[0].Body != "the cut carries an unfinished feature" {
		t.Fatalf("one request-changes review with the reason: %+v", got)
	}
	if len(app.Fake.Approvals) != 0 || len(comments(app)) != 0 {
		t.Errorf("no approval, no comment: %v %q", app.Fake.Approvals, comments(app))
	}
	if !strings.Contains(app.Last(), "out of the window") {
		t.Errorf("status: %q", app.Last())
	}

	// a short reason is refused before anything is posted
	w, app = newWF(t, true)
	app.Picks, app.Prompts = []int{2}, []string{"no"}
	w.Approve(app, pr, nil)
	if len(app.Fake.CallsFor("request_changes")) != 0 || !strings.Contains(app.Last(), "cancelled") {
		t.Errorf("a short reason posts nothing: %+v %q", app.Fake.Calls, app.Last())
	}
}

// Dismiss withdraws your own approval by its review id, so a later a
// approves (and queues) it again; the local mark goes too.
func TestDismissMyApproval(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	pr.Opinions.Nodes[0].ID = 9001
	app.Approved[pr.Ident()] = pr.HeadRefOid
	app.Picks, app.Confirms = []int{1}, []bool{true}
	w.Approve(app, pr, nil)
	got := app.Fake.CallsFor("dismiss_review")
	if len(got) != 1 || got[0].ID != 9001 || got[0].Number != 42 || !strings.Contains(got[0].Body, "alice") {
		t.Fatalf("one dismissal of review 9001: %+v", got)
	}
	if app.LocalApproved(pr) {
		t.Error("the local approval mark is forgotten")
	}
	if !strings.Contains(app.Last(), "re-queue") {
		t.Errorf("status says how to re-queue: %q", app.Last())
	}

	// no review id on the row: read the PR fresh
	w, app = newWF(t, true)
	pr = approved(makeRelease(relOpts{}), "alice")
	fresh := *pr
	fresh.Reviews.Nodes = []model.Review{{ID: 77, Author: model.Actor{Login: "alice"}, State: "APPROVED"}}
	app.Fake.PRs = map[model.Ident]*model.PR{pr.Ident(): &fresh}
	app.Picks, app.Confirms = []int{1}, []bool{true}
	w.Approve(app, pr, nil)
	if got := app.Fake.CallsFor("dismiss_review"); len(got) != 1 || got[0].ID != 77 {
		t.Errorf("the fresh read's review id: %+v (%q)", got, app.Last())
	}
}

// A command that no run picked up (its dispatch failed, or it was posted
// from the PR page) gets the dispatch for that same comment from a: no
// second comment, no review.
func TestRefireTheDispatchForAPostedCommand(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{}), "alice")
	app.Fake.Fail["dispatch"] = errors.New("HTTP 502")
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, pr, nil)
	if len(comments(app)) != 1 || len(app.Fake.Dispatches) != 0 {
		t.Fatalf("posted, not told: %q %+v", comments(app), app.Fake.Dispatches)
	}
	id := app.Fake.CommentsPosted[0].ID
	delete(app.Fake.Fail, "dispatch")
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, pr, nil)
	d := app.Fake.Dispatches
	if len(d) != 1 || d[0].Payload["comment_id"] != id || d[0].Event != "release-now" {
		t.Fatalf("the dispatch names the posted comment: %+v (%q)", d, app.Last())
	}
	if len(comments(app)) != 1 || len(app.Fake.Approvals) != 0 {
		t.Errorf("nothing else posted: %q %v", comments(app), app.Fake.Approvals)
	}
	if m := app.Menus[len(app.Menus)-1]; len(m) != 2 || !strings.Contains(m[1], "fire release-now") {
		t.Errorf("the menu offers only the dispatch: %q", m)
	}

	// posted from the PR page: the comment is read with its id, and the
	// dispatch is offered once it has waited refireAfter with no run
	w, app = newWF(t, true)
	pr = approved(makeRelease(relOpts{number: 43}), "alice")
	posted := utc(13, 50)
	w.mu.Lock()
	tr := w.trackFor(pr, true, posted)
	tr.comments = []model.Comment{{ID: 555, Author: model.Actor{Login: "bob"}, CreatedAt: posted, Body: `/release-now "` + reason + `"`}}
	tr.set(w.derive(pr, tr, utc(14, 0)))
	w.mu.Unlock()
	if _, _, ok := w.unpicked(pr, posted.Add(time.Minute)); ok {
		t.Error("too soon to offer it again")
	}
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, pr, nil)
	if d := app.Fake.Dispatches; len(d) != 1 || d[0].Payload["comment_id"] != int64(555) || d[0].Payload["pr"] != 43 {
		t.Fatalf("the dispatch names the page's comment: %+v (%q)", d, app.Last())
	}
	// and once told, it is on its way again: nothing more to offer
	app.Picks = nil
	w.Approve(app, pr, nil)
	if len(app.Fake.Dispatches) != 1 || !strings.Contains(app.Last(), "already on its way") {
		t.Errorf("told once: %+v %q", app.Fake.Dispatches, app.Last())
	}
}

// The menus an approver already knows keep their entries in place: the
// new review choices are only ever appended, so enter (the first entry)
// and the numbers 1-2 do what they did before.
func TestApproveMenusKeepTheirOrder(t *testing.T) {
	patchNow(t, utc(14, 0))
	cases := []struct {
		name string
		pr   *model.PR
		want []string
	}{
		{"release", makeRelease(relOpts{}), []string{"queue it for", "release NOW (/release-now)", "request changes"}},
		{"hotfix", makeRelease(relOpts{kind: KindHotfix}), []string{"release NOW (/release-now)", "approve review only", "request changes"}},
		{"approved by me", approved(makeRelease(relOpts{}), "alice"), []string{"release NOW (/release-now)", "dismiss my approval", "request changes"}},
	}
	for _, c := range cases {
		w, app := newWF(t, true)
		app.Picks = []int{-1}
		w.Approve(app, c.pr, nil)
		if len(app.Menus) != 1 {
			t.Fatalf("%s: one menu: %v", c.name, app.Menus)
		}
		labels := app.Menus[0][1:]
		if len(labels) != len(c.want) {
			t.Errorf("%s: %q", c.name, labels)
			continue
		}
		for i, p := range c.want {
			if !strings.HasPrefix(labels[i], p) {
				t.Errorf("%s: entry %d is %q, want %q first", c.name, i+1, labels[i], p)
			}
		}
	}
	// a hotfix still has no queue entry where approvals do not queue: the
	// approve-only entry keeps its place
	w, app := newWF(t, false)
	app.Picks = []int{-1}
	w.Approve(app, makeRelease(relOpts{}), nil)
	if l := app.Menus[0][1:]; len(l) != 3 || !strings.HasPrefix(l[0], "approve review only") || !strings.HasPrefix(l[2], "request changes") {
		t.Errorf("no window: %q", l)
	}
}
