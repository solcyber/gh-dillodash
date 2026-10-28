package model

import (
	"reflect"
	"strings"
	"testing"
)

func checkRun(name, status, conclusion string) CheckContext {
	return CheckContext{Typename: "CheckRun", Name: name, Status: status, Conclusion: conclusion}
}

func statusCtx(context, state string) CheckContext {
	return CheckContext{Typename: "StatusContext", Context: context, State: state}
}

func prWithChecks(nodes ...CheckContext) *PR {
	r := &Rollup{State: "x"}
	r.Contexts.Nodes = nodes
	r.Contexts.TotalCount = len(nodes)
	pr := &PR{Number: 3550}
	pr.Commits.Nodes = []commitNode{{Commit: Commit{Rollup: r}}}
	return pr
}

func TestCIGlyph(t *testing.T) {
	cases := []struct {
		status, conclusion string
		want               byte
	}{
		{"COMPLETED", "SUCCESS", '+'},
		{"completed", "success", '+'},
		{"COMPLETED", "FAILURE", 'x'},
		{"COMPLETED", "CANCELLED", 'x'},
		{"COMPLETED", "TIMED_OUT", 'x'},
		{"COMPLETED", "NEUTRAL", '-'},
		{"COMPLETED", "SKIPPED", '-'},
		{"IN_PROGRESS", "", '~'},
		{"in_progress", "", '~'},
		{"QUEUED", "", '.'},
		{"WAITING", "", '.'},
		{"PENDING", "", '.'},
		{"", "SUCCESS", '+'},
		{"", "", 'x'},
	}
	for _, c := range cases {
		if got := CIGlyph(c.status, c.conclusion); got != c.want {
			t.Errorf("CIGlyph(%q,%q) = %c, want %c", c.status, c.conclusion, got, c.want)
		}
	}
}

func TestFinishCluster(t *testing.T) {
	if c := FinishCluster("", nil); c.Glyphs != "" || c.Tone != CIToneNormal || len(c.Failed) != 0 {
		t.Errorf("empty: %+v", c)
	}
	if c := FinishCluster("++", nil); c.Glyphs != "++" || c.Tone != CIToneBold {
		t.Errorf("all pass should be bold: %+v", c)
	}
	if c := FinishCluster("+x+~", []string{"preflight", "plan"}); c.Glyphs != "+x+~" || c.Tone != CIToneNormal ||
		!reflect.DeepEqual(c.Failed, []string{"preflight", "plan"}) {
		t.Errorf("failing cluster: %+v", c)
	}
	if c := FinishCluster("+.~", nil); c.Tone != CIToneDim {
		t.Errorf("in flight should be dim: %+v", c)
	}
	if c := FinishCluster("+-", nil); c.Tone != CIToneDim {
		t.Errorf("skipped is not all-pass: %+v", c)
	}
	// many checks compress to counts in the order + x . ~ -
	many := strings.Repeat("+", 9) + "x" + ".." + "~" + "-"
	if c := FinishCluster(many, []string{"a"}); c.Glyphs != "9+1x2.1~1-" {
		t.Errorf("compressed: %q", c.Glyphs)
	}
	if c := FinishCluster(strings.Repeat("+", 13), nil); c.Glyphs != "13+" || c.Tone != CIToneBold {
		t.Errorf("13 passes: %+v", c)
	}
	// exactly twelve stays verbatim
	if c := FinishCluster(strings.Repeat("+", 12), nil); c.Glyphs != strings.Repeat("+", 12) {
		t.Errorf("12 passes: %q", c.Glyphs)
	}
	// the compressed form is itself clipped to 12
	huge := strings.Repeat("+", 1000) + strings.Repeat("x", 1000) + strings.Repeat(".", 1000) +
		strings.Repeat("~", 1000) + strings.Repeat("-", 1000)
	if c := FinishCluster(huge, nil); len(c.Glyphs) != 12 || !strings.HasPrefix(c.Glyphs, "1000+1000x") {
		t.Errorf("clipped: %q", c.Glyphs)
	}
}

