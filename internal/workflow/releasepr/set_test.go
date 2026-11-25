package releasepr

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

var migrations = model.Repo{Owner: "acme", Name: "db-migrations"}

// setWorld is a window night with set_approve on and three releases
// ready, listed out of lane order: etl-jobs#158, web-portal#212,
// db-migrations#64. The lane is db-migrations, web-portal, etl-jobs.
func setWorld(t *testing.T, extra ...*model.PR) (*Workflow, *wftest.App, []*model.PR) {
	t.Helper()
	w, app := newWF(t, true)
	w.cfg.SetApprove, w.cfg.ServerLane = true, true
	w.cfg.LaneOrder = []string{"db-migrations", "web-portal", "etl-jobs"}
	rels := []*model.PR{
		makeRelease(relOpts{number: 158, repo: etl, head: "e158e158"}),
		makeRelease(relOpts{number: 212, repo: portal, head: "a212a212"}),
		makeRelease(relOpts{number: 64, repo: migrations, head: "d064d064"}),
	}
	st := &State{w: w, Releases: append(append([]*model.PR{}, rels...), extra...)}
	st.index()
	app.SetState(Name, st)
	return w, app, rels
}

func numbers(calls []int) []int { return append([]int{}, calls...) }

// TestSetApproveQueuesTheWholeRelease: one A, one pick, one y posts a
// review on every release, each pinned to its head, in lane order, and
// nothing else.
func TestSetApproveQueuesTheWholeRelease(t *testing.T) {
	patchNow(t, outWindow) // 15:00 ET, before the freeze
	w, app, rels := setWorld(t)
	app.Picks, app.Confirms = []int{0}, []bool{true}
	if !w.Approve(app, rels[0], rels[0]) {
		t.Fatal("handled")
	}
	// a menu is its title, then its choices
	if len(app.Menus) != 1 || !strings.HasPrefix(app.Menus[0][1], "approve the whole release (3): queue every one for") ||
		!strings.HasPrefix(app.Menus[0][2], "release the whole set NOW (3)") {
		t.Fatalf("the set leads the menu: %q", app.Menus)
	}
	if got := numbers(app.Fake.Approvals); len(got) != 3 || got[0] != 64 || got[1] != 212 || got[2] != 158 {
		t.Fatalf("every release approved, in lane order: %v", got)
	}
	heads := map[int]string{64: "d064d064", 212: "a212a212", 158: "e158e158"}
	for _, c := range app.Fake.CallsFor("approve") {
		if c.SHA != heads[c.Number] {
			t.Errorf("#%d approved at %q, not its head %q", c.Number, c.SHA, heads[c.Number])
		}
	}
	if len(app.Fake.CommentsPosted) != 0 || len(app.Fake.Dispatches) != 0 {
		t.Errorf("queue posts reviews only: %v %v", app.Fake.CommentsPosted, app.Fake.Dispatches)
	}
	q := app.Questions[0]
	if !strings.Contains(q, "approve 3 releases as alice? (queues every one for") ||
		strings.Index(q, "db-migrations#64 at d064d06") > strings.Index(q, "web-portal#212") ||
		strings.Index(q, "web-portal#212") > strings.Index(q, "etl-jobs#158") {
		t.Errorf("the confirm lists every release in lane order with its head: %q", q)
	}
	if !logHas(app, "ok", "approved 3 releases as alice — every one queued for") {
		t.Errorf("status: %q", app.Log)
	}
}

