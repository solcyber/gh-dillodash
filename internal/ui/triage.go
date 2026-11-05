package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Issue triage from the detail view: labels (L), assignees (s), close
// and reopen (Z), and handing the item to the agent (H). Every one is a
// write, so each refuses early on a repo outside [github] allowed_repos;
// the client refuses it again underneath (ghapi.WithAllowedRepos).

// writeRefused says why a write to repo is refused, before any prompt:
// the repo is not in [github] allowed_repos. False means go on.
func (m *Model) writeRefused(app *lockedApp, repo model.Repo, what string) bool {
	return workflow.WriteRefused(app, repo, what)
}

// hasHandoff gates the H chip: [agent] handoff_label is set.
func (m *Model) hasHandoff() bool { return m.cfg != nil && m.cfg.Agent.HandoffLabel != "" }

// triageTarget is the open item: its repo, number, labels and whether it
// is an issue (assignees and state are read from issues only).
type triageTarget struct {
	repo    model.Repo
	number  int
	labels  []string
	issue   *model.Issue
	isIssue bool
}

func (m *Model) triageTarget(app *lockedApp) (triageTarget, bool) {
	switch v := app.Detail().(type) {
	case *model.PR:
		return triageTarget{repo: m.repoOf(v), number: v.Number, labels: v.Labels.Names()}, true
	case *model.Issue:
		return triageTarget{repo: m.repoOf(v), number: v.Number, labels: v.Labels.Names(), issue: v, isIssue: true}, true
	}
	return triageTarget{}, false
}

// setDetailLabels replaces the open item's labels in place (the next
// refresh brings GitHub's word) and redraws it.
func (m *Model) setDetailLabels(app *lockedApp, names []string) {
	nodes := make([]model.Label, len(names))
	for i, n := range names {
		nodes[i] = model.Label{Name: n}
	}
	m.mu.Lock()
	switch v := m.detail.(type) {
	case *model.PR:
		v.Labels.Nodes = nodes
	case *model.Issue:
		v.Labels.Nodes = nodes
	}
	m.mu.Unlock()
	app.BumpDetail()
}

func hasFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func withoutFold(list []string, s string) []string {
	var out []string
	for _, x := range list {
		if !strings.EqualFold(x, s) {
			out = append(out, x)
		}
	}
	return out
}

// doLabels (L): pick one of the repo's labels to add, or one the item
// carries to remove.
func (m *Model) doLabels(app *lockedApp) {
	t, ok := m.triageTarget(app)
	if !ok {
		return
	}
	where := m.where(t.repo, t.number)
	if m.writeRefused(app, t.repo, "labelling "+where) {
		return
	}
	defs, err := m.gh.RepoLabels(m.ctx, t.repo)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't list the labels of %s: %s", t.repo, err))
		return
	}
	if len(defs) == 0 {
		app.Notice(t.repo.String() + " defines no labels")
		return
	}
	choices := make([]string, len(defs))
	for i, d := range defs {
		mark := "[ ]"
		if hasFold(t.labels, d.Name) {
			mark = "[x]"
		}
		choices[i] = mark + " " + d.Name
	}
	i := app.Pick(fmt.Sprintf("label %s: [x] removes, [ ] adds", where), choices)
	if i < 0 {
		app.Notice("label cancelled")
		return
	}
	name := defs[i].Name
	if hasFold(t.labels, name) {
		if err := m.gh.RemoveLabel(m.ctx, t.repo, t.number, name); err != nil {
			app.Fail(fmt.Sprintf("couldn't remove %q from %s: %s", name, where, err))
			return
		}
		m.setDetailLabels(app, withoutFold(t.labels, name))
		app.OK(fmt.Sprintf("removed %q from %s", name, where))
	} else {
		if err := m.gh.AddLabels(m.ctx, t.repo, t.number, []string{name}); err != nil {
			app.Fail(fmt.Sprintf("couldn't add %q to %s: %s", name, where, err))
			return
		}
		m.setDetailLabels(app, append(append([]string(nil), t.labels...), name))
		app.OK(fmt.Sprintf("added %q to %s", name, where))
	}
	app.Refresh(true)
}

// assignable is the repo's assignable users, cached for the session.
func (m *Model) assignable(app *lockedApp, repo model.Repo) ([]model.Actor, bool) {
	m.mu.Lock()
	users, cached := m.users[repo]
	m.mu.Unlock()
	if cached {
		return users, true
	}
	u, err := m.gh.AssignableUsers(m.ctx, repo)
	if err != nil {
		app.Fail(fmt.Sprintf("couldn't list who is assignable in %s: %s", repo, err))
		return nil, false
	}
	m.mu.Lock()
	m.users[repo] = u
	m.mu.Unlock()
	return u, true
}

