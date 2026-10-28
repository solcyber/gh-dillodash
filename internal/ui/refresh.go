package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// refreshResult is what the worker hands back.
type refreshResult struct {
	started     float64
	allPRs      []*model.PR
	issues      []*model.Issue
	ci          map[model.Ident]model.CICluster
	warn        string
	wfStates    map[string]workflow.State
	fortress    map[model.Repo]ghapi.FortressData
	hasFortress bool
	detail      any
	detailErr   string
	err         string
	retryAt     time.Time
}

type refreshDoneMsg struct{ out *refreshResult }

// refreshDue reports whether the auto-refresh clock ran out.
func (m *Model) refreshDue() bool { return fx.Now() >= m.nextRefreshAt }

// refresh kicks off a background refresh; the UI keeps running and the
// result is applied by applyRefresh on a later tick. again=true: if a
// refresh is already in flight, run another as soon as it lands (its
// data predates whatever the caller just did).
func (m *Model) refresh(again bool) {
	// honor a GitHub-imposed backoff: retrying while rate-limited keeps
	// the token pinned at the wall (and extends secondary-limit penalties)
	if time.Now().Before(m.rateLimitedUntil) {
		return
	}
	if m.refreshing {
		if again {
			m.refreshAgain = true
		}
		return
	}
	var detailRepo model.Repo
	detailNumber := 0
	detailKind := m.detailKind
	var detailListed *model.PR
	switch d := m.detail.(type) {
	case *model.PR:
		detailRepo, detailNumber = m.repoOf(d), d.Number
		detailListed = m.detailListed
	case *model.Issue:
		detailRepo, detailNumber = m.repoOf(d), d.Number
	}
	started := fx.Now()
	flushCI := m.ciFlush
	m.ciFlush = false
	wantFortress := m.fortress != nil && m.fortress.wanted
	m.refreshing = true
	m.lastRefresh = started
	m.nextRefreshAt = started + float64(m.cfg.Display.RefreshSecs)
	gh, repos, wf, ctx := m.gh, m.repos, m.wf, m.ctx
	ci := m.ci
	go func() {
		out := &refreshResult{started: started}
		defer func() {
			if r := recover(); r != nil {
				// the worker must never die silently
				out.err = fmt.Sprintf("refresh failed: %v", r)
			}
			m.send(refreshDoneMsg{out})
		}()
		if flushCI {
			ci.Flush()
		}
		results, err := gh.ListOpen(ctx, repos)
		if err != nil {
			out.err = err.Error()
			if e, ok := ghapi.AsError(err); ok && e.IsRateLimit() {
				out.retryAt = e.RetryAt
			}
			return
		}
		var warns []string
		for _, repo := range repos {
			r := results[repo]
			if r.Err != nil {
				warns = append(warns, fmt.Sprintf("%s: %s", repo.Name, r.Err))
				continue
			}
			out.allPRs = append(out.allPRs, r.PRs...)
			out.issues = append(out.issues, r.Issues...)
			for _, n := range r.Notes {
				if len(repos) > 1 {
					n = repo.Name + ": " + n
				}
				warns = append(warns, n)
			}
		}
		sort.SliceStable(out.allPRs, func(i, j int) bool {
			return out.allPRs[i].CreatedAt.Before(out.allPRs[j].CreatedAt)
		})
		sort.SliceStable(out.issues, func(i, j int) bool {
			return out.issues[i].UpdatedAt.After(out.issues[j].UpdatedAt)
		})
		clusters, ciWarn, err := ci.Annotate(ctx, time.Now(), out.allPRs, gh.WorkflowRuns)
		if err != nil {
			out.err = err.Error()
			if e, ok := ghapi.AsError(err); ok && e.IsRateLimit() {
				out.retryAt = e.RetryAt
			}
			return
		}
		out.ci = map[model.Ident]model.CICluster{}
		for i, pr := range out.allPRs {
			if i < len(clusters) {
				out.ci[pr.Ident()] = clusters[i]
			}
		}
		if ciWarn != "" {
			warns = append([]string{"degraded: " + ciWarn}, warns...)
		}
		states, wfWarns := wf.RefreshAll(ctx, gh, repos, out.allPRs, out.issues)
		out.wfStates = states
		warns = append(warns, wfWarns...)
		if wantFortress {
			fort, err := gh.Fortress(ctx, repos)
			if err != nil {
				if e, ok := ghapi.AsError(err); ok && e.IsRateLimit() {
					out.err, out.retryAt = err.Error(), e.RetryAt
					return
				}
				warns = append(warns, "fortress: "+err.Error())
			} else {
				out.fortress, out.hasFortress = fort, true
			}
		}
		out.warn = strings.Join(warns, "; ")
		if detailNumber != 0 {
			var d any
			var derr error
			if detailKind == "issue" {
				d, derr = gh.IssueDetail(ctx, detailRepo, detailNumber)
			} else {
				d, derr = fetchPRDetail(ctx, gh, wf, detailRepo, detailNumber, detailListed)
			}
			if derr != nil {
				if e, ok := ghapi.AsError(derr); ok && e.IsRateLimit() {
					out.err, out.retryAt = derr.Error(), e.RetryAt
					return
				}
				// the list refreshed fine; don't throw it away
				out.detailErr = derr.Error()
			} else {
				out.detail = d
			}
		}
	}()
}

