package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

var oldHead = strings.Repeat("1", 40)

// approveOn marks p (and its detail twin) approved by who on sha.
func (h *harness) approveOn(n int, who, sha string) {
	h.t.Helper()
	for _, p := range []*model.PR{h.gh.Open[infra].PRs[0], h.gh.PRs[model.Ident{Repo: infra, Number: n}]} {
		p.ReviewDecision = "APPROVED"
		p.Opinions.Nodes = []model.Review{{Author: model.Actor{Login: who}, State: "APPROVED", Commit: &model.ReviewCommit{Oid: sha}}}
	}
}

// TestStaleApprovalOfMineNeedsAReReview: my approval on an older commit
// is not "approved by me": the row says re-review, counts as needing me,
// and u opens the changes since the approval.
func TestStaleApprovalOfMineNeedsAReReview(t *testing.T) {
	h := newHarness(t)
	h.approveOn(101, "alice", oldHead)
	h.gh.Compares = map[string]ghapi.CompareResult{oldHead + "..." + strings.Repeat("a", 40): {AheadBy: 3}}
	h.load()
	s := h.screen()
	for _, want := range []string{"re-review", "stale: approved 1111111, head aaaaaaa (+3)", "1 need you", "u  since approval"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	h.key('u')
	want := []string{"https://github.com/acme/infra/compare/" + oldHead + "..." + strings.Repeat("a", 40)}
	if !slices.Equal(h.opened, want) {
		t.Fatalf("u opened %v, want %v", h.opened, want)
	}
	// the compare is asked once per pair, not every refresh
	h.key('r')
	if n := h.gh.Reads["compare"]; n != 1 {
		t.Errorf("compare calls = %d, want 1", n)
	}
	// the detail says why, and a is offered again (not "already approved")
	h.named("enter")
	if s := h.screen(); !strings.Contains(s, "the changes since the approval") {
		t.Errorf("detail lacks the stale line:\n%s", s)
	}
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('a')
	if len(h.gh.Approvals) != 1 {
		t.Errorf("re-approve after the head moved: approvals %v status %q", h.gh.Approvals, h.m.status)
	}
}

// TestStuckApprovalByOthersStaysListed: an approval by someone else used
// to hide the PR; a conflicting one stays, with its reason.
func TestStuckApprovalByOthersStaysListed(t *testing.T) {
	h := newHarness(t)
	h.approveOn(101, "bob2", strings.Repeat("a", 40))
	h.gh.Open[infra].PRs[0].MergeStateStatus = "DIRTY"
	h.gh.PRs[model.Ident{Repo: infra, Number: 101}].MergeStateStatus = "DIRTY"
	h.load()
	s := h.screen()
	for _, want := range []string{"approved · stuck", "conflicts with base"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "u  since approval") {
		t.Error("u is offered only for a stale approval")
	}
	// M refuses what GitHub says cannot merge
	h.named("enter")
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('M')
	if len(h.gh.Merges) != 0 || !strings.Contains(h.m.status, "not mergeable: GitHub says dirty") {
		t.Errorf("merges %v status %q", h.gh.Merges, h.m.status)
	}
}

// TestCleanApprovalByOthersStaysHidden: the old rule is unchanged for an
// approved PR that will merge.
func TestCleanApprovalByOthersStaysHidden(t *testing.T) {
	h := newHarness(t)
	h.approveOn(101, "bob2", strings.Repeat("a", 40))
	h.gh.Open[infra].PRs[0].MergeStateStatus = "CLEAN"
	h.load()
	if s := h.screen(); strings.Contains(s, "#101") {
		t.Errorf("a clean approved PR should be hidden until A:\n%s", s)
	}
}
