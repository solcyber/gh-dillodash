package workflow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

var postRepo = model.Repo{Owner: "acme", Name: "infra"}

// TestApprovePinsTheHead: the review names the commit the reviewer read.
func TestApprovePinsTheHead(t *testing.T) {
	app := wftest.New(config.Default(), "alice")
	if err := app.GH().Approve(context.Background(), postRepo, 7, "", "abc1234"); err != nil {
		t.Fatal(err)
	}
	if c := app.Fake.Calls[len(app.Fake.Calls)-1]; c.Op != "approve" || c.SHA != "abc1234" {
		t.Errorf("approve call = %+v, want SHA abc1234", c)
	}
}

// TestHeadStill: a head that moved while the confirm was up is refused.
func TestHeadStill(t *testing.T) {
	app := wftest.New(config.Default(), "alice")
	shown := &model.PR{Repo: postRepo, Number: 7, HeadRefOid: "aaaaaaa1"}
	app.Fake.PRs = map[model.Ident]*model.PR{{Repo: postRepo, Number: 7}: {Repo: postRepo, Number: 7, HeadRefOid: "bbbbbbb2"}}
	if workflow.HeadStill(app, shown, "approval") {
		t.Fatalf("a moved head must refuse: %q", app.Log)
	}
	if l := lastLog(app); !strings.Contains(l, "moved from aaaaaaa to bbbbbbb") {
		t.Errorf("notice = %q", l)
	}
	app.Fake.PRs[model.Ident{Repo: postRepo, Number: 7}].HeadRefOid = "aaaaaaa1"
	if !workflow.HeadStill(app, shown, "approval") {
		t.Errorf("the same head posts: %q", app.Log)
	}
	delete(app.Fake.PRs, model.Ident{Repo: postRepo, Number: 7})
	if !workflow.HeadStill(app, shown, "approval") {
		t.Errorf("an unreadable head allows (the review is pinned anyway): %q", app.Log)
	}
}

// TestPostAmbiguous: a timed-out post that landed counts as posted; one
// that can't be checked says "may have posted"; a 4xx is NOT posted.
func TestPostAmbiguous(t *testing.T) {
	app := wftest.New(config.Default(), "alice")
	id := model.Ident{Repo: postRepo, Number: 7}
	app.Fake.Fail["comment"] = &ghapi.Error{Message: "timed out"}
	app.Fake.Comments = map[model.Ident][]model.Comment{id: {{ID: 99, Author: model.Actor{Login: "alice"}, CreatedAt: clock.Now(), Body: "/approve x"}}}
	if got, unsure, err := workflow.Post(app, postRepo, 7, "/approve x"); err != nil || unsure || got != 99 {
		t.Errorf("landed post: id=%d unsure=%v err=%v", got, unsure, err)
	}
	app.Fake.Comments = nil
	if _, unsure, err := workflow.Post(app, postRepo, 7, "/approve x"); err == nil || unsure {
		t.Errorf("not on the thread: unsure=%v err=%v, want a clean failure", unsure, err)
	}
	app.Fake.Fail["comments"] = &ghapi.Error{Message: "timed out"}
	app.Fake.Fail["issue_detail"] = &ghapi.Error{Message: "timed out"}
	_, unsure, err := workflow.Post(app, postRepo, 7, "/approve x")
	if err == nil || !unsure {
		t.Errorf("unreadable thread: unsure=%v err=%v, want unsure", unsure, err)
	}
	workflow.PostFailed(app, "", "/approve", unsure, err)
	if l := lastLog(app); !strings.Contains(l, "may have posted") {
		t.Errorf("status = %q", l)
	}
	app.Fake.Fail["comment"] = &ghapi.Error{Status: 422, Message: "Unprocessable"}
	if _, unsure, err := workflow.Post(app, postRepo, 7, "/approve x"); err == nil || unsure {
		t.Errorf("a 422 is NOT posted, surely: unsure=%v err=%v", unsure, err)
	}
}
