package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// TestAgentSection: with [agent] section on, the agent's PRs and the
// items carrying the handoff label sit under its name; its PRs still
// count toward "need you"; it is named in the rows and in H's question.
func TestAgentSection(t *testing.T) {
	h := newHarness(t)
	h.m.cfg.Agent.Name, h.m.cfg.Agent.Login, h.m.cfg.Agent.HandoffLabel, h.m.cfg.Agent.Section = "Ada Ops", "infra-bot", "ada", true
	h.gh.Issues[model.Ident{Repo: infra, Number: 7}].Labels.Nodes = append(h.gh.Issues[model.Ident{Repo: infra, Number: 7}].Labels.Nodes, model.Label{Name: "ada"})
	h.load()
	s := h.screen()
	if !strings.Contains(s, "with Ada Ops") {
		t.Fatalf("no agent section:\n%s", s)
	}
	h.m.mu.Lock()
	var inSection []string
	on := false
	for _, r := range h.m.rows {
		if r.Kind == "header" {
			on = strings.HasPrefix(r.Item.(string), "with Ada Ops")
			continue
		}
		if on {
			inSection = append(inSection, fmt.Sprintf("%s#%d", r.Ident().Repo.Name, r.Ident().Number))
		}
	}
	h.m.mu.Unlock()
	if strings.Join(inSection, " ") != "infra#102 infra#7" {
		t.Errorf("the section holds the agent's PR and the handed issue: %v", inSection)
	}
	if !strings.Contains(s, " Ada ") {
		t.Errorf("the agent's row shows its short name:\n%s", s)
	}
	h.openIssue("7")
	h.m.cfg.Agent.HandoffLabel = "ada-next" // so H has something to add
	var q string
	h.m.tp.Confirm = func(s string) bool { q = s; return false }
	h.key('H')
	if !strings.Contains(q, "to Ada Ops") {
		t.Errorf("H names the agent: %q", q)
	}
}

// TestMergeApplies: M in a merge_applies repo says so first.
func TestMergeApplies(t *testing.T) {
	h := newHarness(t)
	d := h.gh.PRs[model.Ident{Repo: infra, Number: 101}]
	d.ReviewDecision, d.MergeStateStatus = "APPROVED", "CLEAN"
	h.m.cfg.Reviews.MergeApplies = []model.Repo{infra}
	h.load()
	h.named("enter")
	if !strings.Contains(h.screen(), "merge applies") {
		t.Errorf("the header lacks the chip:\n%s", h.screen())
	}
	var q string
	h.m.tp.Confirm = func(s string) bool { q = s; return false }
	h.key('M')
	if !strings.HasPrefix(q, "MERGING APPLIES: acme/infra deploys on push to main") {
		t.Errorf("M's question: %q", q)
	}
}
