package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The key bar shows what can be done here: a key that acts appears when
// it acts on the selected row or the open item, labelled with what it
// will do there. The offers below are read under the lock, at draw time,
// from the model alone.

// focus is the key bar's view of the selection (lock held).
func (m *Model) focus() workflow.Focus {
	view := m.view
	if view == ViewDetail && m.detailKind == "issue" {
		view = "issue"
	}
	f := workflow.Focus{View: view, Detail: m.detail, Listed: m.detailListed, Me: m.me, Now: clock.Now(), State: m.State}
	if view == ViewList {
		f.Row, f.HasRow = m.Selected()
	}
	return f
}

func (m *Model) offerApprove() (string, bool) {
	pr, ok := m.detail.(*model.PR)
	if !ok || pr == nil || m.detailKind == "issue" {
		return "", false
	}
	if label := m.wf.ApproveOffer(m.focus()); label != "" {
		return label, true
	}
	switch {
	case !strings.EqualFold(pr.State, "OPEN") && pr.State != "":
		return "", false
	case pr.IsDraft, strings.EqualFold(pr.Author.Login, m.me):
		return "", false
	case pr.MyApprovalStands(m.me) || m.localApproved(pr):
		return "", false
	}
	return "approve", true
}

func (m *Model) offerMerge() (string, bool) {
	pr, ok := m.detail.(*model.PR)
	if !ok || pr == nil || !strings.EqualFold(pr.State, "OPEN") || pr.IsDraft {
		return "", false
	}
	if pr.ReviewDecision == "APPROVED" || m.localApproved(pr) {
		return "merge", true
	}
	return "", false
}

func (m *Model) offerDeploysList() (string, bool) {
	row, ok := m.Selected()
	if !ok {
		return "", false
	}
	if pr, ok := row.Item.(*model.PR); ok && pr != nil && strings.EqualFold(pr.State, "MERGED") {
		return "approve deploys", true
	}
	if row.Kind == "waiting_run" {
		return "approve deploys", true
	}
	return "", false
}

func (m *Model) offerDeploysDetail() (string, bool) {
	if pr, ok := m.detail.(*model.PR); ok && pr != nil && strings.EqualFold(pr.State, "MERGED") {
		return "approve deploys", true
	}
	return "", false
}

// offerRunsList: in the list x and X are offered on the rows whose runs
// are the point (a release, a waiting run, a merged PR deploying); on a
// PR under review they still work, but its detail is where to look.
func (m *Model) offerRunsList() (string, bool) {
	row, ok := m.Selected()
	if !ok {
		return "", false
	}
	if row.Kind == "release" || row.Kind == "waiting_run" {
		return "runs", true
	}
	if pr, ok := row.Item.(*model.PR); ok && pr != nil && strings.EqualFold(pr.State, "MERGED") {
		return "runs", true
	}
	return "", false
}

func (m *Model) offerRunsAct() (string, bool) {
	if m.view == ViewDetail {
		if m.detailKind == "issue" {
			return "", m.hasDispatches()
		}
		return "act on runs", true
	}
	if _, ok := m.offerRunsList(); ok {
		return "act on runs", true
	}
	if m.hasDispatches() {
		return "run workflow", true
	}
	return "", false
}

func (m *Model) offerUpdate() (string, bool) {
	switch stale, restart := m.configStale(), m.pendingRestart(); {
	case stale && restart:
		return "update + config", true
	case restart:
		return "restart into " + m.upd.Installed, true
	case stale:
		return "pull config", true
	}
	return "", false
}

func (m *Model) offerIssueState() (string, bool) {
	is, ok := m.detail.(*model.Issue)
	if !ok || is == nil {
		return "", false
	}
	if strings.EqualFold(is.State, "CLOSED") {
		return "reopen", true
	}
	return "close", true
}

func (m *Model) offerHandoff() (string, bool) {
	return "to " + m.agentName(), true
}

// agentName is how the agent H hands to is called: [agent] name, else
// "the agent".
func (m *Model) agentName() string {
	if m.cfg != nil && m.cfg.Agent.Name != "" {
		return m.cfg.Agent.Name
	}
	return "the agent"
}