// fetchPRDetail is the detail query plus the workflows' enrichment; the
// PR carries its repo. A free function so the worker needs no model.
func fetchPRDetail(ctx context.Context, gh ghapi.Client, wf *workflow.Set, repo model.Repo, number int, listed *model.PR) (*model.PR, error) {
	pr, err := gh.PRDetail(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	pr.Repo = repo
	wf.EnrichDetail(ctx, gh, pr, listed)
	return pr, nil
}

// fetchPRDetail is the action goroutine's form (gh and wf never change).
func (m *Model) fetchPRDetail(ctx context.Context, repo model.Repo, number int, listed *model.PR) (*model.PR, error) {
	return fetchPRDetail(ctx, m.gh, m.wf, repo, number, listed)
}

// applyRefresh lands a worker result on the UI goroutine.
func (m *Model) applyRefresh(out *refreshResult) {
	m.refreshing = false
	if out.err != "" {
		m.fail(out.err)
		if !out.retryAt.IsZero() {
			m.rateLimitedUntil = out.retryAt
		} else {
			m.failStreak++
			delay := 15.0
			for i := 1; i < m.failStreak && i <= 8; i++ {
				delay *= 2
			}
			if delay > float64(m.cfg.Display.RefreshSecs) {
				delay = float64(m.cfg.Display.RefreshSecs)
			}
			if t := fx.Now() + delay; t < m.nextRefreshAt {
				m.nextRefreshAt = t
			}
		}
	} else {
		// a clean refresh means the wall has lifted
		m.rateLimitedUntil = time.Time{}
		m.failStreak = 0
		m.loaded = true
		m.allPRs = out.allPRs
		m.issues = out.issues
		m.ciClusters = out.ci
		m.wf.ApplyAll(m, out.wfStates)
		if out.hasFortress && m.fortress != nil {
			m.fortress.data = out.fortress
		}
		m.refilter()
		m.errText = ""
		// the listed PR behind an open detail follows the fresh list, so
		// cards and the header icon read this refresh's data; one that
		// left the list keeps its last copy (nothing newer exists)
		if m.detailListed != nil {
			want := m.detailListed.Ident()
			for _, r := range m.rows {
				if p, ok := r.Item.(*model.PR); ok && p.Ident() == want {
					m.detailListed = p
					break
				}
			}
		}
		// only when this refresh set off AFTER the detail was last fetched
		// by hand: an older result must not replace newer data
		if out.detail != nil && m.detail != nil && sameItem(out.detail, m.detail) && out.started >= m.detailLoadedAt {
			m.detail = out.detail
			m.detailVer++
		}
		if out.detailErr != "" && m.detail != nil {
			m.errText = fmt.Sprintf("couldn't refresh %s: %s", m.detailRef(), out.detailErr)
		}
		m.warn = out.warn
		m.lastRefreshWall = clock.Now().Local().Format("15:04:05")
		m.noteDeltas()
		if m.status == "refreshing..." && m.errText == "" {
			m.ok("refreshed at " + m.lastRefreshWall)
		}
	}
	if m.refreshAgain {
		m.refreshAgain = false
		m.refresh(false)
	}
}

func sameItem(a, b any) bool {
	switch x := a.(type) {
	case *model.PR:
		y, ok := b.(*model.PR)
		return ok && x.Ident() == y.Ident()
	case *model.Issue:
		y, ok := b.(*model.Issue)
		return ok && x.Ident() == y.Ident()
	}
	return false
}

// detailRef is "#N"/"repo#N" of the open detail.
func (m *Model) detailRef() string {
	switch d := m.detail.(type) {
	case *model.PR:
		return m.where(m.repoOf(d), d.Number)
	case *model.Issue:
		return m.where(m.repoOf(d), d.Number)
	}
	return ""
}

// ciOf is a PR's CI cluster from the last refresh.
func (m *Model) ciOf(pr *model.PR) (model.CICluster, bool) {
	c, ok := m.ciClusters[pr.Ident()]
	return c, ok
}

// noteDeltas marks what this refresh changed, for the eye: the first
// load cascades the list in; later, a new PR or issue glints gold in the
// gutter with its number pulsing, an updated one pulses, and a check that
// just started failing flashes.
func (m *Model) noteDeltas() {
	sig := map[model.Ident]sigEntry{}
	for _, p := range m.allPRs {
		c, _ := m.ciOf(p)
		sig[p.Ident()] = sigEntry{p.UpdatedAt, len(c.Failed) > 0}
	}
	for _, i := range m.issues {
		sig[i.Ident()] = sigEntry{i.UpdatedAt, false}
	}
	for _, r := range m.rows {
		if r.Kind == "header" || r.Kind == "pr" || r.Kind == "issue" {
			continue
		}
		if p, ok := r.Item.(*model.PR); ok {
			c, _ := m.ciOf(p)
			sig[p.Ident()] = sigEntry{p.UpdatedAt, len(c.Failed) > 0}
		}
	}
	old := m.sig
	m.sig = sig
	if old == nil {
		if _, splash := m.fx.Elapsed("splash"); !splash { // else endSplash does
			m.startCascade()
		}
		return
	}
	for n, e := range sig {
		o, had := old[n]
		if !had {
			m.fx.Start("new:"+identKey(n), 2.0, nil)
			continue
		}
		if !e.updated.Equal(o.updated) {
			m.fx.Start("upd:"+identKey(n), 1.2, nil)
		}
		if e.failing && !o.failing {
			m.fx.Start("cifail:"+identKey(n), 1.6, nil)
		}
	}
}

func identKey(n model.Ident) string { return n.String() }

// manualRefresh is the 'r' key: refresh now, but if GitHub has us in a
// rate-limit backoff, say so instead of silently doing nothing.
func (m *Model) manualRefresh() {
	if wait := int(time.Until(m.rateLimitedUntil).Seconds()); wait > 0 {
		m.notice(fmt.Sprintf("rate-limited — refresh held %ds; auto-resumes when GitHub's window resets", wait))
		return
	}
	m.setStatus("refreshing...")
	m.ciFlush = true // re-check settled CI too (e.g. a re-run)
	m.refresh(true)
}