// TestSetReleaseNow: one reason, then the command on every release in
// lane order, each followed by its dispatch naming that comment.
func TestSetReleaseNow(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[1], rels[1])
	if len(app.Prompted) != 1 {
		t.Fatalf("one reason for the whole set: %q", app.Prompted)
	}
	if got := numbers(app.Fake.Approvals); len(got) != 3 {
		t.Fatalf("every review first: %v", got)
	}
	want := `/release-now "` + reason + `"`
	var order []int
	for _, c := range app.Fake.CommentsPosted {
		if c.Body != want {
			t.Errorf("exact command: %q", c.Body)
		}
		order = append(order, c.Number)
	}
	if len(order) != 3 || order[0] != 64 || order[1] != 212 || order[2] != 158 {
		t.Fatalf("one command per release, in lane order: %v", order)
	}
	if len(app.Fake.Dispatches) != 3 {
		t.Fatalf("one dispatch per command: %v", app.Fake.Dispatches)
	}
	for i, d := range app.Fake.Dispatches {
		c := app.Fake.CommentsPosted[i]
		if d.Event != "release-now" || d.Payload["pr"] != c.Number || d.Payload["comment_id"] != c.ID || d.Payload["repo"] != c.Repo.Name ||
			d.Payload["set"] != "db-migrations#64,web-portal#212,etl-jobs#158" {
			t.Errorf("dispatch %d names its comment: %+v vs %+v", i, d, c)
		}
	}
	// every review lands before the first command
	firstComment := -1
	for i, c := range app.Fake.Calls {
		if c.Op == "comment" && firstComment < 0 {
			firstComment = i
		}
		if c.Op == "approve" && firstComment >= 0 {
			t.Errorf("a review after a command: %+v", app.Fake.Calls)
		}
	}
	if !logHas(app, "ok", "released the set as alice: /release-now posted on 3 releases — the release lane runs them in lane order, one at a time") {
		t.Errorf("status: %q", app.Log)
	}
	if app.Launched == 0 {
		t.Error("the rocket")
	}
}

// TestSetLeavesOut: a draft, your own release, one with failing checks
// and one with changes requested are named as left out; a hotfix is not
// a candidate at all; fewer than two ready offers no set.
func TestSetLeavesOut(t *testing.T) {
	patchNow(t, outWindow)
	draft := makeRelease(relOpts{number: 70, repo: migrations, draft: true})
	mine := makeRelease(relOpts{number: 71, repo: portal, author: "alice"})
	red := makeRelease(relOpts{number: 72, repo: etl})
	red.Set(ExtCI, model.CICluster{Failed: []string{"checks"}})
	changes := makeRelease(relOpts{number: 73, repo: etl})
	changes.ReviewDecision = "CHANGES_REQUESTED"
	hot := makeRelease(relOpts{number: 74, repo: portal, kind: KindHotfix})
	w, app, rels := setWorld(t, draft, mine, red, changes, hot)
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	q := app.Questions[0]
	for _, s := range []string{"left out: db-migrations#70 (a draft)", "left out: web-portal#71 (yours", "left out: etl-jobs#72 (checks failing: checks)",
		"left out: etl-jobs#73 (changes requested)"} {
		if !strings.Contains(q, s) {
			t.Errorf("the confirm names %q: %q", s, q)
		}
	}
	if strings.Contains(q, "#74") {
		t.Errorf("a hotfix is never part of the set: %q", q)
	}
	if got := numbers(app.Fake.Approvals); len(got) != 3 {
		t.Errorf("only the three ready are approved: %v", got)
	}

	// one release ready: the plain menu, no set
	w, app = newWF(t, true)
	w.cfg.SetApprove = true
	only := makeRelease(relOpts{number: 9, repo: etl})
	st := &State{w: w, Releases: []*model.PR{only, makeRelease(relOpts{number: 10, repo: portal, draft: true})}}
	st.index()
	app.SetState(Name, st)
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, only, only)
	if strings.Contains(strings.Join(app.Menus[0], "|"), "whole") {
		t.Errorf("one ready release: no set: %q", app.Menus)
	}

	// set_approve off: no set however many are ready
	w, app, rels = setWorld(t)
	w.cfg.SetApprove = false
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if strings.Contains(strings.Join(app.Menus[0], "|"), "whole") || len(app.Fake.Approvals) != 1 {
		t.Errorf("set_approve off: the plain path: %q %v", app.Menus, app.Fake.Approvals)
	}
}

// TestSetNowPostsNothingWhenAReviewFails: a set with a hole in it never
// reaches the lane.
func TestSetNowPostsNothingWhenAReviewFails(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	app.Fake.Fail = map[string]error{"approve": errors.New("HTTP 502")}
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if len(app.Fake.CommentsPosted) != 0 || len(app.Fake.Dispatches) != 0 {
		t.Fatalf("no command after a failed review: %v", app.Fake.CommentsPosted)
	}
	if !logHas(app, "fail", "approved 0 of 3; nothing released — failed: db-migrations#64: HTTP 502") {
		t.Errorf("status: %q", app.Log)
	}
}