// doAssign (s, issues): pick someone to assign, or an assignee to
// unassign.
func (m *Model) doAssign(app *lockedApp) {
	t, ok := m.triageTarget(app)
	if !ok {
		return
	}
	where := m.where(t.repo, t.number)
	if !t.isIssue {
		app.Notice(fmt.Sprintf("%s is a pull request — s assigns issues; L labels and H hands either to the agent", where))
		return
	}
	if m.writeRefused(app, t.repo, "assigning "+where) {
		return
	}
	users, ok := m.assignable(app, t.repo)
	if !ok {
		return
	}
	m.mu.Lock()
	var current []string
	for _, a := range t.issue.Assignees.Nodes {
		current = append(current, a.Login)
	}
	m.mu.Unlock()
	// the current assignees first (one may no longer be assignable), then
	// everyone else
	var logins, choices []string
	names := map[string]string{}
	for _, u := range users {
		names[strings.ToLower(u.Login)] = u.Name
	}
	for _, l := range current {
		logins = append(logins, l)
		choices = append(choices, fmt.Sprintf("[x] @%-20s %s", l, names[strings.ToLower(l)]))
	}
	for _, u := range users {
		if !hasFold(current, u.Login) {
			logins = append(logins, u.Login)
			choices = append(choices, fmt.Sprintf("[ ] @%-20s %s", u.Login, u.Name))
		}
	}
	if len(choices) == 0 {
		app.Notice("no assignable users found in " + t.repo.String())
		return
	}
	i := app.Pick(fmt.Sprintf("assign %s: [x] unassigns, [ ] assigns", where), choices)
	if i < 0 {
		app.Notice("assign cancelled")
		return
	}
	login := logins[i]
	if hasFold(current, login) {
		if err := m.gh.RemoveAssignees(m.ctx, t.repo, t.number, []string{login}); err != nil {
			app.Fail(fmt.Sprintf("couldn't unassign @%s from %s: %s", login, where, err))
			return
		}
		m.setDetailAssignees(app, withoutFold(current, login))
		app.OK(fmt.Sprintf("unassigned @%s from %s", login, where))
	} else {
		if err := m.gh.AddAssignees(m.ctx, t.repo, t.number, []string{login}); err != nil {
			app.Fail(fmt.Sprintf("couldn't assign @%s to %s: %s", login, where, err))
			return
		}
		m.setDetailAssignees(app, append(current, login))
		app.OK(fmt.Sprintf("assigned @%s to %s", login, where))
	}
	app.Refresh(true)
}

// setDetailAssignees replaces the open issue's assignees in place.
func (m *Model) setDetailAssignees(app *lockedApp, logins []string) {
	nodes := make([]model.Actor, len(logins))
	for i, l := range logins {
		nodes[i] = model.Actor{Login: l}
	}
	m.mu.Lock()
	if v, ok := m.detail.(*model.Issue); ok {
		v.Assignees.Nodes = nodes
	}
	m.mu.Unlock()
	app.BumpDetail()
}

// doIssueState (Z, issues): close an open issue as completed or not
// planned; reopen a closed one.
func (m *Model) doIssueState(app *lockedApp) {
	t, ok := m.triageTarget(app)
	if !ok {
		return
	}
	where := m.where(t.repo, t.number)
	if !t.isIssue {
		app.Notice(fmt.Sprintf("%s is a pull request — Z closes and reopens issues only", where))
		return
	}
	if m.writeRefused(app, t.repo, "closing or reopening "+where) {
		return
	}
	m.mu.Lock()
	open := !strings.EqualFold(t.issue.State, "CLOSED")
	m.mu.Unlock()
	var state, reason, did string
	if open {
		i := app.Pick(fmt.Sprintf("close %s?", where), []string{"close as completed", "close as not planned"})
		switch i {
		case 0:
			state, reason, did = "closed", "completed", "closed %s as completed"
		case 1:
			state, reason, did = "closed", "not_planned", "closed %s as not planned"
		default:
			app.Notice("close cancelled")
			return
		}
	} else {
		if !app.Confirm(fmt.Sprintf("reopen %s?", where)) {
			app.Notice("reopen cancelled")
			return
		}
		state, reason, did = "open", "reopened", "reopened %s"
	}
	if err := m.gh.SetIssueState(m.ctx, t.repo, t.number, state, reason); err != nil {
		app.Fail(fmt.Sprintf("couldn't change %s: %s", where, err))
		return
	}
	m.mu.Lock()
	if v, ok := m.detail.(*model.Issue); ok {
		v.State = strings.ToUpper(state)
	}
	m.mu.Unlock()
	app.BumpDetail()
	app.OK(fmt.Sprintf(did, where))
	app.Refresh(true)
}

// doHandoff (H): hand the open item to the agent — add [agent]
// handoff_label and, when set, post handoff_comment. An agent that is a
// GitHub App's bot cannot be @-mentioned into a notification, so it
// watches the label instead.
func (m *Model) doHandoff(app *lockedApp) {
	t, ok := m.triageTarget(app)
	if !ok {
		return
	}
	where := m.where(t.repo, t.number)
	label := ""
	comment := ""
	if m.cfg != nil {
		label, comment = m.cfg.Agent.HandoffLabel, m.cfg.Agent.HandoffComment
	}
	if label == "" {
		app.Notice("no agent to hand to: set [agent] handoff_label in the config")
		return
	}
	if m.writeRefused(app, t.repo, "handing "+where+" to the agent") {
		return
	}
	if hasFold(t.labels, label) {
		app.Notice(fmt.Sprintf("%s already carries %q — the agent has it", where, label))
		return
	}
	q := fmt.Sprintf("hand %s to the agent: add the label %q", where, label)
	if comment != "" {
		q += " and post the handoff comment"
	}
	if !app.Confirm(q + "?") {
		app.Notice("handoff cancelled")
		return
	}
	if err := m.gh.AddLabels(m.ctx, t.repo, t.number, []string{label}); err != nil {
		app.Fail(fmt.Sprintf("couldn't add %q to %s: %s", label, where, err))
		return
	}
	m.setDetailLabels(app, append(append([]string(nil), t.labels...), label))
	if comment != "" {
		if err := m.gh.Comment(m.ctx, t.repo, t.number, comment); err != nil {
			app.Fail(fmt.Sprintf("labelled %s %q, but the handoff comment failed: %s", where, label, err))
			app.Refresh(true)
			return
		}
	}
	app.OK(fmt.Sprintf("handed %s to the agent (%q)", where, label))
	app.Celebrate(false)
	app.Refresh(true)
}

func doLabels(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doLabels(app) })
	return nil
}

func doAssign(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doAssign(app) })
	return nil
}

func doIssueState(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doIssueState(app) })
	return nil
}

func doHandoff(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doHandoff(app) })
	return nil
}
