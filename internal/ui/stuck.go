package ui

import (
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/widgets"

	tea "charm.land/bubbletea/v2"
)

// listedFor is pr as the list query read it (the detail query reads
// fewer fields), or nil when the list does not carry it (lock held).
func (m *Model) listedFor(pr *model.PR) *model.PR {
	if m.detailListed != nil && m.detailListed.Ident() == pr.Ident() {
		return m.detailListed
	}
	for _, p := range m.allPRs {
		if p.Ident() == pr.Ident() {
			return p
		}
	}
	return nil
}

// stuckSelected is the open PR (detail) or the PR row under the cursor
// (list), and why it is stuck (lock held).
func (m *Model) stuckSelected() (*model.PR, model.Stuck) {
	if m.view == ViewDetail {
		if pr, ok := m.detail.(*model.PR); ok {
			return pr, model.StuckOf(pr, m.listedFor(pr), m.me)
		}
		return nil, model.Stuck{}
	}
	if m.view != ViewList {
		return nil, model.Stuck{}
	}
	row, ok := m.Selected()
	if !ok {
		return nil, model.Stuck{}
	}
	pr, ok := row.Item.(*model.PR)
	if !ok {
		return nil, model.Stuck{}
	}
	return pr, model.StuckOf(pr, nil, m.me)
}

// staleSelected gates u: the selected PR's approval is on an older commit.
func (m *Model) staleSelected() bool {
	_, s := m.stuckSelected()
	return s.Kind == model.StuckStale && s.ApprovedSHA != "" && s.HeadSHA != ""
}

// doSinceApproval opens the compare view from the approved commit to the
// head: what changed since the approval, so a re-approve is a look at
// the delta rather than a full review.
func doSinceApproval(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	pr, s := m.stuckSelected()
	if pr == nil || s.Kind != model.StuckStale || s.ApprovedSHA == "" || s.HeadSHA == "" {
		m.notice("no stale approval here: u opens the changes since an approval of an older commit")
		return nil
	}
	m.openOrCopy(ghapi.CompareURL(m.host, m.repoOf(pr), s.ApprovedSHA, s.HeadSHA))
	return nil
}