// TestSetNowStopsAtAFailedCommand: the commands after a failed one stay
// unposted, and the status line says which.
func TestSetNowStopsAtAFailedCommand(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	app.Fake.Fail = map[string]error{"comment": errors.New("HTTP 422")}
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if len(app.Fake.Dispatches) != 0 {
		t.Fatalf("nothing told after a failed command: %v", app.Fake.Dispatches)
	}
	if !logHas(app, "fail", "released 0 of 3; db-migrations#64: /release-now NOT posted (HTTP 422); not posted: web-portal#212, etl-jobs#158 — A on web-portal#212 releases those") {
		t.Errorf("status: %q", app.Log)
	}
}

// TestSetWithYourApprovalsStanding: a release you approved on this head
// gets no second review; with every one approved the set leads the
// approved menu and posts the commands only.
func TestSetWithYourApprovalsStanding(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	for _, r := range rels {
		approved(r, "alice")
	}
	app.Picks, app.Prompts, app.Confirms = []int{0}, []string{reason}, []bool{true}
	w.Approve(app, rels[2], rels[2])
	if !strings.HasPrefix(app.Menus[0][1], "release the whole set NOW (3)") || strings.Contains(strings.Join(app.Menus[0], "|"), "queue every one") {
		t.Fatalf("approved: the set's NOW leads: %q", app.Menus)
	}
	if len(app.Fake.Approvals) != 0 || len(app.Fake.CommentsPosted) != 3 {
		t.Errorf("no second review; three commands: %v %v", app.Fake.Approvals, app.Fake.CommentsPosted)
	}
	if !strings.Contains(app.Questions[0], "(your approval stands)") {
		t.Errorf("the confirm says whose approval stands: %q", app.Questions[0])
	}

	// one of three approved: two reviews, and the confirm marks the one
	w, app, rels = setWorld(t)
	approved(rels[1], "alice")
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if got := numbers(app.Fake.Approvals); len(got) != 2 || got[0] != 64 || got[1] != 158 {
		t.Errorf("only the two not yet approved: %v", got)
	}
}

// TestSetHeadMoved: a head that moved while the confirm was up is not
// approved, and NOW posts nothing.
func TestSetHeadMoved(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	moved := *rels[1]
	moved.HeadRefOid = "ffff0000"
	app.Fake.PRs = map[model.Ident]*model.PR{rels[1].Ident(): &moved}
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if len(app.Fake.CommentsPosted) != 0 {
		t.Fatalf("a moved head stops NOW: %v", app.Fake.CommentsPosted)
	}
	if !logHas(app, "fail", "web-portal#212: its head moved") {
		t.Errorf("status names the moved head: %q", app.Log)
	}
	for _, n := range app.Fake.Approvals {
		if n == 212 {
			t.Errorf("the moved release got a review: %v", app.Fake.Approvals)
		}
	}
}

// TestServerLanePhases: the release lane's labels read as phases, and
// only a held release is the viewer's move.
func TestServerLanePhases(t *testing.T) {
	w, app := newWF(t, true)
	w.cfg.ServerLane = true
	t0 := utc(3, 0)
	pr := approved(makeRelease(relOpts{}), "alice")
	posted := func(tr *track) { tr.nowPostedAt = t0.Add(-10 * time.Minute) }

	pr.Labels = model.Labels{Nodes: []model.Label{{Name: "release:now-queued"}}}
	p := deriveWith(w, pr, posted, t0)
	if p.Phase != PhaseLaneQueued || p.Terminal || !strings.HasPrefix(w.progressChipText(p, "10m"), "queued in lane") {
		t.Errorf("queued: %+v", p)
	}
	if progressStyle(p) != "chip.sched" {
		t.Errorf("queued style: %s", progressStyle(p))
	}

	pr.Labels = model.Labels{Nodes: []model.Label{{Name: "release:held"}}}
	p = deriveWith(w, pr, posted, t0)
	if p.Phase != PhaseHeld || !p.Terminal || w.progressChipText(p, "") != "held · A" || progressStyle(p) != "chip.act" {
		t.Errorf("held: %+v", p)
	}
	// a command this session just posted re-queues it: the label lags
	p = deriveWith(w, pr, func(tr *track) { tr.nowPostedAt = t0.Add(-30 * time.Second) }, t0)
	if p.Phase == PhaseHeld {
		t.Errorf("just re-posted: not held any more: %+v", p)
	}

	// without server_lane the labels mean nothing here
	w.cfg.ServerLane = false
	if p = deriveWith(w, pr, posted, t0); p.Phase == PhaseHeld || p.Phase == PhaseLaneQueued {
		t.Errorf("server_lane off: %+v", p)
	}

	// turn: held is yours (A), queued is the lane's
	w.cfg.ServerLane = true
	w.mu.Lock()
	w.progress[pr.Ident()] = &track{repo: pr.Repo, number: pr.Number, cur: Progress{Phase: PhaseHeld, Terminal: true}}
	w.mu.Unlock()
	if tu := w.turn(app, pr); tu.Kind != workflow.TurnYou || tu.Key != "A" {
		t.Errorf("held is the viewer's move: %+v", tu)
	}
	w.mu.Lock()
	w.progress[pr.Ident()].cur = Progress{Phase: PhaseLaneQueued}
	w.mu.Unlock()
	if tu := w.turn(app, pr); tu.Kind != workflow.TurnOthers || tu.What != "the release lane" {
		t.Errorf("queued waits on the lane: %+v", tu)
	}
}

