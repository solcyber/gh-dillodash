package ui

import (
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// openIssue opens acme/infra#7 (the fixture's issue) with ':'.
func (h *harness) openIssue(ref string) {
	h.t.Helper()
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return ref, true }
	h.key(':')
	if h.m.view != ViewDetail || h.m.detailKind != "issue" {
		h.t.Fatalf("issue %s not open: view=%s kind=%s err=%q status=%q", ref, h.m.view, h.m.detailKind, h.m.errText, h.m.status)
	}
}

// pickLabel answers the next menu with the entry that ends in name.
func pickEnding(t *testing.T, name string, seen *[]string) func(string, []string) int {
	return func(_ string, labels []string) int {
		*seen = labels
		for i, l := range labels {
			if strings.HasSuffix(strings.TrimSpace(l), name) {
				return i
			}
		}
		t.Fatalf("no menu entry ends in %q: %q", name, labels)
		return -1
	}
}

func detailLabels(h *harness) []string {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return h.m.detail.(*model.Issue).Labels.Names()
}

// TestTriageLabels (L): the menu marks the labels the item carries;
// picking an unmarked one adds it, a marked one removes it.
func TestTriageLabels(t *testing.T) {
	h := newHarness(t)
	h.gh.LabelDefs = map[model.Repo][]model.Label{infra: {{Name: "ops"}, {Name: "bug"}}}
	h.load()
	h.openIssue("7")
	var seen []string
	h.m.tp.Pick = pickEnding(t, "bug", &seen)
	h.key('L')
	if len(seen) != 2 || seen[0] != "[x] ops" || seen[1] != "[ ] bug" {
		t.Errorf("menu = %q", seen)
	}
	if c := h.gh.CallsFor("add_labels"); len(c) != 1 || c[0].Number != 7 || strings.Join(c[0].Names, ",") != "bug" {
		t.Fatalf("add_labels = %+v (status %q)", c, h.m.status)
	}
	if got := strings.Join(detailLabels(h), ","); got != "ops,bug" {
		t.Errorf("detail labels = %s", got)
	}
	h.m.tp.Pick = pickEnding(t, "ops", &seen)
	h.key('L')
	if c := h.gh.CallsFor("remove_label"); len(c) != 1 || c[0].Names[0] != "ops" {
		t.Fatalf("remove_label = %+v (status %q)", c, h.m.status)
	}
	if got := strings.Join(detailLabels(h), ","); got != "bug" {
		t.Errorf("detail labels after removal = %s", got)
	}
	h.m.tp.Pick = func(string, []string) int { return -1 }
	h.key('L')
	if len(h.gh.CallsFor("add_labels"))+len(h.gh.CallsFor("remove_label")) != 2 || !strings.Contains(h.m.status, "label cancelled") {
		t.Errorf("cancel posts nothing: status %q", h.m.status)
	}
}

// TestTriageAssign (s): assign someone, then unassign them; a PR's s
// does nothing.
func TestTriageAssign(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.openIssue("7")
	var seen []string
	h.m.tp.Pick = pickEnding(t, "Bob", &seen)
	h.key('s')
	if c := h.gh.CallsFor("add_assignees"); len(c) != 1 || c[0].Names[0] != "bob" {
		t.Fatalf("add_assignees = %+v (status %q)", c, h.m.status)
	}
	h.m.tp.Pick = func(_ string, labels []string) int {
		seen = labels
		return 0 // the current assignee comes first, marked
	}
	h.key('s')
	if !strings.HasPrefix(seen[0], "[x] @bob") {
		t.Errorf("the assignee is listed first and marked: %q", seen)
	}
	if c := h.gh.CallsFor("remove_assignees"); len(c) != 1 || c[0].Names[0] != "bob" {
		t.Fatalf("remove_assignees = %+v (status %q)", c, h.m.status)
	}

	h.key('q')
	h.named("enter") // the PR
	h.key('s')
	h.key('Z')
	if n := len(h.gh.Calls); n != 2 {
		t.Errorf("s and Z on a PR write nothing: %+v", h.gh.Calls)
	}
}

// TestTriageCloseReopen (Z): close as not planned, then reopen.
func TestTriageCloseReopen(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.openIssue("7")
	h.m.tp.Pick = func(string, []string) int { return 1 }
	h.key('Z')
	c := h.gh.CallsFor("set_state")
	if len(c) != 1 || c[0].State != "closed" || c[0].Reason != "not_planned" {
		t.Fatalf("close = %+v (status %q)", c, h.m.status)
	}
	if !strings.Contains(h.m.status, "closed") || !strings.Contains(h.m.status, "not planned") {
		t.Errorf("status %q", h.m.status)
	}
	h.m.tp.Confirm = func(q string) bool { return strings.Contains(q, "reopen") }
	h.key('Z')
	c = h.gh.CallsFor("set_state")
	if len(c) != 2 || c[1].State != "open" || c[1].Reason != "reopened" {
		t.Fatalf("reopen = %+v (status %q)", c, h.m.status)
	}
}

