package ui

import (
	"context"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/external"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Model implements workflow.App WITHOUT locking: Update and View hold the
// lock and call workflow hooks with the model itself. The action goroutine
// gets a lockedApp, which takes the lock around every call.

var _ workflow.App = (*Model)(nil)
var _ workflow.App = (*lockedApp)(nil)

func (m *Model) OK(msg string)       { m.ok(msg) }
func (m *Model) Notice(msg string)   { m.notice(msg) }
func (m *Model) Fail(msg string)     { m.fail(msg) }
func (m *Model) Status(msg string)   { m.setStatus(msg) }
func (m *Model) Celebrate(big bool)  { m.celebrate(big) }
func (m *Model) Launch()             { m.launch() }
func (m *Model) GH() ghapi.Client    { return m.gh }
func (m *Model) Me() string          { return m.me }
func (m *Model) Repos() []model.Repo { return m.repos }
func (m *Model) MultiRepo() bool     { return m.multi }
func (m *Model) Config() *config.Config {
	return m.cfg
}
func (m *Model) Now() time.Time           { return clock.Now() }
func (m *Model) Context() context.Context { return m.ctx }
func (m *Model) Width() int               { return m.w }
func (m *Model) State(name string) any    { return m.wfState[name] }
func (m *Model) SetState(name string, s any) {
	m.wfState[name] = s
}
func (m *Model) AllPRs() []*model.PR             { return m.allPRs }
func (m *Model) Issues() []*model.Issue          { return m.issues }
func (m *Model) LocalApproved(pr *model.PR) bool { return m.localApproved(pr) }

// MarkApproved marks the row "approved by me" instantly, before the
// server confirms it, but only while the head is unchanged.
func (m *Model) MarkApproved(pr *model.PR) {
	m.approvedByMe[pr.Ident()] = pr.HeadRefOid
	m.detailVer++
}
func (m *Model) Detail() any             { return m.detail }
func (m *Model) DetailListed() *model.PR { return m.detailListed }
func (m *Model) DetailKind() string      { return m.detailKind }
func (m *Model) CurrentView() string     { return m.view }
func (m *Model) Rows() []workflow.Row    { return m.rows }
func (m *Model) Refresh(again bool)      { m.refresh(again) }
func (m *Model) BumpDetail()             { m.detailVer++ }
func (m *Model) Selected() (workflow.Row, bool) {
	if m.selected >= 0 && m.selected < len(m.rows) && m.rows[m.selected].Kind != "header" {
		return m.rows[m.selected], true
	}
	return workflow.Row{}, false
}

// RefreshSoon pulls the next refresh in to at most d from now.
func (m *Model) RefreshSoon(d time.Duration) {
	if t := fx.Now() + d.Seconds(); t < m.nextRefreshAt {
		m.nextRefreshAt = t
	}
}

// Flash puts a spinner message on the status row ahead of a blocking fetch.
func (m *Model) Flash(msg string) {
	m.status = msg
	m.statusKind = "spin"
	m.statusAt = fx.Now()
	m.redraw()
}

// EditInEditor runs $EDITOR seeded with text (action goroutine).
func (m *Model) EditInEditor(seed string) (string, bool, error) {
	return external.EditInTemp(seed, m.RunExternal)
}

// OpenDetail opens an item's detail view (action goroutine).
func (m *Model) OpenDetail(repo model.Repo, number int, kind string, listed any) {
	var lp *model.PR
	if p, ok := listed.(*model.PR); ok {
		lp = p
	}
	m.openDetail(&lockedApp{m}, repo, number, kind, lp)
}

// lockedApp is the action goroutine's view of the model.
type lockedApp struct{ m *Model }

func (a *lockedApp) lock() func() {
	a.m.mu.Lock()
	return a.m.mu.Unlock
}

func (a *lockedApp) OK(msg string)      { defer a.lock()(); a.m.ok(msg) }
func (a *lockedApp) Notice(msg string)  { defer a.lock()(); a.m.notice(msg) }
func (a *lockedApp) Fail(msg string)    { defer a.lock()(); a.m.fail(msg) }
func (a *lockedApp) Status(msg string)  { defer a.lock()(); a.m.setStatus(msg) }
func (a *lockedApp) Celebrate(big bool) { defer a.lock()(); a.m.celebrate(big) }
func (a *lockedApp) Launch()            { defer a.lock()(); a.m.launch() }
func (a *lockedApp) Flash(msg string) {
	a.m.mu.Lock()
	a.m.status = msg
	a.m.statusKind = "spin"
	a.m.statusAt = fx.Now()
	a.m.mu.Unlock()
	a.m.redraw()
}
func (a *lockedApp) GH() ghapi.Client            { return a.m.gh }
func (a *lockedApp) Me() string                  { return a.m.me }
func (a *lockedApp) Repos() []model.Repo         { return a.m.repos }
func (a *lockedApp) MultiRepo() bool             { return a.m.multi }
func (a *lockedApp) Config() *config.Config      { return a.m.cfg }
func (a *lockedApp) Now() time.Time              { return clock.Now() }
func (a *lockedApp) Context() context.Context    { return a.m.ctx }
func (a *lockedApp) Width() int                  { defer a.lock()(); return a.m.w }
func (a *lockedApp) State(name string) any       { defer a.lock()(); return a.m.wfState[name] }
func (a *lockedApp) SetState(name string, s any) { defer a.lock()(); a.m.wfState[name] = s }
func (a *lockedApp) AllPRs() []*model.PR         { defer a.lock()(); return a.m.allPRs }
func (a *lockedApp) Issues() []*model.Issue      { defer a.lock()(); return a.m.issues }
func (a *lockedApp) LocalApproved(pr *model.PR) bool {
	defer a.lock()()
	return a.m.localApproved(pr)
}
func (a *lockedApp) MarkApproved(pr *model.PR) { defer a.lock()(); a.m.MarkApproved(pr) }
func (a *lockedApp) Detail() any               { defer a.lock()(); return a.m.detail }
func (a *lockedApp) DetailListed() *model.PR   { defer a.lock()(); return a.m.detailListed }
func (a *lockedApp) DetailKind() string        { defer a.lock()(); return a.m.detailKind }
func (a *lockedApp) CurrentView() string       { defer a.lock()(); return a.m.view }
func (a *lockedApp) Rows() []workflow.Row      { defer a.lock()(); return a.m.rows }
func (a *lockedApp) Selected() (workflow.Row, bool) {
	defer a.lock()()
	return a.m.Selected()
}
func (a *lockedApp) Refresh(again bool)          { defer a.lock()(); a.m.refresh(again) }
func (a *lockedApp) RefreshSoon(d time.Duration) { defer a.lock()(); a.m.RefreshSoon(d) }
func (a *lockedApp) BumpDetail()                 { defer a.lock()(); a.m.detailVer++ }
func (a *lockedApp) Confirm(q string) bool       { return a.m.Confirm(q) }
func (a *lockedApp) Prompt(p string, o workflow.PromptOpts) (string, bool) {
	return a.m.Prompt(p, o)
}
func (a *lockedApp) Pick(title string, labels []string) int { return a.m.Pick(title, labels) }
func (a *lockedApp) RunExternal(argv []string) error        { return a.m.RunExternal(argv) }
func (a *lockedApp) EditInEditor(seed string) (string, bool, error) {
	return external.EditInTemp(seed, a.m.RunExternal)
}
func (a *lockedApp) OpenDetail(repo model.Repo, number int, kind string, listed any) {
	var lp *model.PR
	if p, ok := listed.(*model.PR); ok {
		lp = p
	}
	a.m.openDetail(a, repo, number, kind, lp)
}