// TestServerLaneNeverWaits: with the manifest repo keeping the order, A
// on the next release asks nothing about the one ahead.
func TestServerLaneNeverWaits(t *testing.T) {
	patchNow(t, utc(3, 30))
	first := mergedRelease()
	first.Repo, first.Number = portal, 743
	first.Set(ExtDeploy, deploys.State{Phase: "running"}) // still deploying: without the lane, A would ask
	next := approved(makeRelease(relOpts{number: 158, repo: etl}), "alice")
	w, _ := laneState(t, first, next)
	w.cfg.ServerLane = true
	_, app := newWF(t, true)
	if !w.laneWait(app, next) || len(app.Questions) != 0 {
		t.Errorf("server lane: no question: %q", app.Questions)
	}
	if got := w.laneNote(first); got != "" {
		t.Errorf("server lane: no next-in-lane note: %q", got)
	}
}

// TestSetNeverSendsTwice: a member this session just sent, or one the
// lane holds in its queue, is not offered again, even while its row
// still reads scheduled on tonight's manifest.
func TestSetNeverSendsTwice(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	for _, r := range rels {
		r.Labels = model.Labels{Nodes: []model.Label{{Name: "window:scheduled"}}}
	}
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if len(app.Fake.CommentsPosted) != 3 {
		t.Fatalf("first set NOW: %v", app.Fake.CommentsPosted)
	}
	app.Picks, app.Confirms = []int{0}, []bool{false}
	w.Approve(app, rels[1], rels[1])
	if strings.Contains(strings.Join(app.Menus[len(app.Menus)-1], "|"), "whole set NOW") || len(app.Fake.CommentsPosted) != 3 {
		t.Errorf("no second set NOW: %q", app.Menus[len(app.Menus)-1])
	}

	// a member already waiting in the lane is not a candidate
	w, app, rels = setWorld(t)
	rels[0].Labels = model.Labels{Nodes: []model.Label{{Name: "release:now-queued"}}}
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, rels[1], rels[1])
	if got := numbers(app.Fake.Approvals); len(got) != 2 {
		t.Errorf("the queued one is left alone: %v", got)
	}
}

// TestSetDispatchFails: a dispatch that surely failed stops the set and
// says how to fire it again; one that may have landed is reported, not
// hidden behind a success line.
func TestSetDispatchFails(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	app.Fake.AfterCall = func(c fake.Call) {
		if c.Op == "dispatch" && len(app.Fake.Dispatches) == 1 {
			app.Fake.Fail = map[string]error{"dispatch": &ghapi.Error{Status: 422, Message: "bad payload"}}
		}
	}
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if len(app.Fake.CommentsPosted) != 2 {
		t.Fatalf("stops after the member whose dispatch failed: %v", app.Fake.CommentsPosted)
	}
	if !logHas(app, "fail", "released 1 of 3; web-portal#212: posted /release-now, but acme/infra was not told") ||
		!logHas(app, "fail", "A on its row fires the dispatch again; not posted: etl-jobs#158 — A on etl-jobs#158 releases those") {
		t.Errorf("status: %q", app.Log)
	}

	w, app, rels = setWorld(t)
	app.Fake.Fail = map[string]error{"dispatch": &ghapi.Error{Status: 502, Message: "bad gateway"}}
	app.Picks, app.Prompts, app.Confirms = []int{1}, []string{reason}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if len(app.Fake.CommentsPosted) != 3 || logHas(app, "ok", "released the set") ||
		!logHas(app, "fail", "telling the manifest repo may have failed for db-migrations#64, web-portal#212, etl-jobs#158") {
		t.Errorf("uncertain dispatches are reported: %q", app.Log)
	}
}

