package workflow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// TestPostGuardStrict: with approvers_team_strict a team the token cannot
// read, or a check that fails, refuses the command instead of warning
// and posting unchecked; a member and a non-member behave as before.
func TestPostGuardStrict(t *testing.T) {
	app := guardApp(t, "bob", "acme/secret-team")
	app.Cfg.GitHub.ApproversTeamStrict = true
	if workflow.PostGuard(app, "/approve") {
		t.Error("strict: an unreadable team refuses")
	}
	if got := lastLog(app); !strings.Contains(got, "/approve not posted") || !strings.Contains(got, "approvers_team_strict") || !strings.Contains(got, "Members read") {
		t.Errorf("strict refusal: %q", got)
	}

	app = guardApp(t, "bob", "acme/release-approvers")
	app.Cfg.GitHub.ApproversTeamStrict = true
	app.Fake.Fail["team_member"] = errors.New("connection reset")
	if workflow.PostGuard(app, "/approve") || !strings.Contains(lastLog(app), "approvers_team_strict refuses") {
		t.Errorf("strict: a failed check refuses: %q", app.Log)
	}

	app = guardApp(t, "alice", "acme/release-approvers")
	app.Cfg.GitHub.ApproversTeamStrict = true
	if !workflow.PostGuard(app, "/approve") {
		t.Errorf("strict: a member posts: %q", app.Log)
	}
	app = guardApp(t, "bob", "acme/release-approvers")
	app.Cfg.GitHub.ApproversTeamStrict = true
	if workflow.PostGuard(app, "/approve") {
		t.Error("strict: a non-member is refused")
	}
}
