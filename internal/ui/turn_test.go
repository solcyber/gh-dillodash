package ui

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// TestMyOwnPRWaitingOnReviewIsNotMyTurn: a PR I opened that nobody has
// reviewed waits on its reviewers; it is mine to move only when it is
// stuck on something I clear.
func TestMyOwnPRWaitingOnReviewIsNotMyTurn(t *testing.T) {
	h := newHarness(t)
	mine := pr(infra, 103, "my change", "alice", false)
	h.gh.Open[infra] = appendPR(h.gh.Open[infra], mine)
	h.load()
	if got := h.m.prTurn(mine); got.Kind != workflow.TurnOthers {
		t.Errorf("my unreviewed PR: %+v", got)
	}
	for _, r := range h.m.rows {
		if pinned(r) && r.Ident() == mine.Ident() {
			t.Error("my unreviewed PR is pinned in your turn")
		}
	}
	if s := h.screen(); !strings.Contains(s, "1 on you: 1 review") {
		t.Errorf("only bob's PR is on me:\n%s", s)
	}
}

// TestTheCursorStartsOnYourTurn: the first whole load puts the cursor on
// the head of "your turn"; n walks it and wraps.
func TestTheCursorStartsOnYourTurn(t *testing.T) {
	h := newHarness(t)
	h.load()
	sel, ok := h.m.Selected()
	if !ok || !pinned(sel) || sel.Ident().Number != 101 {
		t.Fatalf("cursor on %+v, want the pinned #101", sel)
	}
	h.key('j')
	if sel, _ := h.m.Selected(); pinned(sel) {
		t.Fatal("j leaves the section")
	}
	h.key('n')
	if sel, _ := h.m.Selected(); !pinned(sel) {
		t.Error("n goes back to the section")
	}
}

// TestAMergedPRsDeployOnYouIsYourTurn: a PR merged from here whose deploy
// waits on the viewer is theirs, with D; one only deploying is not.
func TestAMergedPRsDeployOnYouIsYourTurn(t *testing.T) {
	h := newHarness(t)
	h.load()
	p := pr(infra, 120, "merged here", "bob", false)
	p.State = "MERGED"
	p.Set("deploy", deploys.State{Phase: "waiting", Waits: []deploys.Wait{{EnvName: "prod", Mine: true, Due: true}}})
	if got := h.m.prTurn(p); got.Kind != workflow.TurnYou || got.Key != "D" || got.Verb != "approve prod" {
		t.Errorf("due: %+v", got)
	}
	p.Set("deploy", deploys.State{Phase: "running"})
	if got := h.m.prTurn(p); got.Kind != workflow.TurnRunning {
		t.Errorf("running: %+v", got)
	}
	if l, ok := deployOffer(p); ok {
		t.Errorf("D offered on a deploy that only runs: %q", l)
	}
}

func appendPR(r ghapi.ListResult, p *model.PR) ghapi.ListResult {
	r.PRs = append(r.PRs, p)
	return r
}