// TestSetNowNeedsTheLane: without server_lane the set offers no NOW
// (its commands would all start at once); a read-only repo is left out.
func TestSetNowNeedsTheLane(t *testing.T) {
	patchNow(t, outWindow)
	w, app, rels := setWorld(t)
	w.cfg.ServerLane = false
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if m := strings.Join(app.Menus[0], "|"); strings.Contains(m, "whole set NOW") || !strings.Contains(m, "approve the whole release (3)") {
		t.Errorf("no lane: queue only: %q", m)
	}

	w, app, rels = setWorld(t)
	app.Cfg.GitHub.AllowedRepos = []model.Repo{portal, etl}
	app.Picks, app.Confirms = []int{0}, []bool{true}
	w.Approve(app, rels[0], rels[0])
	if !strings.Contains(app.Questions[0], "left out: db-migrations#64 (read-only here (allowed_repos))") || len(app.Fake.Approvals) != 2 {
		t.Errorf("a read-only repo is left out: %q %v", app.Questions[0], app.Fake.Approvals)
	}
}

// TestServerLaneLabelsYield: the lane's labels win over a scheduled
// item, and lose to a refusal, an outcome or a merge.
func TestServerLaneLabelsYield(t *testing.T) {
	w, app := newWF(t, true)
	w.cfg.ServerLane = true
	t0 := utc(3, 0)
	pr := approved(makeRelease(relOpts{}), "alice")
	pr.Labels = model.Labels{Nodes: []model.Label{{Name: "release:now-queued"}, {Name: "window:scheduled"}}}
	cmd := model.Comment{ID: 5, Body: "/release-now \"" + reason + "\"", CreatedAt: t0.Add(-5 * time.Minute), Author: model.Actor{Login: "alice"}}
	if p := deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd} }, t0); p.Phase != PhaseLaneQueued {
		t.Errorf("queued wins over the window's scheduled label: %+v", p)
	}
	ref := model.Comment{ID: 6, Body: "**/release-now refused:** at its turn in the release lane its head moved", CreatedAt: t0.Add(-time.Minute)}
	if p := deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd, ref} }, t0); p.Phase != PhaseRefused {
		t.Errorf("a refusal after the command wins: %+v", p)
	}
	pr.Labels = model.Labels{Nodes: []model.Label{{Name: "release:held"}, {Name: "window:failed"}}}
	if p := deriveWith(w, pr, func(tr *track) { tr.comments = []model.Comment{cmd} }, t0); p.Phase != PhaseFailed {
		t.Errorf("an outcome label wins over held: %+v", p)
	}
	merged := mergedRelease()
	merged.Labels = model.Labels{Nodes: []model.Label{{Name: "release:held"}}}
	if p := deriveWith(w, merged, nil, t0); p.Phase == PhaseHeld {
		t.Errorf("a merged release is never held: %+v", p)
	}
	// the author of a held release waits for an approver
	pr.Labels = model.Labels{Nodes: []model.Label{{Name: "release:held"}}}
	pr.Author = model.Actor{Login: "alice"}
	w.mu.Lock()
	w.progress[pr.Ident()] = &track{repo: pr.Repo, number: pr.Number, cur: Progress{Phase: PhaseHeld, Terminal: true}}
	w.mu.Unlock()
	if tu := w.turn(app, pr); tu.Kind != workflow.TurnOthers {
		t.Errorf("held, but yours: an approver's move: %+v", tu)
	}
	// refire and M stay closed while the lane holds an accepted command
	w.mu.Lock()
	w.progress[pr.Ident()] = &track{repo: pr.Repo, number: pr.Number, nowPostedAt: t0.Add(-30 * time.Minute), nowCommentID: 5,
		cur: Progress{Phase: PhaseDispatched, Source: "command", Since: t0.Add(-30 * time.Minute)}}
	w.mu.Unlock()
	if _, _, ok := w.unpicked(pr, t0); ok {
		t.Error("server lane: no refire offer for an accepted command")
	}
}
