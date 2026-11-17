package model

import (
	"fmt"
	"strings"
)

// BranchRules is what a base branch asks of a PR before it merges, as
// far as GitHub's rules for the branch say: the required checks, the
// approvals, a code owner's review. Known is false when the rules could
// not be read, and then nothing is concluded from them.
type BranchRules struct {
	Known          bool
	RequiredChecks []string
	Approvals      int
	CodeOwner      bool
}

// BlockedWhy says what stands between a blocked PR and its merge, from
// the branch's rules and the PR's own checks and reviews: the first
// required check that failed, is still running or never reported, else
// the review the rules ask for and the PR lacks. "" when the rules are
// unknown or nothing they ask for is missing (the block is something
// else: a deployment, a signature, a conversation).
func BlockedWhy(pr *PR, rules BranchRules) string {
	if pr == nil || !rules.Known {
		return ""
	}
	var ro *Rollup
	if c := pr.Commits.Last(); c != nil {
		ro = c.Rollup
	}
	state := map[string]string{} // check name -> failed | running | passed
	// a check is called missing only when the whole list of checks was
	// read: with no rollup at all (a token that can't see it, a query
	// that didn't ask) or a list past its page, absence proves nothing
	whole := false
	if ro != nil {
		whole = ro.Contexts.TotalCount <= len(ro.Contexts.Nodes)
		for _, c := range ro.Contexts.Nodes {
			name, st := c.Name, ""
			if c.Typename == "StatusContext" || name == "" {
				name = c.Context
				switch c.State {
				case "SUCCESS":
					st = "passed"
				case "PENDING", "EXPECTED":
					st = "running"
				default:
					st = "failed"
				}
			} else {
				switch {
				case c.Status != "" && c.Status != "COMPLETED":
					st = "running"
				case c.Conclusion == "SUCCESS" || c.Conclusion == "NEUTRAL" || c.Conclusion == "SKIPPED":
					st = "passed"
				default:
					st = "failed"
				}
			}
			// two runs of one name (two workflows, two events, a re-run)
			// that disagree: which one the rule reads is not knowable
			// from here, so that check is left out of every claim
			switch prev := state[name]; {
			case prev == "":
				state[name] = st
			case prev != st:
				state[name] = "mixed"
			}
		}
	}
	var failed, running, missing []string
	for _, name := range rules.RequiredChecks {
		switch state[name] {
		case "failed":
			failed = append(failed, name)
		case "running":
			running = append(running, name)
		case "":
			// past the page of checks that was read, absence proves nothing
			if whole {
				missing = append(missing, name)
			}
		}
	}
	quote := func(names []string) string {
		for i, n := range names {
			names[i] = `"` + n + `"`
		}
		if len(names) > 2 {
			return fmt.Sprintf("%s and %d more", strings.Join(names[:2], ", "), len(names)-2)
		}
		return strings.Join(names, " and ")
	}
	check := func(names []string) string {
		if len(names) == 1 {
			return "required check " + quote(names)
		}
		return "required checks " + quote(names)
	}
	switch {
	case len(failed) > 0:
		return check(failed) + " failed"
	case len(running) > 0:
		if len(running) == 1 {
			return check(running) + " is still running"
		}
		return check(running) + " are still running"
	case len(missing) > 0:
		if len(missing) == 1 {
			return check(missing) + " hasn't reported"
		}
		return check(missing) + " haven't reported"
	}
	// the reviews, by GitHub's own verdict where it gives one: once it
	// says approved, no review is the block, whatever else is
	approved, changes := false, ""
	for _, r := range pr.Opinions.Nodes {
		switch r.State {
		case "APPROVED":
			approved = true
		case "CHANGES_REQUESTED":
			if changes == "" {
				changes = r.Author.Login
			}
		}
	}
	switch pr.ReviewDecision {
	case "APPROVED":
		return ""
	case "CHANGES_REQUESTED":
		if changes != "" {
			return "changes requested by " + changes
		}
		return "changes requested"
	case "REVIEW_REQUIRED":
		switch {
		case rules.Approvals > 1:
			return fmt.Sprintf("needs %d approvals", rules.Approvals)
		case rules.CodeOwner && rules.Approvals == 0:
			return "needs a code owner's review"
		default:
			return "needs an approval"
		}
	}
	// no verdict: a branch that asks for no approving review reports
	// none, even when it asks for a code owner's. Then a review is named
	// only when nobody approved and every required check is known to be
	// in, so a check can't be the block instead.
	switch {
	case changes != "":
		return "changes requested by " + changes
	case rules.CodeOwner && !approved && (whole || len(rules.RequiredChecks) == 0):
		return "needs a code owner's review"
	}
	return ""
}
