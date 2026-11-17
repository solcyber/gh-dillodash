package ui

import (
	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// A write shows on screen the moment GitHub accepts it, not when the
// next refresh lands. The open detail is edited in place, and the edit
// is also kept here (pending) and laid over the list's copies of the
// item, refresh after refresh, until a refresh that set off after the
// write has read GitHub's own answer. A refresh already in flight when
// the write went out can't know it: without the overlay it would put
// the old state back for a few seconds.

// pendingEdit is one item's writes this session that a refresh may not
// have read yet.
type pendingEdit struct {
	at        float64 // the latest write (fx clock)
	state     string  // "" leaves the state as read
	mergeSHA  string
	labels    []string // nil leaves the labels as read
	assignees []string // nil leaves the assignees as read
}

// noteEdit records a write to id and shows it now (lock held): on the
// list's copies, and the open detail is guarded against an older read.
func (m *Model) noteEdit(id model.Ident, edit func(e *pendingEdit)) {
	if m.pending == nil {
		m.pending = map[model.Ident]*pendingEdit{}
	}
	e := m.pending[id]
	if e == nil {
		e = &pendingEdit{}
		m.pending[id] = e
	}
	edit(e)
	e.at = fx.Now()
	m.dcache.clear()
	if d, ok := m.identOf(m.detail); ok && d == id {
		// a refresh that set off before this write must not put the
		// detail back as it was
		m.detailLoadedAt = e.at
	}
	m.refilter()
}

// prunePending drops the edits a refresh that set off at started has
// read for itself (lock held).
func (m *Model) prunePending(started float64) {
	for id, e := range m.pending {
		if started > e.at {
			delete(m.pending, id)
		}
	}
}

// overlayPending lays the pending edits over the rows' items (lock
// held; refilter calls it last).
func (m *Model) overlayPending() {
	if len(m.pending) == 0 {
		return
	}
	for i, r := range m.rows {
		if r.Kind != "pr" && r.Kind != "issue" {
			continue // a workflow's rows are its own state, which its poller reads
		}
		id, ok := m.identOf(r.Item)
		if !ok {
			continue
		}
		e := m.pending[id]
		if e == nil {
			continue
		}
		// a copy: actions read the listed objects without the lock
		switch v := r.Item.(type) {
		case *model.PR:
			c := *v
			applyEditPR(e, &c)
			m.rows[i].Item = &c
		case *model.Issue:
			c := *v
			applyEditIssue(e, &c)
			m.rows[i].Item = &c
		}
	}
}

func applyEditPR(e *pendingEdit, p *model.PR) {
	if e.state != "" {
		p.State = e.state
	}
	if e.mergeSHA != "" {
		p.MergeCommit = &model.MergeCommit{Oid: e.mergeSHA}
		if p.MergedAt == nil {
			t := clock.Now()
			p.MergedAt = &t
		}
	}
	if e.labels != nil {
		p.Labels.Nodes = labelNodes(e.labels)
	}
}

func applyEditIssue(e *pendingEdit, i *model.Issue) {
	if e.state != "" {
		i.State = e.state
	}
	if e.labels != nil {
		i.Labels.Nodes = labelNodes(e.labels)
	}
	if e.assignees != nil {
		i.Assignees.Nodes = actorNodes(e.assignees)
	}
}

func labelNodes(names []string) []model.Label {
	out := make([]model.Label, len(names))
	for i, n := range names {
		out[i] = model.Label{Name: n}
	}
	return out
}

func actorNodes(logins []string) []model.Actor {
	out := make([]model.Actor, len(logins))
	for i, l := range logins {
		out[i] = model.Actor{Login: l}
	}
	return out
}

// appendMyComment shows a comment just posted on the open item, before
// any read has it (lock held).
func (m *Model) appendMyComment(id model.Ident, body string) {
	d, ok := m.identOf(m.detail)
	if !ok || d != id {
		return
	}
	c := model.Comment{Author: model.Actor{Login: m.me}, CreatedAt: clock.Now(), Body: body}
	switch v := m.detail.(type) {
	case *model.PR:
		v.Comments.Nodes = append(v.Comments.Nodes, c)
		v.Comments.TotalCount++
	case *model.Issue:
		v.Comments.Nodes = append(v.Comments.Nodes, c)
		v.Comments.TotalCount++
	}
	m.detailLoadedAt = fx.Now()
	m.detailVer++
}

// reloadDetail reads the open item again right after a write (lock
// held), so reviews and the timeline settle in one round trip instead
// of with the next whole refresh.
func (m *Model) reloadDetail() {
	id, ok := m.identOf(m.detail)
	if !ok {
		return
	}
	kind := "pr"
	if _, isIssue := m.detail.(*model.Issue); isIssue {
		kind = "issue"
	}
	m.dcache.reread = m.readDetail(id, kind, m.detailListed, m.openSeq, false)
}

// repoIdent is an issue's identity with its repo resolved.
func (m *Model) repoIdent(i *model.Issue) model.Ident {
	return model.Ident{Repo: m.repoOf(i), Number: i.Number}
}
