package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
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
	h.key('A')
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

// TestRequestedReposListOnlyMyPRs: a requested repo's PRs come from the
// review-request search alone; its issues and other PRs never list.
func TestRequestedReposListOnlyMyPRs(t *testing.T) {
	h := newHarness(t)
	etl := model.Repo{Owner: "acme", Name: "etl"}
	asked := pr(etl, 1012, "retry the nightly load", "infra-bot", false)
	asked.BaseRefName = "dev"
	h.gh.Requested = []*model.PR{asked}
	h.m.requested = []model.Repo{etl}
	h.m.multi = true
	h.load()
	s := h.screen()
	for _, want := range []string{"etl#1012", "retry the nightly load", "+1 requested"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if n := h.gh.Reads["requested"]; n != 1 {
		t.Errorf("requested reads %d", n)
	}
	// a search that fails warns; the list still lands
	h.gh.Fail = map[string]error{"requested": &ghapi.Error{Message: "boom"}}
	h.key('r')
	if s := h.screen(); !strings.Contains(s, "#101") {
		t.Errorf("the review repo's list must survive a failed search:\n%s", s)
	}
}

// TestOpenFirstTierListsUnrequestedPRs: with promote's first_tier_reviews
// = "open", every open PR into a requested repo's first tier joins the
// list, though nobody asked for a review, an agent's in [bots] too; a
// GitHub App's does not.
func TestOpenFirstTierListsUnrequestedPRs(t *testing.T) {
	h := newHarness(t)
	etl := model.Repo{Owner: "acme", Name: "etl"}
	agent := pr(etl, 206, "opt-out flag for the digest", "agent-x", false)
	h.m.cfg.Bots.Logins = append(h.m.cfg.Bots.Logins, "agent-x")
	agent.BaseRefName = "dev"
	bump := pr(etl, 207, "bump the actions group", "dependabot", false)
	bump.BaseRefName = "dev"
	bump.Author.Type = "Bot"
	h.gh.OpenInto = []*model.PR{agent, bump}
	h.m.requested = []model.Repo{etl}
	h.m.cfg.Reviews.Requested = []model.Repo{etl}
	pc := config.DefaultPromote()
	pc.Repos, pc.FirstTierReviews = []model.Repo{etl}, "open"
	h.m.cfg.Promote = pc
	h.m.multi = true
	h.load()
	s := h.screen()
	if !strings.Contains(s, "etl#206") {
		t.Errorf("an unrequested first-tier PR should list:\n%s", s)
	}
	if strings.Contains(s, "etl#207") {
		t.Errorf("a bot's PR should not list:\n%s", s)
	}
	if n := h.gh.Reads["open_into"]; n != 1 {
		t.Errorf("open_into reads %d", n)
	}
	pc.FirstTierReviews = "requested"
	h.key('r')
	if s := h.screen(); strings.Contains(s, "etl#206") {
		t.Errorf("first_tier_reviews = requested lists only requested PRs:\n%s", s)
	}
}
