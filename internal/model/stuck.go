package model

import (
	"fmt"
	"strings"
)

// StuckKind names why an approved PR will not turn into a merge by itself.
type StuckKind string

const (
	StuckNone StuckKind = ""
	// StuckStale: the newest approval is on an older commit; the head
	// moved after it and the approver has not seen the change.
	StuckStale StuckKind = "stale"
	// StuckDismissed: my approval was dismissed and I have not
	// approved again.
	StuckDismissed StuckKind = "dismissed"
	// StuckConflicts: approved, but the branch conflicts with its base.
	StuckConflicts StuckKind = "conflicts"
	// StuckRed: approved, merging is blocked and a check failed on the head.
	StuckRed StuckKind = "red"
	// StuckBlocked: approved, but merging is blocked by something other
	// than review (a required check that never reported, a rule).
	StuckBlocked StuckKind = "blocked"
)

// Stuck is an approved PR's reason to be looked at again.
type Stuck struct {
	Kind StuckKind
	// Reason is the one-line text a row or the detail shows.
	Reason string
	// Mine: the approval at issue is the viewer's own (a re-review is
	// theirs to give).
	Mine bool
	// ApprovedSHA is a stale approval's commit (for a compare view).
	ApprovedSHA string
	// HeadSHA is the PR's head at the time.
	HeadSHA string
}

// AuthorAction reports a stuck kind only the PR's author can clear: the
// approver can't resolve a conflict or fix a red check.
func (s Stuck) AuthorAction() bool {
	switch s.Kind {
	case StuckConflicts, StuckRed, StuckBlocked:
		return true
	}
	return false
}

// StuckOf classifies pr. listed, when not nil, is the same PR as the list
// query read it: it fills the fields the detail query leaves out (the
// check rollup, the merge state). Drafts and closed PRs are never stuck.
//
// A stale or dismissed approval of mine comes first (it is mine to act
// on); then a stale approval by someone else, a conflict, a failed
// check while blocked, and a block. Only the stale and dismissed cases
// read reviews; a conflict reads mergeable or the merge state, so none
// of them depend on a readable check rollup except StuckRed.
func StuckOf(pr, listed *PR, me string) Stuck {
	if pr == nil || pr.IsDraft || (pr.State != "" && pr.State != "OPEN") {
		return Stuck{}
	}
	f := stuckFields(pr, listed)
	stale := func(sha string, mine bool) Stuck {
		reason := fmt.Sprintf("stale: approved %s, head %s", ShortSHA(sha), ShortSHA(f.head))
		if f.ahead > 0 {
			reason += fmt.Sprintf(" (+%d)", f.ahead)
		}
		return Stuck{Kind: StuckStale, Reason: reason, Mine: mine, ApprovedSHA: sha, HeadSHA: f.head}
	}

	// my own review first
	var mineOp *Review
	for i := range f.opinions {
		if me != "" && strings.EqualFold(f.opinions[i].Author.Login, me) {
			mineOp = &f.opinions[i]
			break
		}
	}
	if mineOp != nil && mineOp.State == "APPROVED" && ShaMoved(mineOp.CommitOid(), f.head) {
		return stale(mineOp.CommitOid(), true)
	}
	if mineOp == nil && me != "" {
		for _, r := range f.latest {
			if strings.EqualFold(r.Author.Login, me) && r.State == "DISMISSED" {
				return Stuck{Kind: StuckDismissed, Reason: "your approval was dismissed", Mine: true, HeadSHA: f.head}
			}
		}
	}

	// is it approved at all? reviewDecision is null on a repo that asks
	// for no approvals, so an approving opinion stands in for it
	approved := pr.ReviewDecision == "APPROVED"
	var newest *Review
	onHead := false
	for i := range f.opinions {
		r := &f.opinions[i]
		if r.State != "APPROVED" {
			continue
		}
		if pr.ReviewDecision == "" {
			approved = true
		}
		if !ShaMoved(r.CommitOid(), f.head) {
			onHead = true
		}
		if newest == nil || r.SubmittedAt.After(newest.SubmittedAt) {
			newest = r
		}
	}
	if !approved || pr.ReviewDecision == "CHANGES_REQUESTED" {
		return Stuck{}
	}
	if newest != nil && !onHead && ShaMoved(newest.CommitOid(), f.head) {
		return stale(newest.CommitOid(), false)
	}
	switch {
	case f.mergeable == "CONFLICTING" || f.mergeState == "DIRTY":
		return Stuck{Kind: StuckConflicts, Reason: "conflicts with base", HeadSHA: f.head}
	case f.mergeState == "BLOCKED" && f.red:
		return Stuck{Kind: StuckRed, Reason: "blocked: a check failed", HeadSHA: f.head}
	case f.mergeState == "BLOCKED":
		return Stuck{Kind: StuckBlocked, Reason: "blocked: a required check or rule has not passed", HeadSHA: f.head}
	}
	return Stuck{}
}

type stuckInput struct {
	head, mergeable, mergeState string
	opinions, latest            []Review
	red                         bool
	ahead                       int
}

// stuckFields reads pr, falling back to listed for what pr lacks.
func stuckFields(pr, listed *PR) stuckInput {
	in := stuckInput{
		head: pr.HeadRefOid, mergeable: pr.Mergeable, mergeState: pr.MergeStateStatus,
		opinions: pr.Opinions.Nodes, latest: pr.LatestReviews.Nodes, ahead: pr.ApprovalAhead,
	}
	rollupOf := func(p *PR) *Rollup {
		if c := p.Commits.Last(); c != nil {
			return c.Rollup
		}
		return nil
	}
	ro := rollupOf(pr)
	if listed != nil && listed != pr {
		same := !ShaMoved(listed.HeadRefOid, pr.HeadRefOid)
		if in.head == "" {
			in.head = listed.HeadRefOid
		}
		if same {
			if in.mergeable == "" {
				in.mergeable = listed.Mergeable
			}
			if in.mergeState == "" {
				in.mergeState = listed.MergeStateStatus
			}
			if len(in.latest) == 0 {
				in.latest = listed.LatestReviews.Nodes
			}
			if in.ahead == 0 {
				in.ahead = listed.ApprovalAhead
			}
			if ro == nil {
				ro = rollupOf(listed)
			}
		}
		if len(in.opinions) == 0 {
			in.opinions = listed.Opinions.Nodes
		}
	}
	if ro != nil {
		in.red = ro.State == "FAILURE" || ro.State == "ERROR"
	}
	return in
}
