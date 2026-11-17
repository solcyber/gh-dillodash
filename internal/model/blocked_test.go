package model

import "testing"

func rollup(total int, cs ...CheckContext) *Rollup {
	r := &Rollup{}
	r.Contexts.Nodes = cs
	r.Contexts.TotalCount = total
	if total == 0 {
		r.Contexts.TotalCount = len(cs)
	}
	return r
}

func run(name, status, conclusion string) CheckContext {
	return CheckContext{Typename: "CheckRun", Name: name, Status: status, Conclusion: conclusion}
}

// TestBlockedWhy: the first required check that failed, runs or never
// reported is named; then the review the rules ask for; nothing is
// concluded from rules or checks that were not read.
func TestBlockedWhy(t *testing.T) {
	rules := BranchRules{Known: true, RequiredChecks: []string{"build", "e2e", "gate"}}
	approved := []Review{{State: "APPROVED"}}
	for name, tc := range map[string]struct {
		rules    BranchRules
		rollup   *Rollup
		opinions []Review
		want     string
	}{
		"failed wins":    {rules, rollup(0, run("build", "COMPLETED", "SUCCESS"), run("e2e", "COMPLETED", "FAILURE"), run("gate", "IN_PROGRESS", "")), nil, `required check "e2e" failed`},
		"running":        {rules, rollup(0, run("build", "COMPLETED", "SUCCESS"), run("e2e", "COMPLETED", "SKIPPED"), run("gate", "QUEUED", "")), nil, `required check "gate" is still running`},
		"not reported":   {rules, rollup(0, run("build", "COMPLETED", "SUCCESS")), nil, `required checks "e2e" and "gate" haven't reported`},
		"status":         {BranchRules{Known: true, RequiredChecks: []string{"apply-gate"}}, rollup(0, CheckContext{Typename: "StatusContext", Context: "apply-gate", State: "PENDING"}), nil, `required check "apply-gate" is still running`},
		"rerun failed":   {BranchRules{Known: true, RequiredChecks: []string{"build"}}, rollup(0, run("build", "COMPLETED", "SUCCESS"), run("build", "COMPLETED", "FAILURE")), nil, `required check "build" failed`},
		"past the page":  {rules, rollup(80, run("build", "COMPLETED", "SUCCESS")), nil, ""},
		"no rollup":      {rules, nil, nil, ""},
		"unknown rules":  {BranchRules{RequiredChecks: []string{"build"}}, rollup(0), nil, ""},
		"an approval":    {BranchRules{Known: true, Approvals: 1}, rollup(0), nil, "needs an approval"},
		"two approvals":  {BranchRules{Known: true, Approvals: 2}, rollup(0), approved, "needs 2 approvals, has 1"},
		"code owner":     {BranchRules{Known: true, CodeOwner: true}, rollup(0), nil, "needs a code owner's review"},
		"wrong owner":    {BranchRules{Known: true, CodeOwner: true}, rollup(0), approved, "needs a review from a code owner of the changed files"},
		"owner or check": {BranchRules{Known: true, CodeOwner: true, RequiredChecks: []string{"build"}}, nil, approved, ""},
		"nothing asked":  {BranchRules{Known: true}, rollup(0), nil, ""},
		"all green":      {BranchRules{Known: true, RequiredChecks: []string{"build"}, Approvals: 1}, rollup(0, run("build", "COMPLETED", "SUCCESS")), approved, ""},
	} {
		pr := &PR{}
		pr.Opinions.Nodes = tc.opinions
		pr.Commits = NewCommits(Commit{Rollup: tc.rollup})
		if got := BlockedWhy(pr, tc.rules); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	// commits not read at all
	if got := BlockedWhy(&PR{}, rules); got != "" {
		t.Errorf("no commits read: %q", got)
	}
	// the stuck chip carries the reason
	pr := &PR{ReviewDecision: "APPROVED", MergeStateStatus: "BLOCKED", BlockedWhy: `required check "gate" hasn't reported`}
	pr.Opinions.Nodes = approved
	if s := StuckOf(pr, nil, "alice"); s.Kind != StuckBlocked || s.Reason != `blocked: required check "gate" hasn't reported` {
		t.Errorf("stuck: %+v", s)
	}
}
