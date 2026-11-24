package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/keymap"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The key bar shows what can be done here: a key that acts appears when
// it acts on the selected row or the open item, labeled with what it
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
	// no review asked for: offer the merge exactly when M would take it
	if pr.ReviewDecision == "" && changesRequestedBy(pr) == "" && canMergeUnreviewed(pr) {
		return "merge", true
	}
	return "", false
}

// deploysSettledAfter is how long after a merge a "done" deploy read is
// trusted to stay done: a deploy that starts after CI (workflow_run, a
// conductor's dispatch) can begin minutes later, on a state read before.
const deploysSettledAfter = 30 * time.Minute

// deploysDone: the merged PR's deploys were read, every run on its
// merge commit finished, and the merge is old enough that no deploy is
// still to start, so D has nothing to approve.
func deploysDone(pr *model.PR) bool {
	st, ok := pr.Get("deploy").(deploys.State)
	if !ok || (st.Phase != "done" && st.Phase != "cancelled") {
		return false
	}
	return pr.MergedAt != nil && clock.Now().Sub(*pr.MergedAt) > deploysSettledAfter
}

// browseLabel is what o opens on the selected row when the row has
// nothing to open in the dashboard (RowKind.Browse); "" otherwise.
func (m *Model) browseLabel() string {
	row, ok := m.Selected()
	if !ok {
		return ""
	}
	if rk, has := m.wf.RowKinds()[row.Kind]; has {
		return rk.Browse
	}
	return ""
}

// offerOpen: Enter opens the selected row, unless the row is a branch
// with nothing to open in the dashboard (o opens it on GitHub).
func (m *Model) offerOpen() (string, bool) {
	if _, ok := m.Selected(); !ok {
		return "", false
	}
	return "open", m.browseLabel() == ""
}

// offerBrowse: o joins the bar on a row Enter can't open.
func (m *Model) offerBrowse() (string, bool) {
	if m.view != ViewList {
		return "", false
	}
	l := m.browseLabel()
	return l, l != ""
}

func (m *Model) offerDeploysList() (string, bool) {
	row, ok := m.Selected()
	if !ok {
		return "", false
	}
	if pr, ok := row.Item.(*model.PR); ok && pr != nil && strings.EqualFold(pr.State, "MERGED") {
		return deployOffer(pr)
	}
	// a workflow's row says itself whether D is its move (a waiting run,
	// a change whose tier deploy waits)
	// (a wait nobody read whose it is names D too, without being pinned)
	if t := m.turnOf(row); t.Key == keymap.S(keymap.Deploys) && t.Verb != "" {
		return t.Verb, true
	}
	return "", false
}

func (m *Model) offerDeploysDetail() (string, bool) {
	if pr, ok := m.detail.(*model.PR); ok && pr != nil && strings.EqualFold(pr.State, "MERGED") {
		if l := m.detailListed; l != nil {
			if _, read := l.Get("deploy").(deploys.State); read {
				return deployOffer(l)
			}
		}
		return deployOffer(pr)
	}
	return "", false
}

