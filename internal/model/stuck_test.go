package model

import (
	"strings"
	"testing"
	"time"
)

const (
	headSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	oldSHA  = "1111111111111111111111111111111111111111"
)

func approval(who, sha string, at time.Time) Review {
	r := Review{Author: Actor{Login: who}, State: "APPROVED", SubmittedAt: at}
	if sha != "" {
		r.Commit = &ReviewCommit{Oid: sha}
	}
	return r
}

func stuckPR(decision string, reviews ...Review) *PR {
	p := &PR{Number: 1, State: "OPEN", HeadRefOid: headSHA, ReviewDecision: decision}
	p.Opinions.Nodes = reviews
	return p
}

func withRollup(p *PR, state string) *PR {
	p.Commits.Nodes = []commitNode{{Commit: Commit{Rollup: &Rollup{State: state}}}}
	return p
}

func TestStuckOf(t *testing.T) {
	t1 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	cases := []struct {
		name   string
		pr     *PR
		kind   StuckKind
		mine   bool
		reason string
	}{
		{"approved on the head", stuckPR("APPROVED", approval("bob", headSHA, t1)), StuckNone, false, ""},
		{"my approval of an older commit", stuckPR("APPROVED", approval("alice", oldSHA, t1)), StuckStale, true, "stale: approved 1111111, head eeeeeee"},
		{"someone's approval of an older commit", stuckPR("APPROVED", approval("bob", oldSHA, t1)), StuckStale, false, "stale: approved 1111111"},
		{"an older stale one beside a head approval", stuckPR("APPROVED", approval("bob", oldSHA, t1), approval("carol", headSHA, t2)), StuckNone, false, ""},
		{"a review commit the query did not read", stuckPR("APPROVED", approval("alice", "", t1)), StuckNone, false, ""},
		{"a short sha matches as a prefix", stuckPR("APPROVED", approval("bob", headSHA[:7], t1)), StuckNone, false, ""},
		{"conflicting", func() *PR {
			p := stuckPR("APPROVED", approval("bob", headSHA, t1))
			p.Mergeable = "CONFLICTING"
			return p
		}(), StuckConflicts, false, "conflicts with base"},
		{"dirty", func() *PR {
			p := stuckPR("APPROVED", approval("bob", headSHA, t1))
			p.MergeStateStatus = "DIRTY"
			return p
		}(), StuckConflicts, false, "conflicts"},
		{"mergeable not computed yet", func() *PR { p := stuckPR("APPROVED", approval("bob", headSHA, t1)); p.Mergeable = "UNKNOWN"; return p }(), StuckNone, false, ""},
		{"blocked and red", func() *PR {
			p := withRollup(stuckPR("APPROVED", approval("bob", headSHA, t1)), "FAILURE")
			p.MergeStateStatus = "BLOCKED"
			return p
		}(), StuckRed, false, "a check failed"},
		{"red but only unstable (no required check failed)", func() *PR {
			p := withRollup(stuckPR("APPROVED", approval("bob", headSHA, t1)), "FAILURE")
			p.MergeStateStatus = "UNSTABLE"
			return p
		}(), StuckNone, false, ""},
		{"blocked with no rollup", func() *PR {
			p := stuckPR("APPROVED", approval("bob", headSHA, t1))
			p.MergeStateStatus = "BLOCKED"
			return p
		}(), StuckBlocked, false, "required check or rule"},
		{"not approved is never stuck", func() *PR { p := stuckPR("REVIEW_REQUIRED"); p.MergeStateStatus = "DIRTY"; return p }(), StuckNone, false, ""},
		{"changes requested is never stuck", func() *PR {
			p := stuckPR("CHANGES_REQUESTED", approval("bob", oldSHA, t1))
			p.Mergeable = "CONFLICTING"
			return p
		}(), StuckNone, false, ""},
		{"no review decision: an approval stands in", func() *PR { p := stuckPR("", approval("bob", headSHA, t1)); p.Mergeable = "CONFLICTING"; return p }(), StuckConflicts, false, ""},
		{"a draft is never stuck", func() *PR { p := stuckPR("APPROVED", approval("alice", oldSHA, t1)); p.IsDraft = true; return p }(), StuckNone, false, ""},
		{"a merged PR is never stuck", func() *PR { p := stuckPR("APPROVED", approval("alice", oldSHA, t1)); p.State = "MERGED"; return p }(), StuckNone, false, ""},
		{"my approval dismissed", func() *PR {
			p := stuckPR("REVIEW_REQUIRED")
			p.LatestReviews.Nodes = []Review{{Author: Actor{Login: "Alice"}, State: "DISMISSED"}}
			return p
		}(), StuckDismissed, true, "dismissed"},
		{"someone else's dismissal is theirs", func() *PR {
			p := stuckPR("REVIEW_REQUIRED")
			p.LatestReviews.Nodes = []Review{{Author: Actor{Login: "bob"}, State: "DISMISSED"}}
			return p
		}(), StuckNone, false, ""},
		{"ahead count in the reason", func() *PR { p := stuckPR("APPROVED", approval("alice", oldSHA, t1)); p.ApprovalAhead = 2; return p }(), StuckStale, true, "(+2)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := StuckOf(c.pr, nil, "alice")
			if s.Kind != c.kind || s.Mine != c.mine || !strings.Contains(s.Reason, c.reason) {
				t.Errorf("got %+v, want kind %q mine %v reason ~%q", s, c.kind, c.mine, c.reason)
			}
		})
	}
}

// The detail query reads no rollup: the listed twin fills it, but only
// while both are the same head.
func TestStuckOfFillsFromTheListedTwin(t *testing.T) {
	t1 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	detail := stuckPR("APPROVED", approval("bob", headSHA, t1))
	listed := withRollup(stuckPR("APPROVED", approval("bob", headSHA, t1)), "FAILURE")
	listed.MergeStateStatus = "BLOCKED"
	if s := StuckOf(detail, listed, "alice"); s.Kind != StuckRed {
		t.Errorf("same head: %+v", s)
	}
	listed.HeadRefOid = oldSHA
	if s := StuckOf(detail, listed, "alice"); s.Kind != StuckNone {
		t.Errorf("listed twin on another head must not lend its state: %+v", s)
	}
}

func TestMyApprovalStands(t *testing.T) {
	t1 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if !stuckPR("APPROVED", approval("alice", headSHA, t1)).MyApprovalStands("alice") {
		t.Error("an approval of the head stands")
	}
	if stuckPR("APPROVED", approval("alice", oldSHA, t1)).MyApprovalStands("alice") {
		t.Error("an approval of an older commit is stale")
	}
	if !stuckPR("APPROVED", approval("ALICE", "", t1)).MyApprovalStands("alice") {
		t.Error("an unread review commit is taken to stand")
	}
	if stuckPR("APPROVED", approval("bob", headSHA, t1)).MyApprovalStands("alice") {
		t.Error("someone else's approval is not mine")
	}
	if stuckPR("APPROVED", approval("alice", headSHA, t1)).MyApprovalStands("") {
		t.Error("no viewer, no approval")
	}
}