func TestClusterCI(t *testing.T) {
	// no commits at all, or no rollup: not visible (the fallback decides)
	if _, ok := ClusterCI(&PR{}); ok {
		t.Error("no commits should not be visible")
	}
	pr := &PR{}
	pr.Commits.Nodes = []commitNode{{}}
	if _, ok := ClusterCI(pr); ok {
		t.Error("nil rollup should not be visible")
	}
	// a hidden context (no __typename) poisons the whole cluster
	if _, ok := ClusterCI(prWithChecks(checkRun("plan", "COMPLETED", "SUCCESS"), CheckContext{})); ok {
		t.Error("hidden context should not be visible")
	}
	// an empty rollup is a visible, empty cluster
	c, ok := ClusterCI(prWithChecks())
	if !ok || c.Glyphs != "" || len(c.Failed) != 0 {
		t.Errorf("empty rollup: %+v %v", c, ok)
	}
	c, ok = ClusterCI(prWithChecks(
		checkRun("preflight", "COMPLETED", "SUCCESS"),
		checkRun("plan", "COMPLETED", "FAILURE"),
		checkRun("", "COMPLETED", "FAILURE"),
		checkRun("lint", "IN_PROGRESS", ""),
		statusCtx("terraform/apply-gate", "PENDING"),
		statusCtx("terraform/other", "EXPECTED"),
		statusCtx("deploy", "SUCCESS"),
		statusCtx("security", "ERROR"),
		statusCtx("", "FAILURE"),
	))
	if !ok {
		t.Fatal("should be visible")
	}
	if c.Glyphs != "+xx~..+xx" {
		t.Errorf("glyphs %q", c.Glyphs)
	}
	if !reflect.DeepEqual(c.Failed, []string{"plan", "?", "security", "?"}) {
		t.Errorf("failed %v", c.Failed)
	}
	if c.Tone != CIToneNormal {
		t.Errorf("tone %v", c.Tone)
	}
	// the parsed shape from JSON
	pr = &PR{}
	pr.Commits.Nodes = []commitNode{{Commit: Commit{Rollup: &Rollup{}}}}
	pr.Commits.Nodes[0].Commit.Rollup.Contexts.Nodes = []CheckContext{
		checkRun("a", "COMPLETED", "SUCCESS"), checkRun("b", "COMPLETED", "SUCCESS")}
	c, ok = ClusterCI(pr)
	if !ok || c.Glyphs != "++" || c.Tone != CIToneBold {
		t.Errorf("all green: %+v", c)
	}
}

func TestDecisionLabel(t *testing.T) {
	me := "alice"
	mk := func(decision string, reviews ...Review) *PR {
		pr := &PR{Number: 3543, ReviewDecision: decision}
		pr.Opinions.Nodes = reviews
		return pr
	}
	approvedBy := func(login string) Review {
		return Review{Author: Actor{Login: login}, State: "APPROVED"}
	}
	if got := DecisionLabel(&PR{IsDraft: true, ReviewDecision: "APPROVED"}, me, true); got != "DRAFT" {
		t.Errorf("draft wins: %q", got)
	}
	if got := DecisionLabel(mk("REVIEW_REQUIRED"), me, true); got != "approved by me" {
		t.Errorf("local approval: %q", got)
	}
	if got := DecisionLabel(mk("APPROVED", approvedBy("Alice")), me, false); got != "approved by me" {
		t.Errorf("my review (case-insensitive) beats the decision: %q", got)
	}
	if got := DecisionLabel(mk("APPROVED", approvedBy("bob")), me, false); got != "approved" {
		t.Errorf("someone else's approval: %q", got)
	}
	if got := DecisionLabel(mk("APPROVED", approvedBy("alice")), "", false); got != "approved" {
		t.Errorf("no viewer: %q", got)
	}
	if got := DecisionLabel(mk("CHANGES_REQUESTED"), me, false); got != "changes requested" {
		t.Errorf("changes requested: %q", got)
	}
	if got := DecisionLabel(mk("REVIEW_REQUIRED"), me, false); got != "awaiting review" {
		t.Errorf("review required: %q", got)
	}
	if got := DecisionLabel(mk(""), me, false); got != "awaiting review" {
		t.Errorf("null decision: %q", got)
	}
	if got := DecisionLabel(mk("CHANGES_REQUESTED", Review{Author: Actor{Login: "alice"}, State: "CHANGES_REQUESTED"}), me, false); got != "changes requested" {
		t.Errorf("my changes-requested is not an approval: %q", got)
	}
	for _, l := range []string{LabelAwaiting, LabelApprovedMe, LabelApproved, LabelChanges, LabelDraft, LabelMerged} {
		if ChipForLabel[l] == "" {
			t.Errorf("no chip for %q", l)
		}
	}
	if ChipForLabel["approved by me"] != "chip.mine" || ChipForLabel["DRAFT"] != "chip.draft" {
		t.Errorf("chip styles: %v", ChipForLabel)
	}
}

func TestIdentCarriesOwner(t *testing.T) {
	a := &PR{Repo: Repo{Owner: "acme", Name: "api"}, Number: 5}
	b := &PR{Repo: Repo{Owner: "beta", Name: "api"}, Number: 5}
	if a.Ident() == b.Ident() {
		t.Fatal("same name under two owners must be two idents")
	}
	seen := map[Ident]bool{a.Ident(): true}
	if seen[b.Ident()] {
		t.Fatal("map keyed by Ident conflated two owners")
	}
	if got := a.Ident().String(); got != "acme/api#5" {
		t.Errorf("String() = %q", got)
	}
	i := &Issue{Repo: a.Repo, Number: 5}
	if i.Ident() != a.Ident() {
		t.Error("a PR and an issue with the same number share the row identity")
	}
}