// TestTriageHandoff (H): with [agent] handoff_label set, H adds the
// label and posts handoff_comment after a confirm; a second H says the
// agent has it; without a label H says how to set one.
func TestTriageHandoff(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.openIssue("7")
	h.key('H')
	if len(h.gh.Calls) != 0 || !strings.Contains(h.m.status, "handoff_label") {
		t.Fatalf("no agent: nothing written, the status says how: %q", h.m.status)
	}
	h.m.cfg.Agent.HandoffLabel = "agent"
	h.m.cfg.Agent.HandoffComment = "over to the agent"
	if !strings.Contains(h.screen(), "H  to agent") {
		t.Errorf("the bar shows H once a label is set:\n%s", h.screen())
	}
	var asked string
	h.m.tp.Confirm = func(q string) bool { asked = q; return true }
	h.key('H')
	if !strings.Contains(asked, `"agent"`) || !strings.Contains(asked, "handoff comment") {
		t.Errorf("confirm = %q", asked)
	}
	if c := h.gh.CallsFor("add_labels"); len(c) != 1 || c[0].Names[0] != "agent" {
		t.Fatalf("add_labels = %+v (status %q)", c, h.m.status)
	}
	if len(h.gh.CommentsPosted) != 1 || h.gh.CommentsPosted[0].Body != "over to the agent" {
		t.Fatalf("handoff comment = %+v", h.gh.CommentsPosted)
	}
	h.key('H')
	if len(h.gh.Calls) != 2 || !strings.Contains(h.m.status, "the agent has it") {
		t.Errorf("a second H writes nothing: %q", h.m.status)
	}
}

// TestAllowedReposRefusesWrites: with [github] allowed_repos, every
// write key on a repo outside it is refused before any prompt, also on
// an item opened with ':'; reads still work, and the allowed repo writes.
func TestAllowedReposRefusesWrites(t *testing.T) {
	other := model.Repo{Owner: "acme", Name: "other"}
	h := newHarness(t, infra, other)
	h.gh.LabelDefs = map[model.Repo][]model.Label{infra: {{Name: "bug"}}}
	h.load()
	h.m.cfg.GitHub.AllowedRepos = []model.Repo{other}
	h.m.cfg.Agent.HandoffLabel = "agent"
	asked := 0
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { asked++; return "x", true }
	h.m.tp.Pick = func(string, []string) int { asked++; return 0 }
	h.m.tp.Confirm = func(string) bool { asked++; return true }

	h.openIssue("infra#7") // listed, but not allowed
	asked = 0
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { asked++; return "x", true }
	for _, k := range []rune{'c', 't', 'L', 's', 'Z', 'H'} {
		h.key(k)
		if !strings.Contains(h.m.status, "allowed_repos") {
			t.Errorf("%c: status %q", k, h.m.status)
		}
	}
	if asked != 0 || len(h.gh.Calls) != 0 || h.gh.Reads["repo_labels"] != 0 {
		t.Errorf("refused before asking or reading: asked=%d calls=%+v", asked, h.gh.Calls)
	}

	h.key('q')
	h.named("enter") // the first row: infra#101, a PR
	for _, k := range []rune{'a', 'M'} {
		h.key(k)
		if !strings.Contains(h.m.status, "allowed_repos") {
			t.Errorf("%c on a PR: status %q", k, h.m.status)
		}
	}
	if len(h.gh.Calls) != 0 {
		t.Errorf("no write on the PR: %+v", h.gh.Calls)
	}

	h.key('q')
	h.openIssue("other#7") // the allowed repo, reached with ':'
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "hello", true }
	h.key('c')
	if len(h.gh.CommentsPosted) != 1 || h.gh.CommentsPosted[0].Repo != other {
		t.Errorf("a comment on the allowed repo posts: %+v (status %q)", h.gh.CommentsPosted, h.m.status)
	}
}

// TestProfileBadge: [display] profile is drawn in the header of the list
// and of a detail.
func TestProfileBadge(t *testing.T) {
	h := newHarness(t)
	h.m.cfg.Display.Profile = "OPS"
	h.load()
	if !strings.Contains(h.screen(), " OPS ") {
		t.Errorf("list header lacks the badge:\n%s", h.screen())
	}
	h.named("enter")
	if first := strings.SplitN(h.screen(), "\n", 2)[0]; !strings.Contains(first, " OPS ") {
		t.Errorf("detail header lacks the badge: %q", first)
	}
}