// deployOffer is D on a merged PR: offered, under what it approves, when
// an environment waits on the viewer; not while the deploy only runs,
// failed or finished, or waits on somebody else. A PR whose deploys were
// never read (a core PR merged from here) offers D to read them.
func deployOffer(pr *model.PR) (string, bool) {
	st, ok := pr.Get("deploy").(deploys.State)
	if !ok {
		if deploysDone(pr) {
			return "", false
		}
		return "approve deploys", true
	}
	if st.Phase == "waiting" {
		if due := st.Due(); len(due) > 0 {
			return "approve " + strings.Join(due, ", "), true
		}
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
		if segs := m.turnHintSegs(row); segs != nil {
			return segs
		}
		subject = m.rowSubject(row)
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
		if l := m.browseLabel(); l != "" && view == ViewList {
			next = "o opens its " + l + " on GitHub"
		}
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

// rowSubject names a list row as the hint line does: "infra#41 awaiting
// review" for a PR, "infra#17" for an issue, a workflow row's number and
// title.
func (m *Model) rowSubject(row workflow.Row) string {
	switch it := row.Item.(type) {
	case *model.PR:
		if it != nil {
			label, _, _, _ := m.decision(it, it)
			return fmt.Sprintf("%s %s", m.where(m.repoOf(it), it.Number), label)
		}
	case *model.Issue:
		if it != nil {
			return m.where(m.repoOf(it), it.Number)
		}
	default:
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
			return strings.TrimSpace(strings.Join(parts, " "))
		}
	}
	return ""
}

// rowRef names a row briefly for a pointer to it ("#412", "infra#41").
func (m *Model) rowRef(row workflow.Row) string {
	switch it := row.Item.(type) {
	case *model.PR:
		if it != nil {
			return m.where(m.repoOf(it), it.Number)
		}
	case *model.Issue:
		if it != nil {
			return m.where(m.repoOf(it), it.Number)
		}
	}
	if rk, ok := m.wf.RowKinds()[row.Kind]; ok && rk.NumText != nil {
		return rk.NumText(row)
	}
	return ""
}

// turnHintSegs is the status line for a list row whose turn is known:
// what the row is, whose move it is and the key that makes it, or what
// is happening and that nothing is the viewer's to do; and, when the
// row is not the viewer's but another is, where n goes. nil: the row
// has no turn (an issue, a stage line), and the plain hint applies.
func (m *Model) turnHintSegs(row workflow.Row) []text.Seg {
	t := m.turnOf(row)
	if t.Kind == workflow.TurnNone {
		return nil
	}
	dot := " " + glyphs.G("sep") + " "
	subject := m.rowSubject(row)
	var segs []text.Seg
	lead := func(icon, style string) {
		segs = append(segs, text.S(" "+icon+" ", style))
		if subject != "" {
			segs = append(segs, text.S(subject, "hint.subject"))
		}
	}
	key := func(k, label string) {
		segs = append(segs, text.S(k, "hint.key"), text.S(" "+label, "hint"))
	}
	now := clock.Now()
	switch t.Kind {
	case workflow.TurnYou:
		lead(glyphs.G("arrow"), "hint.key")
		waited := ""
		if !t.Since.IsZero() && now.Sub(t.Since) >= time.Minute {
			waited = " (waiting " + deploys.FmtSecs(now.Sub(t.Since)) + ")"
		}
		segs = append(segs, text.S(" — your move"+waited+": ", "hint"))
		key(t.Key, t.Verb)
		if at, total := m.turnIndex(); total > 1 && at > 0 {
			segs = append(segs, text.S(dot, "hint"))
			key("n", fmt.Sprintf("next (%d of %d)", at, total))
		}
		return segs
	case workflow.TurnRunning:
		lead(glyphs.G("sync"), "hint")
		what := t.What
		if what == "" {
			what = "running"
		}
		segs = append(segs, text.S(" — "+what+m.landsText(t.ETA)+dot+"nothing for you to do", "hint"))
		if _, ok := m.offerRunsList(); ok {
			segs = append(segs, text.S(dot, "hint"))
			key("x", "follows its log")
		}
	case workflow.TurnOthers:
		lead(glyphs.G("clock"), "hint")
		segs = append(segs, text.S(" — waits on "+t.What+dot+"not yours", "hint"))
		if l := m.browseLabel(); l != "" {
			segs = append(segs, text.S(dot, "hint"))
			key("o", "opens its "+l)
		}
	case workflow.TurnFailed:
		lead(glyphs.G("err"), "hint")
		segs = append(segs, text.S(" — "+t.What, "hint"))
		if _, ok := m.offerRunsList(); ok {
			segs = append(segs, text.S(dot, "hint"))
			key("x", "reads the log")
			segs = append(segs, text.S(dot, "hint"))
			key("X", "re-runs")
		}
	case workflow.TurnDone:
		lead(glyphs.G("pass"), "hint")
		if t.What != "" {
			segs = append(segs, text.S(" — "+t.What, "hint"))
		}
	}
	// not the viewer's: point at the next that is
	if i := m.nextTurn(1); i >= 0 {
		nt := m.turnOf(m.rows[i])
		_, total := m.turnIndex()
		if total == 0 {
			total = m.countTurns().you
		}
		segs = append(segs, text.S(dot, "hint"))
		key("n", fmt.Sprintf("%s %s: %s (%d on you)", glyphs.G("arrow"), m.rowRef(m.rows[i]), nt.Verb, total))
	}
	return segs
}
