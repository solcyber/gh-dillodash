package workflow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

func guardApp(t *testing.T, user, team string) *wftest.App {
	t.Helper()
	workflow.ResetMembershipCache()
	t.Cleanup(workflow.ResetMembershipCache)
	cfg := config.Default()
	cfg.GitHub.ApproversTeam = team
	app := wftest.New(cfg, user)
	app.Fake.User = user
	app.Fake.Teams = map[string][]string{"acme/release-approvers": {"alice", "carol"}}
	return app
}

func lastLog(app *wftest.App) string {
	if len(app.Log) == 0 {
		return ""
	}
	return app.Log[len(app.Log)-1]
}

// TestPostGuard (#24): a command a bot accepts only from an approver is
// refused before it is posted as a bot or as someone outside the team,
// with the login it would have used and the variable to unset.
func TestPostGuard(t *testing.T) {
	// a member posts; the answer is remembered for the session
	app := guardApp(t, "alice", "acme/release-approvers")
	if !workflow.PostGuard(app, "/release-now") || !workflow.PostGuard(app, "/release") {
		t.Errorf("a member posts: %q", app.Log)
	}
	if n := app.Fake.Reads["team_member"]; n != 1 {
		t.Errorf("one membership read per session, got %d", n)
	}

	// someone outside the team: refused, named, nothing else read
	app = guardApp(t, "bob", "acme/release-approvers")
	app.Fake.Source = "GITHUB_TOKEN"
	if workflow.PostGuard(app, "/release-now") {
		t.Error("bob is not an approver")
	}
	got := lastLog(app)
	for _, want := range []string{"notice: /release-now not posted", "bob is not in @acme/release-approvers", "the token comes from GITHUB_TOKEN — unset it"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal lacks %q: %q", want, got)
		}
	}

	// a bot login: refused even with no team configured
	app = guardApp(t, "infra-bot[bot]", "")
	if workflow.PostGuard(app, "/window-cancel rel:web-portal#9") || !strings.Contains(lastLog(app), "infra-bot[bot] is a bot login") {
		t.Errorf("a bot never posts: %q", app.Log)
	}
	if !strings.Contains(lastLog(app), "gh auth switch") {
		t.Errorf("a gh-login token gets the switch hint: %q", lastLog(app))
	}

	// no team: anyone not a bot posts, and nothing is read
	app = guardApp(t, "bob", "")
	if !workflow.PostGuard(app, "/attend") || app.Fake.Reads["team_member"] != 0 {
		t.Errorf("no team, no check: %q", app.Log)
	}

	// a team the token cannot read: a gold warning, then it posts
	app = guardApp(t, "bob", "acme/secret-team")
	if !workflow.PostGuard(app, "/approve") || !strings.Contains(lastLog(app), "couldn't read @acme/secret-team") ||
		!strings.Contains(lastLog(app), "Members read") {
		t.Errorf("unreadable team: warn and post: %q", app.Log)
	}

	// the check failing: warn and post, and the error is not remembered
	app = guardApp(t, "bob", "acme/release-approvers")
	app.Fake.Fail["team_member"] = errors.New("connection reset")
	if !workflow.PostGuard(app, "/release") || !strings.Contains(lastLog(app), "couldn't check bob is in @acme/release-approvers") {
		t.Errorf("a failed check posts with a warning: %q", app.Log)
	}
	delete(app.Fake.Fail, "team_member")
	if workflow.PostGuard(app, "/release") {
		t.Error("the next check reads again and refuses")
	}
}