// doRunsLook is x: a run's log (looking only).
func doRunsLook(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doRuns(app, false) })
	return nil
}

// doRunsAct is X: re-run, cancel or reject a run, or run a listed
// workflow.
func doRunsAct(m *Model, _ widgets.Key, _ int, app *lockedApp) tea.Cmd {
	m.runAction(func() { m.doRuns(app, true) })
	return nil
}

// doUpdate is U: the newer config and the installed build, whichever
// is waiting; one keystroke, one restart.
func doUpdate(m *Model, k widgets.Key, page int, app *lockedApp) tea.Cmd {
	switch {
	case m.configStale():
		// pulling restarts too, into whatever binary is on disk now
		return doConfigPull(m, k, page, app)
	case m.pendingRestart():
		return doRestart(m, k, page, app)
	}
	m.notice(fmt.Sprintf("dillodash %s and its config are current — U restarts into an update once one is installed", m.ver))
	return nil
}

// hintSegs is the status line when there is nothing to report: what the
// selection is and waits on, and the keys that act on it, so the next
// step is on screen ("#8 awaiting review — A /approve plan-set · C
// comment · ↵ open"). Nothing selected: what the view is for.
func (m *Model) hintSegs() []text.Seg {
	if m.overlay != "" || m.modal != nil {
		return nil
	}
	view := m.view
	if view == ViewDetail && m.detailKind == "issue" {
		view = "issue"
	}
	var subject string
	switch view {
	case ViewList:
		row, ok := m.Selected()
		if !ok {
			return nil
		}
		switch it := row.Item.(type) {
		case *model.PR:
			if it != nil {
				label, _, _, _ := m.decision(it, it)
				subject = fmt.Sprintf("%s %s", m.where(m.repoOf(it), it.Number), label)
			}
		case *model.Issue:
			if it != nil {
				subject = m.where(m.repoOf(it), it.Number)
			}
		default:
			// a workflow's row (a waiting run, a change to promote): its
			// number column and its title
			if rk, ok := m.wf.RowKinds()[row.Kind]; ok {
				var parts []string
				if rk.NumText != nil {
					parts = append(parts, rk.NumText(row))
				}
				if rk.Title != nil {
					if t := rk.Title(row); len(parts) == 0 || t != parts[0] {
						parts = append(parts, text.Ellip(t, 40))
					}
				}
				subject = strings.TrimSpace(strings.Join(parts, " "))
			}
		}
	case ViewDetail, "issue":
		switch it := m.detail.(type) {
		case *model.PR:
			if it != nil {
				label, _, _, _ := m.decision(it, m.detailListed)
				subject = fmt.Sprintf("%s %s", m.where(m.repoOf(it), it.Number), label)
			}
		case *model.Issue:
			if it != nil {
				subject = m.where(m.repoOf(it), it.Number)
			}
		}
	default:
		return nil
	}
	var acts []barItem
	for _, it := range m.barFor(view) {
		if it.acts {
			acts = append(acts, it)
		}
	}
	segs := []text.Seg{text.S(" "+glyphs.G("arrow")+" ", "hint")}
	if subject != "" {
		segs = append(segs, text.S(subject, "hint.subject"))
	}
	if len(acts) == 0 {
		enter, dot := "↵", glyphs.G("sep")
		if glyphs.Active() == glyphs.ASCII {
			enter = "enter"
		}
		next := enter + " opens it"
		if view != ViewList {
			next = "q goes back " + dot + " ? lists every key"
		}
		if subject != "" {
			next = " — " + next
		}
		return append(segs, text.S(next, "hint"))
	}
	for i, it := range acts {
		if i == 3 {
			break
		}
		lead := " " + glyphs.G("sep") + " "
		if i == 0 {
			lead = " — "
			if subject == "" {
				lead = ""
			}
		}
		segs = append(segs, text.S(lead, "hint"), text.S(it.key, "hint.key"), text.S(" "+it.label, "hint"))
	}
	return segs
}
