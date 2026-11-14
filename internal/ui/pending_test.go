package ui

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// rowFor is the list row of id.
func (h *harness) rowFor(id model.Ident) (*model.PR, bool) {
	for _, r := range h.m.rows {
		if p, ok := r.Item.(*model.PR); ok && h.m.repoOf(p) == id.Repo && p.Number == id.Number {
			return p, true
		}
	}
	return nil, false
}

// TestMergeShowsOnTheListAtOnce: a merge flips the list row on the
// spot, and a refresh that set off before the merge does not flip it
// back; one that set off after it takes over.
func TestMergeShowsOnTheListAtOnce(t *testing.T) {
	h := newHarness(t)
	id := model.Ident{Repo: infra, Number: 101}
	h.gh.PRs[id].ReviewDecision = "APPROVED"
	h.gh.PRs[id].MergeStateStatus = "CLEAN"
	h.load()
	h.named("enter")
	h.m.tp.Confirm = func(string) bool { return true }
	before := fx.Now()
	g := holdOp(h, "list") // the refresh after the merge stays out
	h.m.Update(runeKey('M'))
	waitAction(h)
	if len(h.gh.Merges) != 1 {
		t.Fatalf("merges = %v (status %q)", h.gh.Merges, h.m.status)
	}
	row, ok := h.rowFor(id)
	if !ok || row.State != "MERGED" {
		t.Fatalf("the list row is not merged at once: %+v", row)
	}
	if label, _, _, _ := h.m.decision(row, nil); label != "merged" {
		t.Errorf("row chip = %q, want merged", label)
	}
	// a refresh that set off before the merge lands with the old state
	stale := pr(infra, 101, "widen the deploy role for infra", "bob", false)
	h.m.Update(refreshDoneMsg{&refreshResult{started: before, allPRs: []*model.PR{stale}}})
	if row, _ := h.rowFor(id); row == nil || row.State != "MERGED" {
		t.Errorf("an older refresh put the row back: %+v", row)
	}
	// one that set off after the merge read GitHub's answer itself
	h.m.Update(refreshDoneMsg{&refreshResult{started: fx.Now() + 1, allPRs: nil}})
	if len(h.m.pending) != 0 {
		t.Errorf("pending edits after a newer refresh: %v", h.m.pending)
	}
	g.release()
	h.pump()
}

// waitAction applies messages until the action in flight has ended.
func waitAction(h *harness) {
	for i := 0; i < 300; i++ {
		settleBriefly(h)
		h.m.mu.Lock()
		done := !h.m.action
		h.m.mu.Unlock()
		if done {
			return
		}
	}
	h.t.Fatal("the action never ended")
}

// TestCommentShowsAtOnce: a posted comment is on the open detail when
// the post returns, before any read of the detail lands.
func TestCommentShowsAtOnce(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "ship it now", true }
	g := holdOp(h, "pr_detail") // the re-read after the post stays out
	h.m.Update(runeKey('C'))
	waitAction(h)
	if !strings.Contains(h.screen(), "ship it now") {
		t.Errorf("the comment is not on the detail before a read:\n%s", h.screen())
	}
	g.release()
	h.pump()
}
