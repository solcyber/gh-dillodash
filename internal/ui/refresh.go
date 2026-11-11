package ui

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
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

// refreshListMsg is the first load's list, sent as soon as it is read:
// the workflows, CI and the fortress arrive with refreshDoneMsg.
type refreshListMsg struct {
	allPRs []*model.PR
	issues []*model.Issue
}

// clonePRList copies the PRs (and their Ext maps) so the UI goroutine
// can draw them while the worker annotates the originals.
func clonePRList(in []*model.PR) []*model.PR {
	out := make([]*model.PR, len(in))
	for i, p := range in {
		c := *p
		c.Ext = maps.Clone(p.Ext)
		out[i] = &c
	}
	return out
}

// applyList lands the first load's list while the rest of the refresh
// runs: rows and the cascade now, workflow sections when it lands. Until
// then listOnly holds the keys that act (a workflow may own them).
func (m *Model) applyList(msg refreshListMsg) {
	if m.loaded {
		return // a refresh already landed whole
	}
	m.loaded = true
	m.listOnly = true
	m.allPRs = msg.allPRs
	m.issues = msg.issues
	m.refilter()
	m.noteDeltas()
}

// refreshDue reports whether the auto-refresh clock ran out.
func (m *Model) refreshDue() bool { return fx.Now() >= m.nextRefreshAt }

// refresh kicks off a background refresh; the UI keeps running and the
// result is applied by applyRefresh on a later tick. again=true: if a
// refresh is already in flight, run another as soon as it lands (its
// data predates whatever the caller just did).
func (m *Model) refresh(again bool) {
	// honor a GitHub-imposed backoff: retrying while rate-limited keeps
	// the token pinned at the wall (and extends secondary-limit penalties)
	if m.backoff.RateLimitWait(clock.Now()) > 0 {
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
	gh, repos, requested, wf, ctx := m.gh, m.repos, m.requested, m.wf, m.ctx
	openFirst, firstTier := m.cfg.OpenFirstTier(), ""
	if len(openFirst) > 0 {
		firstTier = m.cfg.Promote.First()
	}
	ci, ahead, me := m.ci, m.ahead, m.me
	firstLoad := !m.loaded
	// one Shared per refresh: two readers of the same URL (two workflows
	// reading one compare) cost one request
	ctx = ghapi.WithShared(ctx)
	go func() {
		out := &refreshResult{started: started}
		var running *workflow.Refreshing
		defer func() {
			if r := recover(); r != nil {
				// the worker must never die silently
				out.err = fmt.Sprintf("refresh failed: %v", r)
			}
			if running != nil && out.err != "" {
				running.Abort() // the list failed: Finish never ran
			}
			m.send(refreshDoneMsg{out})
		}()
		if flushCI {
			ci.Reset() // re-check settled CI, and re-try repos whose token was fixed
		}
		// the workflows that read their own repos start now, beside the
		// list reads; the rest wait for the list (Finish)
		running = wf.Start(ctx, gh, repos)
		// the list, my review requests and the open first-tier PRs side
		// by side
		var (
			results    map[model.Repo]ghapi.ListResult
			rq, op     ghapi.ListResult
			err        error
			rqErr      error
			opErr      error
			lists      sync.WaitGroup
			wantOpenTo = len(requested) > 0 && len(openFirst) > 0
		)
		lists.Add(1)
		go func() { defer lists.Done(); results, err = gh.ListOpen(ctx, repos) }()
		if len(requested) > 0 {
			lists.Add(1)
			go func() { defer lists.Done(); rq, rqErr = gh.ListRequested(ctx, requested) }()
		}
		if wantOpenTo {
			lists.Add(1)
			go func() { defer lists.Done(); op, opErr = gh.ListOpenInto(ctx, openFirst, firstTier) }()
		}
		lists.Wait()
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
		if len(requested) > 0 {
			// the repos listed for my review requests only: their PRs
			// join the list (a PR the repo list already read is skipped)
			if rqErr != nil {
				if e, ok := ghapi.AsError(rqErr); ok && e.IsRateLimit() {
					out.err, out.retryAt = rqErr.Error(), e.RetryAt
					return
				}
				warns = append(warns, "requested repos: "+rqErr.Error())
			} else {
				have := make(map[model.Ident]bool, len(out.allPRs))
				for _, p := range out.allPRs {
					have[p.Ident()] = true
				}
				for _, p := range rq.PRs {
					if !have[p.Ident()] {
						have[p.Ident()] = true
						out.allPRs = append(out.allPRs, p)
					}
				}
				warns = append(warns, rq.Notes...)
				if wantOpenTo {
					// promote's first_tier_reviews = "open": every open PR
					// into the first tier joins, whoever opened it and
					// whether or not a review was requested, GitHub Apps
					// (dependabot) aside; an agent's account is a person
					// here even when [bots] lists it
					if opErr != nil {
						if e, ok := ghapi.AsError(opErr); ok && e.IsRateLimit() {
							out.err, out.retryAt = opErr.Error(), e.RetryAt
							return
						}
						warns = append(warns, "open "+firstTier+" PRs: "+opErr.Error())
					} else {
						for _, p := range op.PRs {
							if !have[p.Ident()] && !p.Author.IsApp() {
								have[p.Ident()] = true
								out.allPRs = append(out.allPRs, p)
							}
						}
						warns = append(warns, op.Notes...)
					}
				}
			}
		}
		ahead.annotate(ctx, gh, out.allPRs, me)
		sort.SliceStable(out.allPRs, func(i, j int) bool {
			return out.allPRs[i].CreatedAt.Before(out.allPRs[j].CreatedAt)
		})
		sort.SliceStable(out.issues, func(i, j int) bool {
			return out.issues[i].UpdatedAt.After(out.issues[j].UpdatedAt)
		})
		if firstLoad {
			// the first load shows the list now; the workflows, CI and the
			// rest follow when the whole refresh lands. Copies, because the
			// worker goes on annotating these PRs.
			m.send(refreshListMsg{allPRs: clonePRList(out.allPRs), issues: append([]*model.Issue(nil), out.issues...)})
		}
		states, wfWarns, wfErr := running.Finish(out.allPRs, out.issues)
		running = nil
		out.wfStates = states
		warns = append(warns, wfWarns...)
		if e, ok := ghapi.AsError(wfErr); ok && e.IsRateLimit() {
			// the list landed; only the workflows hit the wall, so the
			// list shows and the next refresh waits for the window
			out.retryAt = e.RetryAt
			warns = append([]string{"rate limited — workflow data paused until " + e.RetryAt.Local().Format("15:04:05")}, warns...)
		}
		// one CI pass for the review PRs and the PRs the workflows contribute
		// (releases): one cache, one set of live SHAs, one Actions call per SHA
		ciPRs := append(append([]*model.PR{}, out.allPRs...), wf.CIPRs(states)...)
		clusters, ciWarn, err := ci.Annotate(ctx, clock.Now(), ciPRs, gh.WorkflowRuns)
		if err != nil {
			out.err = err.Error()
			if e, ok := ghapi.AsError(err); ok && e.IsRateLimit() {
				out.retryAt = e.RetryAt
			}
			return
		}
		out.ci = map[model.Ident]model.CICluster{}
		for i, pr := range ciPRs {
			out.ci[pr.Ident()] = clusters[i]
			if i >= len(out.allPRs) {
				pr.Set(workflow.CIKey, clusters[i]) // a workflow's PR: its renderers read Ext
			}
		}
		if ciWarn != "" {
			warns = append([]string{"degraded: " + ciWarn}, warns...)
		}
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
		now := clock.Now()
		until := m.backoff.Fail(now, out.retryAt)
		if out.retryAt.IsZero() {
			if t := fx.Now() + until.Sub(now).Seconds(); t < m.nextRefreshAt {
				m.nextRefreshAt = t
			}
		}
	} else {
		// a clean refresh means the wall has lifted, unless a workflow
		// just hit it (retryAt set with the list still delivered)
		m.backoff.OK()
		if !out.retryAt.IsZero() {
			m.backoff.RateLimit(out.retryAt)
		}
		m.loaded = true
		// the list-only pass already cascaded the rows in: this one only
		// takes the baseline, so the workflow rows don't glint as new
		m.sigQuiet = m.listOnly
		m.listOnly = false
		m.allPRs = out.allPRs
		m.issues = out.issues
		m.ciClusters = out.ci
		seq := m.msgSeq
		m.wf.ApplyAll(m, out.wfStates)
		// a workflow that spoke while applying (a release landed, or
		// FAILED) keeps its message over this pass's own
		spoke := m.msgSeq != seq
		m.refreshSeq++
		m.schedulePoll() // the refresh re-derived everything a poll watches
		if out.hasFortress && m.fortress != nil {
			m.fortress.data = out.fortress
		}
		m.refilter()
		if !spoke {
			m.errText = ""
		}
		m.applyRestore()
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
		m.lastRefreshWall = m.wallNow().Format("15:04:05")
		// a generation, not the wall second: two refreshes in one second
		// with the same counts must still rebuild the fortress
		m.refreshGen++
		m.refreshStamp = fmt.Sprintf("%d|%d|%d", m.refreshGen, len(m.allPRs), len(m.issues))
		m.noteDeltas()
		if m.manualPending && m.errText == "" && !spoke {
			m.ok("refreshed at " + m.lastRefreshWall)
		}
		m.manualPending = false
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
	if m.sigQuiet {
		m.sigQuiet = false
		return
	}
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
	if wait := int(m.backoff.RateLimitWait(clock.Now()).Seconds()); wait > 0 {
		m.notice(fmt.Sprintf("rate limited — refresh held %ds; resumes when GitHub's window resets", wait))
		return
	}
	m.setStatus("refreshing...")
	m.ciFlush = true // re-check settled CI too (e.g. a re-run)
	m.manualPending = true
	m.refresh(true)
}
